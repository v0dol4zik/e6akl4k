package main

import (
	"context"
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
)

// cookieAlertState keeps the in-memory part of the stale-cookie detector: the
// sliding window of recent YouTube 403 failures and a cached copy of the persisted
// last-alert time so ordinary failures never touch the database.
type cookieAlertState struct {
	mu        sync.Mutex
	forbidden []time.Time
	lastAlert time.Time
	loaded    bool
	now       func() time.Time
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
	if !s.loaded {
		s.loaded = true
		if state != nil {
			s.lastAlert = state.cookieAlertTime(ctx)
		}
	}
	now := s.clock()
	if !s.lastAlert.IsZero() && now.Sub(s.lastAlert) < cookieAlertCooldown {
		return false
	}
	s.lastAlert = now
	if state != nil {
		_ = state.setMetadata(ctx, cookieAlertMetadataKey, strconv.FormatInt(now.Unix(), 10))
	}
	return true
}

func isCookieFailure(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "cookies.txt") || strings.Contains(message, "not a bot") || strings.Contains(message, "подтверждения, что запрос не от бота")
}

func isForbiddenFailure(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "http error 403") || strings.Contains(message, "forbidden")
}

func isYouTubeSource(host string) bool {
	return strings.Contains(strings.ToLower(host), "youtu")
}

// reportDownloadFailure counts a failed download and alerts administrators when
// the failure looks like stale YouTube cookies: either an explicit bot-check
// message or repeated HTTP 403 responses from a YouTube host.
func (a *app) reportDownloadFailure(message, source string) {
	if a.store != nil {
		a.store.increment(a.ctx, "downloads_failed")
	}
	cookie := isCookieFailure(message)
	if !cookie && isYouTubeSource(source) && isForbiddenFailure(message) {
		cookie = a.cookieAlerts.recordForbidden()
	}
	if !cookie {
		return
	}
	if a.store != nil {
		a.store.increment(a.ctx, "youtube_cookie_errors")
	}
	if !a.cookieAlerts.alertDue(a.ctx, a.store) {
		return
	}
	for _, adminID := range a.administratorIDs(a.ctx) {
		lang := a.langOrDefault(adminID)
		a.sendText(adminID, tr("admin_cookie_warning", lang), "", cookieCheckKeyboard(lang))
	}
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
