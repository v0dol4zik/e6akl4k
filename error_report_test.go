package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestErrorReportFingerprintFoldsTrackIDs(t *testing.T) {
	tests := []struct {
		a, b string
		same bool
	}{
		{"ERROR: [youtube] dQw4w9WgXcQ: Video unavailable", "ERROR: [youtube] abcdefghijk: Video unavailable", true},
		{"ERROR: [youtube] dQw4w9WgXcQ: Video unavailable", "ERROR: [youtube] dQw4w9WgXcQ: Private video", false},
		{"HTTP Error 403: Forbidden", "http error 403: forbidden", true},
		{"[download] a b: c", "[download] d e: c", false},
	}
	for _, test := range tests {
		got := errorReportFingerprint(test.a) == errorReportFingerprint(test.b)
		if got != test.same {
			t.Errorf("fingerprint(%q)==fingerprint(%q) is %v, want %v", test.a, test.b, got, test.same)
		}
	}
}

func TestErrorReporterFoldsRepeatsInsideWindow(t *testing.T) {
	var now time.Time
	reporter := newErrorReporter(-1001)
	reporter.now = func() time.Time { return now }
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	report := errorReport{Stage: "download", Error: "ERROR: [youtube] a1: Video unavailable"}
	steps := []struct {
		offset  time.Duration
		error   string
		stage   string
		ok      bool
		repeats int
	}{
		{0, report.Error, "download", true, 0},
		{time.Minute, "ERROR: [youtube] b2: Video unavailable", "download", false, 0},
		{2 * time.Minute, report.Error, "download", false, 0},
		{3 * time.Minute, report.Error, "preview", true, 0},
		{errorReportDedupWindow + time.Second, report.Error, "download", true, 2},
		{errorReportDedupWindow + 2*time.Second, report.Error, "download", false, 0},
	}
	for i, step := range steps {
		now = base.Add(step.offset)
		repeats, ok := reporter.admit(errorReport{Stage: step.stage, Error: step.error})
		if ok != step.ok || repeats != step.repeats {
			t.Fatalf("step %d: ok=%v repeats=%d, want ok=%v repeats=%d", i, ok, repeats, step.ok, step.repeats)
		}
	}
}

func TestFormatErrorReportRedactsAndEscapes(t *testing.T) {
	entry := errorReportEntry{
		report: errorReport{
			Stage:  "download",
			UserID: 42,
			URL:    "https://www.youtube.com/watch?v=dQw4w9WgXcQ&token=secret&list=PL1",
			Query:  "<b>daft</b> punk",
			Format: "mp3 320",
			Error:  "failed <script> https://rr1.googlevideo.com/videoplayback?id=1&k=signed-secret",
		},
		repeats: 3,
	}
	text := formatErrorReport(entry, "listener_1", "ru")
	for _, want := range []string{
		"<code>download</code>",
		"<code>42</code> @listener_1",
		"v=dQw4w9WgXcQ",
		"list=PL1",
		"&lt;b&gt;daft&lt;/b&gt; punk",
		"mp3 320",
		"failed &lt;script&gt;",
		"[redacted]",
		"3",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report does not contain %q:\n%s", want, text)
		}
	}
	for _, leaked := range []string{"token=secret", "signed-secret", "<script>"} {
		if strings.Contains(text, leaked) {
			t.Errorf("report leaks %q:\n%s", leaked, text)
		}
	}
	chatOnly := formatErrorReport(errorReportEntry{report: errorReport{Stage: "send", ChatID: -100, Error: "x"}}, "", "en")
	if !strings.Contains(chatOnly, "chat: <code>-100</code>") || strings.Contains(chatOnly, "user:") {
		t.Fatalf("chat-only report=%s", chatOnly)
	}
}

func TestReportErrorPostsToCacheChannel(t *testing.T) {
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	telegram := &cookieTestTelegram{}
	application := newCookieTestApp(t, telegram, state, dir)
	reporter := newErrorReporter(-1001)
	reporter.interval = 0
	application.errorReports = reporter
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go application.runErrorReporter(ctx, reporter)

	application.reportError(errorReport{Stage: "download", UserID: 11, URL: "https://youtu.be/abc", Error: "HTTP Error 403: Forbidden"})
	application.reportError(errorReport{Stage: "download", UserID: 11, URL: "https://youtu.be/abc", Error: "HTTP Error 403: Forbidden"})
	application.reportError(errorReport{Stage: "download", UserID: 11, Error: context.Canceled.Error()})
	application.reportError(errorReport{Stage: "search", UserID: 11, Query: "daft punk", Error: "yt-dlp exited"})
	application.reportError(errorReport{Stage: "search", UserID: 11, Error: "  "})

	var calls []struct{ method, chatID, text, markup string }
	deadline := time.Now().Add(2 * time.Second)
	for len(calls) < 2 && time.Now().Before(deadline) {
		calls = append(calls, telegram.snapshot()...)
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	calls = append(calls, telegram.snapshot()...)
	if len(calls) != 2 {
		t.Fatalf("want 2 posts (duplicate, cancellation and empty error skipped), got %#v", calls)
	}
	if calls[0].chatID != "-1001" || !strings.Contains(calls[0].text, "download") || !strings.Contains(calls[0].text, "403") {
		t.Fatalf("first post=%#v", calls[0])
	}
	if calls[1].chatID != "-1001" || !strings.Contains(calls[1].text, "daft punk") {
		t.Fatalf("second post=%#v", calls[1])
	}
	counters, err := state.counters(context.Background())
	if err != nil || counters["error_reports_sent"] != 2 {
		t.Fatalf("counters=%v err=%v", counters, err)
	}
}

func TestReportErrorDropsWhenQueueIsFull(t *testing.T) {
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	application := newCookieTestApp(t, &cookieTestTelegram{}, state, dir)
	application.errorReports = newErrorReporter(-1001)
	for i := 0; i <= errorReportQueueSize; i++ {
		application.reportError(errorReport{Stage: "download", Error: fmt.Sprintf("distinct failure %d", i)})
	}
	counters, err := state.counters(context.Background())
	if err != nil || counters["error_reports_dropped"] != 1 {
		t.Fatalf("counters=%v err=%v", counters, err)
	}
}

func TestStartErrorReporterPicksChat(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name         string
		cache, error int64
		want         int64
	}{
		{name: "disabled without chats", want: 0},
		{name: "cache channel by default", cache: -1001, want: -1001},
		{name: "error chat overrides", cache: -1001, error: -1002, want: -1002},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application := newCookieTestApp(t, &cookieTestTelegram{}, nil, dir)
			application.cfg.CacheChatID, application.cfg.ErrorChatID = test.cache, test.error
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			application.startErrorReporter(ctx)
			got := int64(0)
			if application.errorReports != nil {
				got = application.errorReports.chatID
			}
			if got != test.want {
				t.Fatalf("chat=%d want %d", got, test.want)
			}
		})
	}
}

func TestDeliveryFailuresCapLines(t *testing.T) {
	failures := deliveryFailures{total: 9}
	for i := 1; i <= 7; i++ {
		failures.add(i, "boom")
	}
	if failures.count != 7 || len(failures.lines) != maxDeliveryFailureLines || failures.lines[0] != "[1/9] boom" {
		t.Fatalf("failures=%#v", failures)
	}
}
