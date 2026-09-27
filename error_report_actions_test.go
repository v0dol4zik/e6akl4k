package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestClassifyErrorReport(t *testing.T) {
	for _, test := range []struct {
		message, want string
	}{
		{"ERROR: [youtube] abc: Video unavailable", errorClassExpected},
		{"ERROR: [youtube] abc: Private video. Sign in if you've been granted access to this video", errorClassExpected},
		{errNothingFound.Error(), errorClassExpected},
		{"[1/3] Video unavailable\n[2/3] Private video\n… +1", errorClassExpected},
		{"WARNING: something odd\nERROR: [youtube] abc: Video unavailable", errorClassExpected},
		{"ERROR: [youtube] abc: Video unavailable. This content isn't available, try again later", errorClassReal},
		{"ERROR: unable to download video data: HTTP Error 403: Forbidden", errorClassReal},
		{"yt-dlp exited", errorClassReal},
		{"[1/3] Video unavailable\n[2/3] telegram upload failed", errorClassReal},
		{"   ", errorClassReal},
	} {
		if got := classifyErrorReport(test.message); got != test.want {
			t.Errorf("%q: got %s, want %s", test.message, got, test.want)
		}
	}
}

func TestErrorSpikeNeedsDistinctUsers(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	reporter := newErrorReporter(-1001)
	reporter.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if _, spiked := reporter.spike("f", 1, "download", "r1"); spiked {
			t.Fatal("one user repeating an error is not a spike")
		}
	}
	if _, spiked := reporter.spike("f", 0, "download", "r1"); spiked {
		t.Fatal("unknown users must not count")
	}
	reporter.spike("f", 2, "download", "r2")
	users, spiked := reporter.spike("f", 3, "download", "r3")
	if !spiked || users != errorSpikeUsers {
		t.Fatalf("three users must make a spike: users=%d spiked=%v", users, spiked)
	}
	if record := reporter.lastSpikeRecord(); record.ID != "r3" || record.Users != 3 || !record.At.Equal(now) {
		t.Fatalf("last spike=%#v", record)
	}
	if _, spiked := reporter.spike("f", 4, "download", "r4"); spiked {
		t.Fatal("a spike must not repeat inside the cooldown")
	}
	now = now.Add(errorSpikeCooldown)
	reporter.spike("f", 5, "download", "r5")
	reporter.spike("f", 6, "download", "r6")
	if _, spiked := reporter.spike("f", 7, "download", "r7"); !spiked {
		t.Fatal("a new spike after the cooldown must alert again")
	}
	now = now.Add(errorSpikeWindow)
	if users, spiked := reporter.spike("f", 8, "download", "r8"); spiked || users != 1 {
		t.Fatalf("users outside the window must be forgotten: users=%d", users)
	}
}

func TestReportErrorStoresExpectedAndAlertsOnSpike(t *testing.T) {
	telegram := &statusTestTelegram{}
	application, state, _, _ := newStatusTestApp(t, telegram)
	reporter := application.errorReports
	reporter.interval = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go application.runErrorReporter(ctx, reporter)
	collect := func(want int) []statusTestCall {
		var calls []statusTestCall
		deadline := time.Now().Add(2 * time.Second)
		for len(calls) < want && time.Now().Before(deadline) {
			calls = append(calls, telegram.snapshot()...)
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		return append(calls, telegram.snapshot()...)
	}

	application.reportError(errorReport{Stage: "preview", UserID: 11, URL: "https://youtu.be/gone", Error: "ERROR: [youtube] gone: Video unavailable"})
	if calls := collect(0); len(calls) != 0 {
		t.Fatalf("expected failures wait for the digest: %#v", calls)
	}
	forbidden := errorReport{Stage: "download", UserID: 11, URL: "https://youtu.be/abc", Format: "mp3 320", Error: "HTTP Error 403: Forbidden"}
	application.reportError(forbidden)
	calls := collect(1)
	first, ok := state.lastRealErrorReport(context.Background())
	if !ok || len(calls) != 1 || !calls[0].silent || !strings.Contains(calls[0].text, first.ID) || !strings.Contains(calls[0].text, "версия: <code>") {
		t.Fatalf("a single real failure must be posted silently with its ID and version: %#v", calls)
	}
	for _, button := range []string{"erp:retry:" + first.ID, "erp:fix:" + first.ID, "erp:copy:" + first.ID} {
		if !strings.Contains(calls[0].markup, button) {
			t.Fatalf("report lacks button %s: %s", button, calls[0].markup)
		}
	}

	forbidden.UserID = 10
	application.reportError(forbidden)
	forbidden.UserID = 12
	application.reportError(forbidden)
	if calls := collect(0); len(calls) != 0 {
		t.Fatalf("a repeat inside the dedup window must be folded, and an admin is not an affected user: %#v", calls)
	}
	forbidden.UserID = 13
	application.reportError(forbidden)
	calls = collect(1)
	if len(calls) != 1 || calls[0].silent || !strings.HasPrefix(calls[0].text, "🚨") {
		t.Fatalf("the third user must trigger a loud spike alert: %#v", calls)
	}
	real, expected, err := state.errorReportCounts(context.Background(), time.Now().Add(-time.Hour))
	if err != nil || real != 4 || expected != 1 {
		t.Fatalf("real=%d expected=%d err=%v", real, expected, err)
	}
}

func TestErrorReportStoreClaimsAndDigest(t *testing.T) {
	_, state, _, _ := newStatusTestApp(t, &statusTestTelegram{})
	ctx := context.Background()
	now := time.Now()
	for i, record := range []errorReportRecord{
		{ID: "r1", Stage: "preview", Class: errorClassExpected, Fingerprint: "gone", UserID: 1, Error: "gone", CreatedAt: now},
		{ID: "r2", Stage: "preview", Class: errorClassExpected, Fingerprint: "gone", UserID: 1, Error: "gone", CreatedAt: now},
		{ID: "r3", Stage: "preview", Class: errorClassExpected, Fingerprint: "gone", ChatID: 5, Error: "gone", CreatedAt: now},
		{ID: "r4", Stage: "search", Class: errorClassExpected, Fingerprint: "none", UserID: 2, Error: "nothing", CreatedAt: now},
		{ID: "r5", Stage: "download", Class: errorClassReal, Fingerprint: "boom", UserID: 3, Error: "boom", CreatedAt: now},
		{ID: "r6", Stage: "download", Class: errorClassExpected, Fingerprint: "old", UserID: 3, Error: "old", CreatedAt: now.Add(-errorReportRetention - time.Hour)},
	} {
		if err := state.saveErrorReport(ctx, record); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if err := state.saveErrorReport(ctx, errorReportRecord{ID: "r1", Fingerprint: "x", CreatedAt: now}); !isUniqueViolation(err) {
		t.Fatalf("duplicate ID must be a unique violation: %v", err)
	}
	rows, err := state.errorDigest(ctx, now.Add(-time.Hour), 10)
	if err != nil || len(rows) != 2 || rows[0].Stage != "preview" || rows[0].Count != 3 || rows[0].Users != 2 || rows[1].Count != 1 {
		t.Fatalf("digest=%#v err=%v", rows, err)
	}
	if claimed, err := state.resolveErrorReport(ctx, "r5", 10); err != nil || !claimed {
		t.Fatalf("first claim=%v err=%v", claimed, err)
	}
	if claimed, _ := state.resolveErrorReport(ctx, "r5", 11); claimed {
		t.Fatal("a report must be claimed only once")
	}
	if record, _ := state.errorReportByID(ctx, "r5"); record.ResolvedBy != 10 || record.ResolvedAt.IsZero() {
		t.Fatalf("resolved record=%#v", record)
	}
	state.unresolveErrorReport(ctx, "r5")
	if claimed, _ := state.resolveErrorReport(ctx, "r5", 11); !claimed {
		t.Fatal("a released claim must be claimable again")
	}
	if err := state.pruneErrorReports(ctx, now.Add(-errorReportRetention)); err != nil {
		t.Fatal(err)
	}
	if _, err := state.errorReportByID(ctx, "r6"); err == nil {
		t.Fatal("old reports must be pruned")
	}
}

func TestMaybePostErrorDigest(t *testing.T) {
	telegram := &statusTestTelegram{}
	application, state, _, _ := newStatusTestApp(t, telegram)
	ctx := context.Background()
	start := time.Now().Truncate(time.Second)
	application.maybePostErrorDigest(ctx, -1001, 24*time.Hour, start)
	if calls := telegram.snapshot(); len(calls) != 0 || state.metadata(ctx, errorDigestMetadataKey) == "" {
		t.Fatalf("the first run only starts the window: %#v", calls)
	}
	for i, user := range []int64{1, 2, 2} {
		record := errorReportRecord{ID: "d" + string(rune('a'+i)), Stage: "preview", Class: errorClassExpected, Fingerprint: "gone", UserID: user, Error: "Video <unavailable>", CreatedAt: start.Add(time.Minute)}
		if err := state.saveErrorReport(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	application.maybePostErrorDigest(ctx, -1001, 24*time.Hour, start.Add(23*time.Hour))
	if calls := telegram.snapshot(); len(calls) != 0 {
		t.Fatalf("the digest must wait for the interval: %#v", calls)
	}
	application.maybePostErrorDigest(ctx, -1001, 24*time.Hour, start.Add(24*time.Hour))
	calls := telegram.snapshot()
	if len(calls) != 1 || !calls[0].silent || calls[0].chatID != "-1001" {
		t.Fatalf("digest must be posted silently: %#v", calls)
	}
	for _, want := range []string{"за 24 ч", "ожидаемых: 3, настоящих: 0", "×3", "пользователей 2", "Video &lt;unavailable&gt;"} {
		if !strings.Contains(calls[0].text, want) {
			t.Errorf("digest lacks %q:\n%s", want, calls[0].text)
		}
	}
	application.maybePostErrorDigest(ctx, -1001, 24*time.Hour, start.Add(48*time.Hour))
	if calls := telegram.snapshot(); len(calls) != 0 {
		t.Fatalf("an empty window must not be posted: %#v", calls)
	}
	if got := metadataTime(state.metadata(ctx, errorDigestMetadataKey)); !got.Equal(start.Add(48 * time.Hour)) {
		t.Fatalf("the window must advance: %v", got)
	}
	rest := formatErrorDigest([]errorDigestRow{{Stage: "preview", Sample: "x", Count: 2, Users: 1}}, 1, 5, 24*time.Hour, "en")
	if !strings.Contains(rest, "…and 3 more") {
		t.Fatalf("digest must count rows it did not show:\n%s", rest)
	}
}

func TestErrorReportButtons(t *testing.T) {
	telegram := &statusTestTelegram{}
	application, state, _, _ := newStatusTestApp(t, telegram)
	application.errorReports = nil
	application.setLang(987654321, "en")
	ctx := context.Background()
	record := errorReportRecord{ID: "rtest001", Stage: "download", Class: errorClassReal, Fingerprint: "x", ChatID: 987654321, UserID: 987654321,
		URL: "https://www.youtube.com/watch?v=abc&si=tracking", Format: "mp3 320", Error: "HTTP Error 403: Forbidden", Version: "abc1234 (2026-09-26)", CreatedAt: time.Now()}
	if err := state.saveErrorReport(ctx, record); err != nil {
		t.Fatal(err)
	}
	press := func(from int64, data string) {
		application.handleErrorReportCallback(&tgbotapi.CallbackQuery{
			From: &tgbotapi.User{ID: from}, Data: data,
			Message: &tgbotapi.Message{MessageID: 55, Chat: &tgbotapi.Chat{ID: -1001}},
		})
	}
	waitFor := func(match func(statusTestCall) bool) []statusTestCall {
		t.Helper()
		var calls []statusTestCall
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			calls = append(calls, telegram.snapshot()...)
			for _, call := range calls {
				if match(call) {
					return calls
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("expected call did not arrive: %#v", calls)
		return nil
	}

	press(987654321, "erp:copy:rtest001")
	if calls := telegram.snapshot(); len(calls) != 0 {
		t.Fatalf("only admins may press report buttons: %#v", calls)
	}

	press(10, "erp:copy:rtest001")
	calls := telegram.snapshot()
	if len(calls) != 1 || calls[0].chatID != "10" || !strings.HasPrefix(calls[0].text, "<pre>") {
		t.Fatalf("copy must go to the admin: %#v", calls)
	}
	for _, want := range []string{"report: rtest001 (real)", "version: abc1234 (2026-09-26)", "stage: download", "source: www.youtube.com", "format: mp3 320", "HTTP Error 403"} {
		if !strings.Contains(calls[0].text, want) {
			t.Errorf("developer copy lacks %q:\n%s", want, calls[0].text)
		}
	}
	if strings.Contains(calls[0].text, "987654321") || strings.Contains(calls[0].text, "tracking") {
		t.Fatalf("developer copy must not identify the user or keep tracking parameters:\n%s", calls[0].text)
	}

	press(10, "erp:fix:rtest001")
	calls = telegram.snapshot()
	if len(calls) != 1 || calls[0].method != "editMessageReplyMarkup" || !strings.Contains(calls[0].markup, "erp:fixok:rtest001") || !strings.Contains(calls[0].markup, "erp:back:rtest001") {
		t.Fatalf("fix must ask for confirmation: %#v", calls)
	}
	press(10, "erp:back:rtest001")
	if calls := telegram.snapshot(); len(calls) != 1 || !strings.Contains(calls[0].markup, "erp:retry:rtest001") {
		t.Fatalf("back must restore the report buttons: %#v", calls)
	}

	press(10, "erp:fixok:rtest001")
	calls = waitFor(func(call statusTestCall) bool { return call.method == "editMessageText" })
	if calls[0].chatID != "987654321" || calls[0].text != tr("report_fixed_notice", "en") {
		t.Fatalf("the user must get the fixed notice first: %#v", calls)
	}
	if calls[1].method != "editMessageReplyMarkup" || !strings.Contains(calls[1].markup, "erp:noop:rtest001") {
		t.Fatalf("the report must show it was fixed: %#v", calls)
	}
	if calls[2].chatID != "987654321" || calls[2].text != tr("analyzing", "en") {
		t.Fatalf("the request must be repeated for the user: %#v", calls)
	}
	if resolved, _ := state.errorReportByID(ctx, "rtest001"); resolved.ResolvedBy != 10 {
		t.Fatalf("report must be resolved by the admin: %#v", resolved)
	}
	audit, err := state.auditLog(ctx, 5)
	if err != nil || len(audit) == 0 || audit[0].Action != "report_fix_sent" || audit[0].TargetID != 987654321 || audit[0].Details != "rtest001" {
		t.Fatalf("audit=%#v err=%v", audit, err)
	}

	press(10, "erp:fixok:rtest001")
	calls = telegram.snapshot()
	if len(calls) != 2 || calls[0].chatID != "10" || !strings.Contains(calls[0].text, "already marked fixed") {
		t.Fatalf("a second fix must not notify the user again: %#v", calls)
	}

	banned := record
	banned.ID, banned.UserID, banned.ChatID = "rtest002", 555, 555
	if err := state.saveErrorReport(ctx, banned); err != nil {
		t.Fatal(err)
	}
	if _, err := state.banUser(ctx, 555, 10, "spam"); err != nil {
		t.Fatal(err)
	}
	press(10, "erp:fixok:rtest002")
	calls = telegram.snapshot()
	if len(calls) != 2 || !strings.Contains(calls[0].markup, "erp:retry:rtest002") || calls[1].chatID != "10" || !strings.Contains(calls[1].text, "banned") {
		t.Fatalf("a banned user must not be notified: %#v", calls)
	}
	if unresolved, _ := state.errorReportByID(ctx, "rtest002"); !unresolved.ResolvedAt.IsZero() {
		t.Fatal("the claim for a banned user must be released")
	}

	press(10, "erp:retry:rtest001")
	calls = waitFor(func(call statusTestCall) bool { return call.method == "editMessageText" })
	if calls[0].chatID != "10" || !strings.Contains(calls[0].text, "retrying the request") || calls[1].text != tr("analyzing", "en") {
		t.Fatalf("retry must run for the admin: %#v", calls)
	}

	press(10, "erp:copy:rmissing")
	if calls := telegram.snapshot(); len(calls) != 1 || !strings.Contains(calls[0].text, "not found") {
		t.Fatalf("an unknown report must be explained: %#v", calls)
	}
}

func TestErrorReportKeyboardOmitsUnrepeatableActions(t *testing.T) {
	markup := errorReportKeyboard("r1", errorReport{Stage: "search", UserID: 5, Query: "x"}, "en")
	if len(markup.InlineKeyboard) != 1 || *markup.InlineKeyboard[0][0].CallbackData != "erp:copy:r1" {
		t.Fatalf("search failures offer only the developer copy: %#v", markup.InlineKeyboard)
	}
	markup = errorReportKeyboard("r2", errorReport{Stage: "download", ChatID: -100, URL: "https://youtu.be/a"}, "en")
	if len(markup.InlineKeyboard[0]) != 1 || *markup.InlineKeyboard[0][0].CallbackData != "erp:retry:r2" {
		t.Fatalf("without a user there is nobody to notify: %#v", markup.InlineKeyboard)
	}
}

func TestRerunErrorReportRewritesStoredLink(t *testing.T) {
	directory := t.TempDir()
	probes := filepath.Join(directory, "probes.log")
	bin := filepath.Join(directory, "fake-yt-dlp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nfor last do :; done\nprintf '%s\\n' \"$last\" >> "+probes+"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	application := newExportTestApp(t, &exportTelegram{})
	application.downloader = &downloader{downloadDir: t.TempDir(), bin: bin, maxFileSize: maxFileSize, maxPlaylistTracks: maxPlaylistTracks}
	application.rerunErrorReport(10, 10, errorReportRecord{ID: "rsample", Stage: "preview", URL: "https://youtube.com/samples/dQw4w9WgXcQ"}, "en")
	deadline := time.Now().Add(3 * time.Second)
	for {
		logged, _ := os.ReadFile(probes)
		if len(logged) > 0 {
			if !strings.Contains(string(logged), "watch?v=dQw4w9WgXcQ") || strings.Contains(string(logged), "/samples/") {
				t.Fatalf("probed %q", logged)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the stored link was not probed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
