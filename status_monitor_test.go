package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type statusTestCall struct {
	method, chatID, messageID, text, markup string
	silent                                  bool
}

// statusTestTelegram records every request and can fail getMe, edits and pins on demand.
type statusTestTelegram struct {
	mu       sync.Mutex
	calls    []statusTestCall
	sent     int
	getMeBad bool
	sendBad  bool
	editGone bool
	pinBad   bool
}

func (c *statusTestTelegram) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseMultipartForm(1 << 20)
		_ = r.ParseForm()
		method := filepath.Base(r.URL.Path)
		c.mu.Lock()
		defer c.mu.Unlock()
		if method == "getMe" {
			if c.getMeBad {
				fmt.Fprint(w, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
			return
		}
		c.calls = append(c.calls, statusTestCall{method, r.FormValue("chat_id"), r.FormValue("message_id"), r.FormValue("text"), r.FormValue("reply_markup"), r.FormValue("disable_notification") == "true"})
		switch method {
		case "sendMessage":
			if c.sendBad {
				fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
				return
			}
			c.sent++
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1,"chat":{"id":-1001,"type":"channel"},"text":"x"}}`, 100+c.sent)
		case "editMessageText":
			if c.editGone {
				fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":-1001,"type":"channel"},"text":"x"}}`)
		case "pinChatMessage":
			if c.pinBad {
				fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: not enough rights to manage pinned messages in the chat"}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
}

func (c *statusTestTelegram) set(apply func(*statusTestTelegram)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	apply(c)
}

func (c *statusTestTelegram) snapshot() []statusTestCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]statusTestCall(nil), c.calls...)
	c.calls = nil
	return out
}

// newStatusTestApp is an app whose every component is healthy: working cookies, a known and
// current yt-dlp and plenty of disk.
func newStatusTestApp(t *testing.T, telegram *statusTestTelegram) (*app, *store, *statusMonitor, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegram.handler()})
	if err != nil {
		t.Fatal(err)
	}
	cookies := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(cookies, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config{DownloadWorkers: 2, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute,
		CacheTTL: time.Hour, AdminIDs: map[int64]bool{10: true}, StatusMessage: true, DiskWarningBytes: 1}
	application := newAppWithServices(context.Background(), bot, &downloader{bin: filepath.Join(dir, "missing-yt-dlp"), downloadDir: dir, cookiesFile: cookies}, state, cfg)
	application.setLang(10, "en")
	application.errorReports = newErrorReporter(-1001)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	application.cookieAlerts.now = func() time.Time { return now }
	if err := os.Chtimes(cookies, now, now); err != nil {
		t.Fatal(err)
	}
	application.recordCookieCheck(context.Background(), cookieTestResult(t, application, cookieLoginValid, "full audio confirmed"))
	monitor := newStatusMonitor(-1001)
	monitor.now = func() time.Time { return now }
	monitor.installedYtdlp = func() string { return "2026.08.19" }
	monitor.latestRelease = func(context.Context) (string, error) { return "2026.08.19", nil }
	monitor.dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("dial is not expected")
	}
	return application, state, monitor, &now
}

func TestStatusMonitorPostsPinsAndEditsOnChange(t *testing.T) {
	telegram := &statusTestTelegram{}
	application, state, monitor, now := newStatusTestApp(t, telegram)
	ctx := context.Background()

	application.statusTick(ctx, monitor)
	calls := telegram.snapshot()
	if len(calls) != 2 || calls[0].method != "sendMessage" || calls[0].chatID != "-1001" || !calls[0].silent {
		t.Fatalf("first tick must post one silent status message, got %#v", calls)
	}
	if !strings.HasPrefix(calls[0].text, "🟢") || !strings.Contains(calls[0].text, "yt-dlp: 2026.08.19") || !strings.Contains(calls[0].text, "вход и тестовое скачивание работают") {
		t.Fatalf("status text=%q", calls[0].text)
	}
	if calls[1].method != "pinChatMessage" || calls[1].messageID != "101" || !calls[1].silent {
		t.Fatalf("status message must be pinned silently: %#v", calls[1])
	}
	if got := state.metadata(ctx, statusMessageKey); got != "-1001:101" {
		t.Fatalf("stored status message=%q", got)
	}

	*now = now.Add(time.Minute)
	application.statusTick(ctx, monitor)
	if calls := telegram.snapshot(); len(calls) != 0 {
		t.Fatalf("an unchanged status must not be edited before the refresh interval: %#v", calls)
	}

	telegram.set(func(c *statusTestTelegram) { c.getMeBad = true })
	*now = now.Add(time.Minute)
	application.statusTick(ctx, monitor)
	calls = telegram.snapshot()
	if len(calls) != 2 || calls[0].method != "sendMessage" || calls[0].silent || !strings.HasPrefix(calls[0].text, "🔴") || !strings.Contains(calls[0].text, "Telegram API") || !strings.Contains(calls[0].text, "502") {
		t.Fatalf("a failure must be announced loudly: %#v", calls)
	}
	if calls[1].method != "editMessageText" || calls[1].messageID != "101" || !strings.HasPrefix(calls[1].text, "🔴") {
		t.Fatalf("the pinned message must turn red: %#v", calls[1])
	}

	*now = now.Add(time.Minute)
	application.statusTick(ctx, monitor)
	if calls := telegram.snapshot(); len(calls) > 1 || len(calls) == 1 && calls[0].method != "editMessageText" {
		t.Fatalf("a failure that was already announced must not repeat: %#v", calls)
	}

	telegram.set(func(c *statusTestTelegram) { c.getMeBad = false })
	*now = now.Add(4 * time.Minute)
	application.statusTick(ctx, monitor)
	calls = telegram.snapshot()
	if len(calls) != 2 || !calls[0].silent || !strings.HasPrefix(calls[0].text, "🟢") || !strings.Contains(calls[0].text, "5 мин") {
		t.Fatalf("recovery must be announced silently with its duration: %#v", calls)
	}
	if calls[1].method != "editMessageText" || !strings.HasPrefix(calls[1].text, "🟢") {
		t.Fatalf("the pinned message must turn green again: %#v", calls[1])
	}

	*now = now.Add(statusRefreshInterval)
	application.statusTick(ctx, monitor)
	calls = telegram.snapshot()
	if len(calls) != 1 || calls[0].method != "editMessageText" {
		t.Fatalf("the status must be refreshed after the interval: %#v", calls)
	}
}

func TestStatusMonitorReplacesDeletedMessageAndWarnsAboutPinOnce(t *testing.T) {
	telegram := &statusTestTelegram{pinBad: true}
	application, state, monitor, now := newStatusTestApp(t, telegram)
	ctx := context.Background()

	application.statusTick(ctx, monitor)
	calls := telegram.snapshot()
	if len(calls) != 3 || calls[1].method != "pinChatMessage" || calls[2].method != "sendMessage" || calls[2].chatID != "10" || !strings.Contains(calls[2].text, "could not pin") {
		t.Fatalf("a failed pin must be reported to the admin: %#v", calls)
	}

	telegram.set(func(c *statusTestTelegram) { c.editGone = true })
	*now = now.Add(statusRefreshInterval)
	application.statusTick(ctx, monitor)
	calls = telegram.snapshot()
	if len(calls) != 3 || calls[0].method != "editMessageText" || calls[1].method != "sendMessage" || calls[1].chatID != "-1001" || calls[2].method != "pinChatMessage" || calls[2].messageID != "103" {
		t.Fatalf("a deleted status message must be posted and pinned again without a second admin warning: %#v", calls)
	}
	if got := state.metadata(ctx, statusMessageKey); got != "-1001:103" {
		t.Fatalf("stored status message=%q", got)
	}
}

func TestStatusMonitorKeepsStateAcrossRestarts(t *testing.T) {
	telegram := &statusTestTelegram{}
	application, _, monitor, now := newStatusTestApp(t, telegram)
	ctx := context.Background()
	application.statusTick(ctx, monitor)
	telegram.set(func(c *statusTestTelegram) { c.getMeBad = true })
	*now = now.Add(time.Minute)
	application.statusTick(ctx, monitor)
	telegram.snapshot()

	restarted := newStatusMonitor(-1001)
	restarted.now, restarted.installedYtdlp, restarted.latestRelease = monitor.now, monitor.installedYtdlp, monitor.latestRelease
	*now = now.Add(time.Minute)
	application.statusTick(ctx, restarted)
	calls := telegram.snapshot()
	if len(calls) != 1 || calls[0].method != "editMessageText" || calls[0].messageID != "101" {
		t.Fatalf("after a restart the known failure must not be announced again and the old message must be edited: %#v", calls)
	}
}

func TestStatusMonitorRetriesUndeliveredFailureAlert(t *testing.T) {
	telegram := &statusTestTelegram{}
	application, _, monitor, now := newStatusTestApp(t, telegram)
	ctx := context.Background()
	components := []statusComponent{{Key: "disk", Label: "disk", Level: statusFail, Detail: "full"}}
	telegram.set(func(c *statusTestTelegram) { c.sendBad = true })
	application.announceStatusChanges(ctx, monitor, components, *now)
	if state := monitor.states["disk"]; state.Level != statusFail || state.Alerted {
		t.Fatalf("an undelivered alert must stay pending: %#v", state)
	}
	telegram.snapshot()
	telegram.set(func(c *statusTestTelegram) { c.sendBad = false })
	*now = now.Add(time.Minute)
	application.announceStatusChanges(ctx, monitor, components, *now)
	calls := telegram.snapshot()
	if len(calls) != 1 || calls[0].silent || !strings.Contains(calls[0].text, "full") || !monitor.states["disk"].Alerted {
		t.Fatalf("the pending alert must be retried: %#v", calls)
	}
	components[0].Level = statusOK
	monitor.states["disk"] = statusComponentState{Level: statusFail, Since: now.Add(-3 * time.Hour).Unix()}
	application.announceStatusChanges(ctx, monitor, components, *now)
	calls = telegram.snapshot()
	if len(calls) != 1 || calls[0].silent || !strings.Contains(calls[0].text, "3 ч 0 мин") || !strings.Contains(calls[0].text, "сообщить не получилось") {
		t.Fatalf("a recovery from an unannounced failure must be loud: %#v", calls)
	}
}

func TestCookieStatusComponent(t *testing.T) {
	application, state, _, now := newStatusTestApp(t, &statusTestTelegram{})
	ctx := context.Background()
	if component := application.cookieStatusComponent(ctx, now.Add(time.Hour), "en"); component.Level != statusOK || !strings.Contains(component.Detail, "login and test download work") {
		t.Fatalf("working cookies: %#v", component)
	}
	application.recordCookieCheck(ctx, cookieTestResult(t, application, cookieLoginInvalid, "ERROR: login required <x>"))
	component := application.cookieStatusComponent(ctx, time.Now(), "en")
	if component.Level != statusFail || !strings.Contains(component.Detail, "refresh cookies.txt") || !strings.Contains(component.Detail, "&lt;x&gt;") {
		t.Fatalf("dead cookies: %#v", component)
	}
	application.recordCookieCheck(ctx, cookieTestResult(t, application, cookieLoginUnknown, "timeout"))
	if component := application.cookieStatusComponent(ctx, time.Now(), "en"); component.Level != statusWarn {
		t.Fatalf("inconclusive check: %#v", component)
	}
	_ = state.setMetadata(ctx, cookieCheckStateKey, "")
	if component := application.cookieStatusComponent(ctx, time.Now(), "en"); component.Level != statusWarn || !strings.Contains(component.Detail, "not checked yet") {
		t.Fatalf("no check yet: %#v", component)
	}
	application.downloader.cookiesFile = ""
	if component := application.cookieStatusComponent(ctx, time.Now(), "en"); component.Level != statusWarn {
		t.Fatalf("no cookies file: %#v", component)
	}
}

func TestRunCookieChecksRecordsScheduledCheck(t *testing.T) {
	application, state, _, _ := newStatusTestApp(t, &statusTestTelegram{})
	ctx, cancel := context.WithCancel(context.Background())
	_ = state.setMetadata(ctx, cookieCheckStateKey, "")
	application.cfg.CookieCheckInterval = time.Hour
	checked := make(chan struct{}, 1)
	application.cookieLoginCheck = func(context.Context) cookieCheckResult {
		checked <- struct{}{}
		return cookieTestResult(t, application, cookieLoginInvalid, "login required")
	}
	done := make(chan struct{})
	go func() {
		application.runCookieChecks(ctx)
		close(done)
	}()
	select {
	case <-checked:
	case <-time.After(2 * time.Second):
		t.Fatal("the overdue check did not run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for application.lastCookieCheck(ctx).Detail != "login required" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if got := application.lastCookieCheck(context.Background()).Login; got != cookieLoginInvalid {
		t.Fatalf("recorded result=%s", got)
	}
}

func TestRelayStatusHidesAddress(t *testing.T) {
	application, _, monitor, _ := newStatusTestApp(t, &statusTestTelegram{})
	proxy, _ := url.Parse("socks5h://user:secret@relay.internal")
	application.cfg.YandexProxy = proxy
	var dialed string
	monitor.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}
	component := application.relayStatus(context.Background(), monitor, "en")
	if dialed != "relay.internal:1080" {
		t.Fatalf("dialed %q", dialed)
	}
	if component.Level != statusFail || !strings.Contains(component.Detail, "connection refused") || strings.Contains(component.Detail, "relay.internal") || strings.Contains(component.Detail, "secret") {
		t.Fatalf("relay component=%#v", component)
	}
	client, server := net.Pipe()
	defer server.Close()
	monitor.dial = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	if component := application.relayStatus(context.Background(), monitor, "en"); component.Level != statusOK {
		t.Fatalf("reachable relay=%#v", component)
	}
}

func TestPOTStatusDialsProvider(t *testing.T) {
	application, _, monitor, _ := newStatusTestApp(t, &statusTestTelegram{})
	if components := application.statusComponents(context.Background(), monitor, time.Now()); hasStatusComponent(components, "pot") {
		t.Fatal("the PO token line must stay hidden without a provider")
	}
	application.cfg.YTDLPPOTProviderURL = "http://bgutil-pot:4416"
	var dialed string
	monitor.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}
	component := application.potStatus(context.Background(), monitor, "en")
	if dialed != "bgutil-pot:4416" || component.Level != statusFail || component.Label != "YouTube PO tokens" || !strings.Contains(component.Detail, "connection refused") {
		t.Fatalf("dialed %q, component=%#v", dialed, component)
	}
	client, server := net.Pipe()
	defer server.Close()
	monitor.dial = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	if component := application.potStatus(context.Background(), monitor, "en"); component.Level != statusOK {
		t.Fatalf("reachable provider=%#v", component)
	}
}

func hasStatusComponent(components []statusComponent, key string) bool {
	for _, component := range components {
		if component.Key == key {
			return true
		}
	}
	return false
}

func TestProxyDialAddressDefaults(t *testing.T) {
	for raw, want := range map[string]string{
		"socks5://relay":      "relay:1080",
		"http://relay":        "relay:80",
		"https://relay":       "relay:443",
		"socks5h://relay:900": "relay:900",
		"http://[::1]":        "[::1]:80",
	} {
		proxy, _ := url.Parse(raw)
		if got := proxyDialAddress(proxy); got != want {
			t.Errorf("%s: got %q, want %q", raw, got, want)
		}
	}
}

func TestReportsStatusWarnsAfterDroppedReports(t *testing.T) {
	application, state, monitor, now := newStatusTestApp(t, &statusTestTelegram{})
	ctx := context.Background()
	state.increment(ctx, "error_reports_dropped")
	if component := application.reportsStatus(ctx, monitor, *now, "en"); component.Level != statusOK {
		t.Fatalf("drops before the monitor started are the baseline: %#v", component)
	}
	state.increment(ctx, "error_reports_dropped")
	state.increment(ctx, "error_reports_dropped")
	component := application.reportsStatus(ctx, monitor, now.Add(time.Minute), "en")
	if component.Level != statusWarn || !strings.Contains(component.Detail, "2 reports dropped") {
		t.Fatalf("new drops must warn: %#v", component)
	}
	if component := application.reportsStatus(ctx, monitor, now.Add(time.Minute+statusDroppedWarning), "en"); component.Level != statusOK {
		t.Fatalf("the warning must expire: %#v", component)
	}
}

func TestYtdlpStatusAndReleaseCache(t *testing.T) {
	application, state, monitor, now := newStatusTestApp(t, &statusTestTelegram{})
	ctx := context.Background()
	calls := 0
	monitor.latestRelease = func(context.Context) (string, error) {
		calls++
		return "2026.09.20", nil
	}
	component := application.ytdlpStatus(ctx, monitor, *now, "en")
	if component.Level != statusWarn || !strings.Contains(component.Detail, "2026.09.20 is out") {
		t.Fatalf("a newer release must warn: %#v", component)
	}
	application.ytdlpStatus(ctx, monitor, now.Add(time.Hour), "en")
	if calls != 1 || state.metadata(ctx, ytdlpLatestKey) != "2026.09.20" {
		t.Fatalf("the release must be cached: calls=%d", calls)
	}
	monitor.latestRelease = func(context.Context) (string, error) {
		calls++
		return "", errors.New("offline")
	}
	if component := application.ytdlpStatus(ctx, monitor, now.Add(ytdlpReleaseCheckInterval+time.Minute), "en"); calls != 2 || component.Level != statusWarn {
		t.Fatalf("a failed refresh keeps the cached release: calls=%d %#v", calls, component)
	}
	application.ytdlpStatus(ctx, monitor, now.Add(ytdlpReleaseCheckInterval+2*time.Minute), "en")
	if calls != 2 {
		t.Fatalf("a failed refresh must not be retried every tick: calls=%d", calls)
	}
	monitor.installedYtdlp = func() string { return "" }
	if component := application.ytdlpStatus(ctx, monitor, now.Add(ytdlpReleaseCheckInterval+3*time.Minute), "en"); component.Level != statusWarn || component.Detail != tr("status_unknown", "en") {
		t.Fatalf("an unknown installed version: %#v", component)
	}
}

func TestYtdlpVersionNewer(t *testing.T) {
	for _, test := range []struct {
		latest, current string
		want            bool
	}{
		{"2026.09.20", "2026.08.19", true},
		{"2026.08.19", "2026.08.19", false},
		{"2026.08.19.1", "2026.08.19", true},
		{"2026.08.19", "2026.08.19.232812", false},
		{"2025.12.31", "2026.01.01", false},
		{"nightly", "2026.01.01", false},
	} {
		if got := ytdlpVersionNewer(test.latest, test.current); got != test.want {
			t.Errorf("%s > %s: got %v", test.latest, test.current, got)
		}
	}
}

func TestFetchLatestYtdlpReleaseValidatesTag(t *testing.T) {
	if !ytdlpVersionPattern.MatchString("2026.09.20") || !ytdlpVersionPattern.MatchString("2026.09.20.1") || ytdlpVersionPattern.MatchString("latest") {
		t.Fatal("unexpected version pattern")
	}
}

func TestStatusDurationAndTime(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second:            "1 min",
		42 * time.Minute:            "42 min",
		3*time.Hour + 5*time.Minute: "3 h 5 min",
		72 * time.Hour:              "3 d",
	} {
		if got := statusDuration(d, "en"); got != want {
			t.Errorf("%v: got %q, want %q", d, got, want)
		}
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if got := statusTime(now.Add(-time.Hour), now); got != "11:00" {
		t.Fatalf("today=%q", got)
	}
	if got := statusTime(now.Add(-24*time.Hour), now); got != "25.09 12:00" {
		t.Fatalf("yesterday=%q", got)
	}
}

func TestNetworkReason(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "no such host", Name: "relay"}, "name lookup failed"},
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "connection refused"},
		{context.DeadlineExceeded, "timed out"},
		{&tgbotapi.Error{Code: 502, Message: "Bad Gateway"}, "answered with code 502"},
		{errors.New("boom"), "network error"},
	} {
		if got := networkReason(test.err, "en"); got != test.want {
			t.Errorf("%v: got %q, want %q", test.err, got, test.want)
		}
	}
}

func TestRenderStatusShowsErrorsAndSpike(t *testing.T) {
	application, state, monitor, now := newStatusTestApp(t, &statusTestTelegram{})
	ctx := context.Background()
	record := errorReportRecord{ID: "rabc1234", Stage: "download", Class: errorClassReal, Fingerprint: "x", UserID: 11, Error: "boom", CreatedAt: now.Add(-time.Hour)}
	if err := state.saveErrorReport(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.ID, record.Class = "rdef5678", errorClassExpected
	if err := state.saveErrorReport(ctx, record); err != nil {
		t.Fatal(err)
	}
	application.errorReports.now = func() time.Time { return now.Add(-10 * time.Minute) }
	for user := int64(1); user <= errorSpikeUsers; user++ {
		application.errorReports.spike("x", user, "download", "rspike01")
	}
	components := []statusComponent{{Key: "disk", Label: "disk", Detail: "fine"}}
	text, stable := application.renderStatus(ctx, monitor, components, *now, "en")
	for _, want := range []string{"🟡", "1 real, 1 expected", "last: 11:00 UTC", "rabc1234", "spike: 11:50 UTC", "rspike01", "downloads: 0 of 2", "updated: 26.09 12:00 UTC"} {
		if !strings.Contains(text, want) {
			t.Errorf("status text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(stable, "updated") || strings.Contains(stable, "downloads:") {
		t.Fatalf("the stable part must not include volatile lines:\n%s", stable)
	}
}
