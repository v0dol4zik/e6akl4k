package main

import (
	"context"
	"errors"
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
	// cookieCheckCallback runs a fresh download trace against cookieCheckURL.
	cookieCheckCallback = "cookiecheck"
	cookieCheckURL      = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	// cookieLoginTimeout bounds the login check that has to confirm a suspected cookie failure
	// before administrators are alerted.
	cookieLoginTimeout = time.Minute
	// cookieLoginTrust is how long a successful login check suppresses further checks: YouTube keeps
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
)

func (l cookieLogin) String() string {
	switch l {
	case cookieLoginValid:
		return "ok"
	case cookieLoginInvalid:
		return "failed"
	}
	return "unknown"
}

// cookieAlertState keeps the in-memory part of the stale-cookie detector: the
// sliding window of recent YouTube 403 failures and a cached copy of the persisted
// last-alert time so ordinary failures never touch the database.
type cookieAlertState struct {
	mu           sync.Mutex
	forbidden    []time.Time
	lastAlert    time.Time
	loaded       bool
	checking     bool
	trustedUntil time.Time
	checks       sync.WaitGroup
	now          func() time.Time
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
func (s *cookieAlertState) startLoginCheck(ctx context.Context, state *store) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if s.checking || now.Before(s.trustedUntil) || s.coolingDownLocked(ctx, state, now) {
		return false
	}
	s.checking = true
	s.checks.Add(1)
	return true
}

func (s *cookieAlertState) finishLoginCheck(result cookieLogin) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checking = false
	if result == cookieLoginValid {
		s.trustedUntil = s.clock().Add(cookieLoginTrust)
	}
}

// noteLoginResult lets a scheduled login check suppress checks the way a suspected failure's does.
func (s *cookieAlertState) noteLoginResult(result cookieLogin) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if a.cookieLoginCheck == nil || !a.cookieAlerts.startLoginCheck(a.ctx, a.store) {
		return
	}
	go func() {
		defer a.cookieAlerts.checks.Done()
		result, detail := a.cookieLoginCheck(a.ctx)
		a.cookieAlerts.finishLoginCheck(result)
		if a.store != nil {
			a.store.increment(a.ctx, "youtube_cookie_login_"+result.String())
			a.recordCookieLogin(a.ctx, result, detail)
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

// checkCookieLogin opens the account's Watch Later playlist with an isolated copy of the cookies.
// YouTube serves that playlist only to a signed-in session, so it tells dead cookies apart from the
// occasional 403 that YouTube sends to working ones.
func (d *downloader) checkCookieLogin(ctx context.Context) (cookieLogin, string) {
	cookies, cleanup, err := d.isolatedCookieFile()
	if err != nil {
		return cookieLoginUnknown, err.Error()
	}
	defer cleanup()
	if cookies == "" {
		return cookieLoginInvalid, "no cookies file is configured"
	}
	ctx, cancel := context.WithTimeout(ctx, cookieLoginTimeout)
	defer cancel()
	_, stderr, err := d.runOnce(ctx, []string{
		"--ignore-config", "--color", "never", "--no-progress",
		"--flat-playlist", "--playlist-end", "1", "--print", "id",
		"--cookies", cookies, "--", ":ytwatchlater",
	})
	return classifyCookieLogin(stderr, err)
}

// cookieLoginMarkers are yt-dlp messages that mean YouTube did not treat the request as signed in.
// For an anonymous visitor Watch Later "does not exist".
var cookieLoginMarkers = []string{
	"playlist does not exist",
	"sign in",
	"login required",
	"log in",
	"authentication",
}

// classifyCookieLogin turns the result of the Watch Later request into a login verdict. Anything
// that does not name the login, such as a network error or a timeout, stays unknown and never alerts.
func classifyCookieLogin(stderr string, err error) (cookieLogin, string) {
	low := strings.ToLower(stderr)
	if strings.Contains(low, "cookies are no longer valid") {
		return cookieLoginInvalid, "cookies are no longer valid"
	}
	if err == nil {
		return cookieLoginValid, "watch later opened"
	}
	detail := err.Error()
	if line := lastYtdlpError(stderr); line != "" {
		detail = line
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return cookieLoginUnknown, detail
	}
	for _, marker := range cookieLoginMarkers {
		if strings.Contains(low, marker) {
			return cookieLoginInvalid, detail
		}
	}
	return cookieLoginUnknown, detail
}

func lastYtdlpError(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "ERROR:") {
			if len(line) > 300 {
				line = line[:300] + "…"
			}
			return line
		}
	}
	return ""
}

// cookieStatus reports "suspect" while the persisted alert time is inside the
// cooldown window and "ok" otherwise; it is exposed through /healthz.
func (a *app) cookieStatus(ctx context.Context) string {
	var last time.Time
	if a.store != nil {
		last = a.store.cookieAlertTime(ctx)
	} else {
		a.cookieAlerts.mu.Lock()
		last = a.cookieAlerts.lastAlert
		a.cookieAlerts.mu.Unlock()
	}
	if !last.IsZero() && a.cookieAlerts.clock().Sub(last) < cookieAlertCooldown {
		return "suspect"
	}
	return "ok"
}

func cookieCheckKeyboard(lang string) *tgbotapi.InlineKeyboardMarkup {
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_cookie_check", lang), cookieCheckCallback)),
	)
	return &markup
}

// handleCookieCheck runs the /log fresh flow against a fixed YouTube URL for an
// administrator who pressed the button on the cookie alert. Non-admins are ignored.
func (a *app) handleCookieCheck(callback *tgbotapi.CallbackQuery) {
	if !a.isAdmin(callback.From.ID) || a.downloader == nil {
		return
	}
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	a.runDownloadTrace(callback.From.ID, chatID, cookieCheckURL, true)
}
