package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestCookieForbiddenWindow(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		offsets []time.Duration
		want    []bool
	}{
		{name: "three inside window trigger", offsets: []time.Duration{0, time.Minute, 2 * time.Minute}, want: []bool{false, false, true}},
		{name: "two inside window do not trigger", offsets: []time.Duration{0, 9 * time.Minute}, want: []bool{false, false}},
		{name: "old failures expire", offsets: []time.Duration{0, time.Minute, 11 * time.Minute, 12 * time.Minute}, want: []bool{false, false, false, false}},
		{name: "window resets after trigger", offsets: []time.Duration{0, 1 * time.Minute, 2 * time.Minute, 3 * time.Minute, 4 * time.Minute, 5 * time.Minute}, want: []bool{false, false, true, false, false, true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var now time.Time
			state := cookieAlertState{now: func() time.Time { return now }}
			for i, offset := range test.offsets {
				now = base.Add(offset)
				if got := state.recordForbidden(); got != test.want[i] {
					t.Fatalf("failure %d at +%s: got %v want %v", i+1, offset, got, test.want[i])
				}
			}
		})
	}
}

func TestForbiddenFailureClassification(t *testing.T) {
	tests := []struct {
		message string
		host    string
		want    bool
	}{
		{"ERROR: unable to download video data: HTTP Error 403: Forbidden", "www.youtube.com", true},
		{"HTTP Error 403", "youtu.be", true},
		{"HTTP Error 403: Forbidden", "soundcloud.com", false},
		{"Видео приватное", "www.youtube.com", false},
	}
	for _, test := range tests {
		got := isYouTubeSource(test.host) && isForbiddenFailure(test.message)
		if got != test.want {
			t.Errorf("%q on %s: got %v want %v", test.message, test.host, got, test.want)
		}
	}
}

func TestCookieFailureMarkerMatch(t *testing.T) {
	rotated := "ERROR: [youtube] dQw4w9WgXcQ: The provided YouTube account cookies are no longer valid. They have likely been rotated in the browser as a security measure."
	tests := []struct {
		message string
		want    bool
	}{
		{rotated, true},
		{humanizeError(rotated), true},
		{"ERROR: [youtube] dQw4w9WgXcQ: Sign in to confirm you're not a bot. Use --cookies-from-browser or --cookies for the authentication.", true},
		{"ERROR: [youtube] dQw4w9WgXcQ: Sign in to confirm your age", true},
		{humanizeError("Sign in to confirm you're not a bot"), true},
		{"Sign in: cookies.txt is stale", true},
		{"HTTP Error 403: Forbidden", false},
		{"Видео приватное.", false},
		{"Видео недоступно (удалено или заблокировано).", false},
	}
	for _, test := range tests {
		if got := isCookieFailure(test.message); got != test.want {
			t.Errorf("isCookieFailure(%q)=%v want %v", test.message, got, test.want)
		}
	}
}

func TestCookieAlertFromRotatedCookies(t *testing.T) {
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	telegram := &cookieTestTelegram{}
	application := newCookieTestApp(t, telegram, state, dir)

	raw := "ERROR: [youtube] dQw4w9WgXcQ: The provided YouTube account cookies are no longer valid. They have likely been rotated in the browser as a security measure."
	application.reportDownloadFailure(raw, "www.youtube.com")
	got := telegram.snapshot()
	if len(got) != 1 || got[0].method != "sendMessage" || got[0].chatID != "10" || got[0].text != tr("admin_cookie_warning", "en") {
		t.Fatalf("rotated-cookie failure must alert the admin on the first hit: %#v", got)
	}
	stats, err := state.stats(context.Background())
	if err != nil || stats.CookieErrors != 1 || stats.DownloadsFailed != 1 {
		t.Fatalf("stats=%#v err=%v", stats, err)
	}
}

func TestStoreMetadataRoundTrip(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if got := state.metadata(ctx, "missing"); got != "" {
		t.Fatalf("missing metadata=%q", got)
	}
	if !state.cookieAlertTime(ctx).IsZero() {
		t.Fatal("cookie alert time must be zero before any alert")
	}
	if err := state.setMetadata(ctx, cookieAlertMetadataKey, "1700000000"); err != nil {
		t.Fatal(err)
	}
	if err := state.setMetadata(ctx, cookieAlertMetadataKey, "1700000001"); err != nil {
		t.Fatal(err)
	}
	if got := state.cookieAlertTime(ctx); got.Unix() != 1700000001 {
		t.Fatalf("cookie alert time=%v", got)
	}
}

type cookieTestTelegram struct {
	mu    sync.Mutex
	calls []struct{ method, chatID, text, markup string }
}

func (c *cookieTestTelegram) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseMultipartForm(1 << 20)
		_ = r.ParseForm()
		method := filepath.Base(r.URL.Path)
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
			return
		case "sendMessage", "editMessageText", "sendDocument":
			c.mu.Lock()
			c.calls = append(c.calls, struct{ method, chatID, text, markup string }{method, r.FormValue("chat_id"), r.FormValue("text"), r.FormValue("reply_markup")})
			c.mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"text":"x"}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
}

func (c *cookieTestTelegram) snapshot() []struct{ method, chatID, text, markup string } {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]struct{ method, chatID, text, markup string }(nil), c.calls...)
	c.calls = nil
	return out
}

func newCookieTestApp(t *testing.T, telegram *cookieTestTelegram, state *store, dir string) *app {
	t.Helper()
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegram.handler()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute, CacheTTL: time.Hour, AdminIDs: map[int64]bool{10: true}}
	application := newAppWithServices(context.Background(), bot, &downloader{bin: filepath.Join(dir, "missing-yt-dlp"), downloadDir: dir}, state, cfg)
	application.setLang(10, "en")
	application.setLang(11, "en")
	return application
}

func TestCookieAlertFromForbiddenWindowAndPersistedCooldown(t *testing.T) {
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	telegram := &cookieTestTelegram{}
	first := newCookieTestApp(t, telegram, state, dir)

	first.reportDownloadFailure("HTTP Error 403: Forbidden", "soundcloud.com")
	first.reportDownloadFailure("HTTP Error 403: Forbidden", "www.youtube.com")
	first.reportDownloadFailure("HTTP Error 403: Forbidden", "www.youtube.com")
	if got := telegram.snapshot(); len(got) != 0 {
		t.Fatalf("two YouTube 403s must not alert: %#v", got)
	}
	first.reportDownloadFailure("HTTP Error 403: Forbidden", "youtu.be")
	got := telegram.snapshot()
	if len(got) != 1 || got[0].method != "sendMessage" || got[0].chatID != "10" || got[0].text != tr("admin_cookie_warning", "en") {
		t.Fatalf("third YouTube 403 must alert the admin: %#v", got)
	}
	if !strings.Contains(got[0].markup, `"`+cookieCheckCallback+`"`) || !strings.Contains(got[0].markup, tr("btn_cookie_check", "en")) {
		t.Fatalf("alert must carry the check button: %s", got[0].markup)
	}
	if state.cookieAlertTime(context.Background()).IsZero() {
		t.Fatal("alert time was not persisted")
	}
	stats, err := state.stats(context.Background())
	if err != nil || stats.CookieErrors != 1 || stats.DownloadsFailed != 4 {
		t.Fatalf("stats=%#v err=%v", stats, err)
	}

	first.reportDownloadFailure("YouTube требует подтверждения, что запрос не от бота", "www.youtube.com")
	if got := telegram.snapshot(); len(got) != 0 {
		t.Fatalf("cooldown must suppress the second alert: %#v", got)
	}

	second := newCookieTestApp(t, telegram, state, dir)
	second.reportDownloadFailure("Sign in: cookies.txt is stale", "www.youtube.com")
	if got := telegram.snapshot(); len(got) != 0 {
		t.Fatalf("cooldown must survive a restart sharing the store: %#v", got)
	}

	if err := state.setMetadata(context.Background(), cookieAlertMetadataKey, fmt.Sprint(time.Now().Add(-cookieAlertCooldown-time.Minute).Unix())); err != nil {
		t.Fatal(err)
	}
	third := newCookieTestApp(t, telegram, state, dir)
	third.reportDownloadFailure("Sign in: cookies.txt is stale", "www.youtube.com")
	if got := telegram.snapshot(); len(got) != 1 {
		t.Fatalf("expired cooldown must allow a new alert: %#v", got)
	}
}

func TestHealthzReportsSuspectCookiesAfterAlert(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(bin, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute}
	application := newAppWithServices(context.Background(), nil, &downloader{bin: bin, downloadDir: dir}, state, cfg)
	handler := observabilityHandler(application)
	check := func(want string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"yt_dlp_cookies":"`+want+`"`) {
			t.Fatalf("want yt_dlp_cookies=%s code=%d body=%s", want, recorder.Code, recorder.Body.String())
		}
	}
	check("ok")
	if !application.cookieAlerts.alertDue(context.Background(), state) {
		t.Fatal("first alert must be due")
	}
	check("suspect")
	if err := state.setMetadata(context.Background(), cookieAlertMetadataKey, fmt.Sprint(time.Now().Add(-cookieAlertCooldown-time.Minute).Unix())); err != nil {
		t.Fatal(err)
	}
	check("ok")
}

func TestCookieCheckCallbackRequiresAdmin(t *testing.T) {
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	telegram := &cookieTestTelegram{}
	application := newCookieTestApp(t, telegram, state, dir)
	callback := func(userID int64) *tgbotapi.CallbackQuery {
		return &tgbotapi.CallbackQuery{ID: "cb", From: &tgbotapi.User{ID: userID}, Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: userID, Type: "private"}}, Data: cookieCheckCallback}
	}

	application.handleCallback(callback(11))
	if got := telegram.snapshot(); len(got) != 0 {
		t.Fatalf("non-admin cookiecheck must be ignored: %#v", got)
	}

	application.handleCallback(callback(10))
	got := telegram.snapshot()
	if len(got) == 0 || got[0].method != "sendMessage" || got[0].chatID != "10" || !strings.Contains(got[0].text, "download trace") {
		t.Fatalf("admin cookiecheck must start a download trace: %#v", got)
	}
	if got[len(got)-1].method != "sendDocument" {
		t.Fatalf("trace must be delivered as a document: %#v", got)
	}
	audit, err := state.auditLog(context.Background(), 5)
	if err != nil || len(audit) != 1 || audit[0].Action != "download_log" || !strings.Contains(audit[0].Details, "fresh=true") {
		t.Fatalf("audit=%#v err=%v", audit, err)
	}
}
