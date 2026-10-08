package main

import (
	"context"
	"html"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// cookieAlertCooldown is the minimum gap between two administrator alerts about
	// stale YouTube cookies. The last alert time is persisted in the metadata table.
	cookieAlertCooldown = 6 * time.Hour
	// cookieAlertMetadataKey stores the unix time of the last alert.
	cookieAlertMetadataKey = "cookie_alert_at"
	// cookieForbiddenWindow and cookieForbiddenThreshold describe the sliding window:
	// this many HTTP 403 failures from YouTube inside the window count as a cookie failure.
	cookieForbiddenWindow    = 10 * time.Minute
	cookieForbiddenThreshold = 3
	// cookieCheckCallback validates the configured cookies and updates their status.
	cookieCheckCallback = "cookiecheck"
	cookieCheckURL      = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	// cookieLoginTimeout bounds authentication, download-slot waiting and the full media check.
	cookieLoginTimeout = 2 * time.Minute
	// cookieLoginTrust is how long a successful full check suppresses further checks: YouTube keeps
	// sending the occasional 403 to working sessions, and each one should not cost a new request.
	cookieLoginTrust = 30 * time.Minute
)

// cookieLogin is the outcome of a login check with the configured cookies.
type cookieLogin int

const (
	// cookieLoginUnknown means the check could not tell: a network error, a timeout, or no yt-dlp.
	cookieLoginUnknown cookieLogin = iota
	cookieLoginValid
	cookieLoginInvalid
	// cookieLoginDegraded means authentication works, but the signed-in media probe was blocked.
	cookieLoginDegraded
)

func (l cookieLogin) String() string {
	switch l {
	case cookieLoginValid:
		return "ok"
	case cookieLoginInvalid:
		return "failed"
	case cookieLoginDegraded:
		return "degraded"
	}
	return "unknown"
}

// cookieAlertState keeps the in-memory part of the stale-cookie detector: the
// sliding window of recent YouTube 403 failures and a cached copy of the persisted
// last-alert time so ordinary failures never touch the database.
type cookieAlertState struct {
	mu                 sync.Mutex
	forbidden          []time.Time
	lastAlert          time.Time
	loaded             bool
	checking           bool
	trustedUntil       time.Time
	trustedFingerprint string
	checks             sync.WaitGroup
	now                func() time.Time
}

func (s *cookieAlertState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// recordForbidden adds a YouTube 403 failure to the sliding window and reports
// whether the window now holds enough failures to be treated as stale cookies.
func (s *cookieAlertState) recordForbidden() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	kept := s.forbidden[:0]
	for _, at := range s.forbidden {
		if now.Sub(at) < cookieForbiddenWindow {
			kept = append(kept, at)
		}
	}
	s.forbidden = append(kept, now)
	if len(s.forbidden) < cookieForbiddenThreshold {
		return false
	}
	s.forbidden = s.forbidden[:0]
	return true
}

// alertDue reports whether a new alert may be sent and, if so, records the alert
// time in memory and in the metadata table. The persisted value is read once per
// process so restarts keep honouring the cooldown.
func (s *cookieAlertState) alertDue(ctx context.Context, state *store) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if s.coolingDownLocked(ctx, state, now) {
		return false
	}
	s.lastAlert = now
	if state != nil {
		_ = state.setMetadata(ctx, cookieAlertMetadataKey, strconv.FormatInt(now.Unix(), 10))
	}
	return true
}

func (s *cookieAlertState) coolingDownLocked(ctx context.Context, state *store, now time.Time) bool {
	if !s.loaded {
		s.loaded = true
		if state != nil {
			s.lastAlert = state.cookieAlertTime(ctx)
		}
	}
	return !s.lastAlert.IsZero() && now.Sub(s.lastAlert) < cookieAlertCooldown
}

// startLoginCheck reports whether a login check should run now: no other check is running, no
// recent check found the cookies working, and the alert it may lead to is outside the cooldown.
// Every true result must be followed by finishLoginCheck and, once the alert is handled, checks.Done.
func (s *cookieAlertState) startLoginCheck(ctx context.Context, state *store, fingerprint string, force bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if fingerprint != s.trustedFingerprint {
		s.trustedUntil = time.Time{}
	}
	if s.checking || !force && (now.Before(s.trustedUntil) || s.coolingDownLocked(ctx, state, now)) {
		return false
	}
	s.checking = true
	s.checks.Add(1)
	return true
}

func (s *cookieAlertState) finishLoginCheck(result cookieLogin, fingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checking = false
	s.trustedUntil = time.Time{}
	s.trustedFingerprint = fingerprint
	if result == cookieLoginValid {
		s.trustedUntil = s.clock().Add(cookieLoginTrust)
	}
}

// cookieFailureMarkers are matched case-insensitively against a failure message.
// They cover the raw yt-dlp phrases ("Sign in to confirm you're not a bot",
// "The provided YouTube account cookies are no longer valid") as well as the
// humanized Russian text produced by humanizeError, so the detector works no
// matter which form of the message reaches it.
var cookieFailureMarkers = []string{
	"sign in to confirm",
	"not a bot",
	"cookies are no longer valid",
	"cookies.txt",
	"подтверждения, что запрос не от бота",
}

func isCookieFailure(message string) bool {
	message = strings.ToLower(message)
	for _, marker := range cookieFailureMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isForbiddenFailure(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "http error 403") || strings.Contains(message, "forbidden")
}

func isYouTubeSource(host string) bool {
	return strings.Contains(strings.ToLower(host), "youtu")
}

// reportDownloadFailure counts a failed download and suspects stale YouTube cookies when the
// failure is an explicit bot-check message or one of repeated HTTP 403 responses from a YouTube host.
func (a *app) reportDownloadFailure(message, source string) {
	if a.store != nil {
		a.store.increment(a.ctx, "downloads_failed")
	}
	cookie := isCookieFailure(message)
	if !cookie && isYouTubeSource(source) && isForbiddenFailure(message) {
		cookie = a.cookieAlerts.recordForbidden()
	}
	if cookie {
		a.suspectStaleCookies("admin_cookie_warning")
	}
}

// reportCookieRetry is wired into downloader.onCookieRetry: the download itself succeeded without
// cookies, but the cookie session may be degraded, so it feeds the same sliding window and alert path.
func (a *app) reportCookieRetry() {
	if a.store != nil {
		a.store.increment(a.ctx, "youtube_cookie_retries")
	}
	if a.cookieAlerts.recordForbidden() {
		a.suspectStaleCookies("admin_cookie_degraded")
	}
}

// suspectStaleCookies counts a suspected cookie failure and confirms it in the background with a
// login check before alerting administrators. YouTube now and then answers a signed-in session with
// a 403 or a bot check too, so only a failed login means the cookies need replacing; a working or
// inconclusive check is only logged.
func (a *app) suspectStaleCookies(alertKey string) {
	if a.store != nil {
		a.store.increment(a.ctx, "youtube_cookie_errors")
	}
	fingerprint := ""
	if a.downloader != nil {
		if snapshot, err := a.downloader.readCookieSnapshot(); err == nil {
			fingerprint = snapshot.fingerprint
		}
	}
	if a.cookieLoginCheck == nil || !a.cookieAlerts.startLoginCheck(a.ctx, a.store, fingerprint, false) {
		return
	}
	go func() {
		defer a.cookieAlerts.checks.Done()
		check := a.executeCookieCheck(a.ctx)
		result, detail := check.Login, check.Detail
		if a.store != nil {
			a.store.increment(a.ctx, "youtube_cookie_login_"+result.String())
		}
		if result != cookieLoginInvalid {
			log.Printf("Проверка входа YouTube по cookies: %s (%s), уведомление администраторам не отправлено", result, detail)
			return
		}
		log.Printf("Проверка входа YouTube по cookies не прошла: %s", detail)
		if !a.cookieAlerts.alertDue(a.ctx, a.store) {
			return
		}
		for _, adminID := range a.administratorIDs(a.ctx) {
			lang := a.langOrDefault(adminID)
			a.sendText(adminID, tr(alertKey, lang), "", cookieCheckKeyboard(lang))
		}
	}()
}

// cookieStatus exposes the same fresh, file-bound verdict as the status message. It does not
// make the service unhealthy: ordinary downloads can still work without authenticated cookies.
func (a *app) cookieStatus(ctx context.Context) string {
	switch a.cookieStatusComponent(ctx, a.cookieAlerts.clock(), defaultLang).Level {
	case statusOK:
		return "ok"
	case statusFail:
		return "failed"
	}
	return "unknown"
}

func cookieCheckKeyboard(lang string) *tgbotapi.InlineKeyboardMarkup {
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_cookie_check", lang), cookieCheckCallback)),
	)
	return &markup
}

// handleCookieCheck runs the same strict check as the monitor, without anonymous/cache fallback.
func (a *app) handleCookieCheck(callback *tgbotapi.CallbackQuery) {
	if !a.isAdmin(callback.From.ID) || a.downloader == nil || a.cookieLoginCheck == nil {
		return
	}
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	lang := a.langOrDefault(callback.From.ID)
	if !a.beginUserDownload(callback.From.ID) {
		a.sendText(chatID, tr("user_download_active", lang), "", nil)
		return
	}
	defer a.finishUserDownload(callback.From.ID)
	if !a.cookieAlerts.startLoginCheck(a.ctx, a.store, "", true) {
		a.sendText(chatID, tr("cookie_check_busy", lang), "", nil)
		return
	}
	defer a.cookieAlerts.checks.Done()
	if a.store != nil {
		a.store.audit(a.ctx, callback.From.ID, "cookie_check", callback.From.ID, "strict=true")
	}
	status := a.sendText(chatID, tr("cookie_check_running", lang), "", nil)
	a.executeCookieCheck(a.ctx)
	component := a.cookieStatusComponent(a.ctx, a.cookieAlerts.clock(), lang)
	text := component.Level.icon() + " <b>" + html.EscapeString(component.Label) + "</b>: " + component.Detail
	if status != nil {
		a.editStatusMessageFinal(status, text)
	} else {
		a.sendText(chatID, text, "HTML", nil)
	}
}
