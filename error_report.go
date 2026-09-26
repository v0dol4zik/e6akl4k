package main

import (
	"context"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// errorReportQueueSize bounds reports waiting for the worker; overflow is dropped and counted.
	errorReportQueueSize = 64
	// errorReportInterval spaces channel posts so a burst of failures stays under Telegram's limits.
	errorReportInterval = 3 * time.Second
	// errorReportDedupWindow folds identical stage+error pairs into one post with a repeat count.
	errorReportDedupWindow = 10 * time.Minute
	// maxErrorReportSeen caps the dedup map; expired entries are pruned before it grows further.
	maxErrorReportSeen = 512
	// maxErrorReportText keeps one post well under Telegram's 4096-character message limit.
	maxErrorReportText = 1500
	// maxStoredErrorText caps the redacted error kept in SQLite for the buttons and the digest.
	maxStoredErrorText = 2000
	// errorSpikeWindow, errorSpikeUsers and errorSpikeCooldown define a spike: the same real
	// failure for errorSpikeUsers distinct users inside the window is posted loudly, at most
	// once per cooldown for one fingerprint.
	errorSpikeWindow   = 15 * time.Minute
	errorSpikeUsers    = 3
	errorSpikeCooldown = time.Hour
	// maxErrorSpikes and maxErrorSpikeUsers bound the spike tracker.
	maxErrorSpikes     = 256
	maxErrorSpikeUsers = 64
)

// Report classes: expected failures (deleted, private, geo-blocked or missing content) only
// reach the daily digest, real ones are posted to the operator chat right away.
const (
	errorClassExpected = "expected"
	errorClassReal     = "real"
)

// errorReport describes one user-facing failure that is forwarded to the operator chat.
// UserID may be zero when only the chat is known (delivery helpers receive a chat ID).
type errorReport struct {
	Stage  string
	ChatID int64
	UserID int64
	URL    string
	Query  string
	Format string
	Error  string
}

type errorReportEntry struct {
	report  errorReport
	repeats int
	// id is the short report ID shown in the post and written to the log line.
	id      string
	version string
	// spikeUsers is set when the same failure hit that many distinct users inside
	// errorSpikeWindow; such an entry is posted with a sound notification.
	spikeUsers int
}

type errorSpike struct {
	users     map[int64]time.Time
	alertedAt time.Time
}

// errorSpikeRecord is the last spike, shown by the status message.
type errorSpikeRecord struct {
	At    time.Time
	Stage string
	ID    string
	Users int
}

type errorReportSeen struct {
	first   time.Time
	repeats int
}

// errorReporter forwards failures users see to the cache channel (or ERROR_CHAT_ID). Reports
// are deduplicated at enqueue time, so the bounded queue only holds distinct failures.
type errorReporter struct {
	chatID   int64
	queue    chan errorReportEntry
	interval time.Duration
	now      func() time.Time

	mu        sync.Mutex
	seen      map[string]*errorReportSeen
	spikes    map[string]*errorSpike
	lastSpike errorSpikeRecord
}

func newErrorReporter(chatID int64) *errorReporter {
	return &errorReporter{
		chatID:   chatID,
		queue:    make(chan errorReportEntry, errorReportQueueSize),
		interval: errorReportInterval,
		seen:     make(map[string]*errorReportSeen),
		spikes:   make(map[string]*errorSpike),
	}
}

func (r *errorReporter) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// admit reports whether a failure should be posted now and how many identical failures were
// folded into it since the previous post of the same stage and error.
func (r *errorReporter) admit(report errorReport) (int, bool) {
	key := report.Stage + "\x00" + errorReportFingerprint(report.Error)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	if seen, ok := r.seen[key]; ok && now.Sub(seen.first) < errorReportDedupWindow {
		seen.repeats++
		return 0, false
	}
	repeats := 0
	if seen, ok := r.seen[key]; ok {
		repeats = seen.repeats
	}
	if len(r.seen) >= maxErrorReportSeen {
		for existing, seen := range r.seen {
			if now.Sub(seen.first) >= errorReportDedupWindow {
				delete(r.seen, existing)
			}
		}
		if len(r.seen) >= maxErrorReportSeen {
			return 0, false
		}
	}
	r.seen[key] = &errorReportSeen{first: now}
	return repeats, true
}

// spike records that who hit the failure and reports whether this makes a new spike, with the
// number of distinct users inside errorSpikeWindow. who is zero when nobody is known.
func (r *errorReporter) spike(fingerprint string, who int64, stage, id string) (int, bool) {
	if who == 0 {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	current := r.spikes[fingerprint]
	if current == nil {
		if len(r.spikes) >= maxErrorSpikes {
			for key, existing := range r.spikes {
				existing.prune(now)
				if len(existing.users) == 0 && now.Sub(existing.alertedAt) >= errorSpikeCooldown {
					delete(r.spikes, key)
				}
			}
			if len(r.spikes) >= maxErrorSpikes {
				return 0, false
			}
		}
		current = &errorSpike{users: make(map[int64]time.Time)}
		r.spikes[fingerprint] = current
	}
	current.prune(now)
	if _, ok := current.users[who]; ok || len(current.users) < maxErrorSpikeUsers {
		current.users[who] = now
	}
	users := len(current.users)
	if users < errorSpikeUsers || (!current.alertedAt.IsZero() && now.Sub(current.alertedAt) < errorSpikeCooldown) {
		return users, false
	}
	current.alertedAt = now
	r.lastSpike = errorSpikeRecord{At: now, Stage: stage, ID: id, Users: users}
	return users, true
}

func (s *errorSpike) prune(now time.Time) {
	for user, seen := range s.users {
		if now.Sub(seen) >= errorSpikeWindow {
			delete(s.users, user)
		}
	}
}

func (r *errorReporter) lastSpikeRecord() errorSpikeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSpike
}

// errorReportFingerprint strips per-video identifiers so the same yt-dlp failure on different
// tracks is folded together: "[youtube] abc123: Video unavailable" -> "[youtube]: video unavailable".
func errorReportFingerprint(message string) string {
	message = strings.ToLower(strings.TrimSpace(message))
	if open := strings.Index(message, "["); open >= 0 {
		if closing := strings.Index(message[open:], "] "); closing > 0 {
			rest := message[open+closing+2:]
			if colon := strings.Index(rest, ":"); colon > 0 && !strings.ContainsAny(rest[:colon], " /") {
				message = message[:open+closing+1] + rest[colon:]
			}
		}
	}
	return shortenRunes(message, 200)
}

// expectedErrorMarkers identify failures caused by the content itself rather than the bot.
var expectedErrorMarkers = []string{
	"video unavailable", "видео недоступно", "this video is not available", "content isn't available",
	"private video", "видео приватное",
	"not available in your country", "заблокировано в этом регионе", "geo restrict", "geo-restrict",
	"unsupported url", "ссылка не поддерживается", "is not a valid url",
	"has been removed", "has been terminated", "no longer available",
	"members-only", "members only", "join this channel",
	"premieres in", "live event will begin", "this live event",
	"плейлист пуст или недоступен", "the playlist does not exist",
	errNothingFound.Error(), "no results", "no video formats found",
	"http error 404", errLastfmNotFound.Error(),
}

// realErrorMarkers override the expected markers: YouTube words rate limiting and bot checks as
// "video unavailable ... try again later", which is a bot-side problem.
var realErrorMarkers = []string{
	"try again later", "http error 403", "forbidden", "not a bot", "sign in to confirm",
	"too many requests", "http error 429", "rate-limit", "rate limit",
}

// classifyErrorReport returns errorClassExpected only when every meaningful line of the error
// is an expected content failure; anything unknown is real.
func classifyErrorReport(message string) string {
	low := strings.ToLower(message)
	for _, marker := range realErrorMarkers {
		if strings.Contains(low, marker) {
			return errorClassReal
		}
	}
	matched := false
	for _, line := range strings.Split(low, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "… +") || strings.HasPrefix(line, "warning:") {
			continue
		}
		expected := false
		for _, marker := range expectedErrorMarkers {
			if strings.Contains(line, marker) {
				expected = true
				break
			}
		}
		if !expected {
			return errorClassReal
		}
		matched = true
	}
	if !matched {
		return errorClassReal
	}
	return errorClassExpected
}

func newErrorReportID() string {
	if id, err := randomID(); err == nil {
		return "r" + id[:7]
	}
	return fmt.Sprintf("r%07x", time.Now().UnixNano()&0xfffffff)
}

// reportVersion is the bot and yt-dlp version stamped on every report.
func (a *app) reportVersion() string {
	version := buildVersion()
	if ytdlp := cachedYtdlpVersion(); ytdlp != "" {
		version += ", yt-dlp " + ytdlp
	}
	return version
}

// reportError logs a failure with a short ID, stores it and queues real ones for the operator
// chat without blocking the user's flow. Expected failures wait for the digest. Cancellations
// are ignored; callers skip queue-full, rate-limit and size-limit notices.
func (a *app) reportError(report errorReport) {
	if strings.TrimSpace(report.Error) == "" || isCancellationText(report.Error) {
		return
	}
	id := newErrorReportID()
	class := classifyErrorReport(report.Error)
	redacted := strings.ToValidUTF8(redactTraceText(report.Error), "")
	source := ""
	if report.URL != "" {
		source = sourceHost(report.URL)
	}
	log.Printf("error_report id=%s stage=%s class=%s source=%s user_id=%d chat_id=%d format=%q: %s",
		id, report.Stage, class, source, report.UserID, report.ChatID, report.Format, shortenRunes(strings.Join(strings.Fields(redacted), " "), 500))
	reporter := a.errorReports
	if reporter == nil {
		return
	}
	fingerprint := errorReportFingerprint(report.Error)
	version := a.reportVersion()
	if a.store != nil {
		record := errorReportRecord{
			ID: id, Stage: report.Stage, Class: class, Fingerprint: fingerprint, ChatID: report.ChatID, UserID: report.UserID,
			URL: report.URL, Query: report.Query, Format: report.Format, Error: shortenRunes(redacted, maxStoredErrorText),
			Version: version, CreatedAt: time.Now(),
		}
		err := a.store.saveErrorReport(a.ctx, record)
		if isUniqueViolation(err) {
			id = newErrorReportID()
			record.ID = id
			err = a.store.saveErrorReport(a.ctx, record)
		}
		if err != nil {
			log.Printf("Сохранить отчёт об ошибке %s: %v", id, err)
		}
	}
	if class == errorClassExpected {
		return
	}
	who := report.UserID
	if who == 0 {
		who = report.ChatID
	}
	if a.isAdmin(who) {
		// An administrator repeating a reported request is not another affected user.
		who = 0
	}
	repeats, admitted := reporter.admit(report)
	users, spiked := reporter.spike(fingerprint, who, report.Stage, id)
	if !admitted && !spiked {
		return
	}
	entry := errorReportEntry{report: report, repeats: repeats, id: id, version: version}
	if spiked {
		entry.spikeUsers = users
	}
	select {
	case reporter.queue <- entry:
	default:
		if a.store != nil {
			a.store.increment(a.ctx, "error_reports_dropped")
		}
	}
}

func isCancellationText(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, context.Canceled.Error()) || strings.Contains(message, context.DeadlineExceeded.Error())
}

// startErrorReporter posts queued reports to ERROR_CHAT_ID or, by default, the cache channel.
// It is a no-op when neither chat is configured.
func (a *app) startErrorReporter(ctx context.Context) {
	chatID := a.cfg.ErrorChatID
	if chatID == 0 {
		chatID = a.cfg.CacheChatID
	}
	if chatID == 0 || a.bot == nil {
		return
	}
	reporter := newErrorReporter(chatID)
	a.errorReports = reporter
	go a.runErrorReporter(ctx, reporter)
}

func (a *app) runErrorReporter(ctx context.Context, reporter *errorReporter) {
	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-reporter.queue:
			a.postErrorReport(ctx, reporter.chatID, entry)
			if reporter.interval > 0 {
				timer := time.NewTimer(reporter.interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
	}
}

func (a *app) postErrorReport(ctx context.Context, chatID int64, entry errorReportEntry) {
	username := ""
	if a.store != nil && entry.report.UserID > 0 {
		username = a.store.telegramUsername(ctx, entry.report.UserID)
	}
	message := tgbotapi.NewMessage(chatID, formatErrorReport(entry, username, defaultLang))
	message.ParseMode = "HTML"
	message.DisableWebPagePreview = true
	// Single reports arrive silently; only a spike of the same failure makes a sound.
	message.DisableNotification = entry.spikeUsers == 0
	if a.store != nil && entry.id != "" {
		message.ReplyMarkup = errorReportKeyboard(entry.id, entry.report, defaultLang)
	}
	if _, err := sendTelegram(a.bot, message); err != nil {
		log.Printf("Не удалось отправить отчёт об ошибке в чат %d: %v", chatID, err)
		return
	}
	if a.store != nil {
		a.store.increment(a.ctx, "error_reports_sent")
	}
}

// formatErrorReport renders an HTML post. Every value is redacted and escaped: URLs keep only
// the track-identifying query parameters and error text loses signed-URL and cookie fragments.
func formatErrorReport(entry errorReportEntry, username, lang string) string {
	report := entry.report
	var lines []string
	if entry.spikeUsers > 0 {
		lines = append(lines, tr("error_report_spike", lang, "users", strconv.Itoa(entry.spikeUsers), "minutes", strconv.Itoa(int(errorSpikeWindow.Minutes()))))
	}
	title := tr("error_report_title", lang, "stage", html.EscapeString(report.Stage))
	if entry.id != "" {
		title += " · <code>" + html.EscapeString(entry.id) + "</code>"
	}
	lines = append(lines, title)
	switch {
	case report.UserID != 0:
		who := "<code>" + strconv.FormatInt(report.UserID, 10) + "</code>"
		if username != "" {
			who += " @" + html.EscapeString(username)
		}
		lines = append(lines, tr("error_report_user", lang, "user", who))
	case report.ChatID != 0:
		lines = append(lines, tr("error_report_chat", lang, "chat", "<code>"+strconv.FormatInt(report.ChatID, 10)+"</code>"))
	}
	if report.URL != "" {
		lines = append(lines, tr("error_report_url", lang, "url", html.EscapeString(safeTraceURL(report.URL))))
	}
	if report.Query != "" {
		lines = append(lines, tr("error_report_query", lang, "query", html.EscapeString(shortenRunes(report.Query, 200))))
	}
	if report.Format != "" {
		lines = append(lines, tr("error_report_format", lang, "format", html.EscapeString(report.Format)))
	}
	text := strings.ToValidUTF8(redactTraceText(report.Error), "")
	lines = append(lines, tr("error_report_error", lang, "error", html.EscapeString(shortenRunes(text, maxErrorReportText))))
	if entry.repeats > 0 {
		lines = append(lines, tr("error_report_repeats", lang, "count", strconv.Itoa(entry.repeats), "minutes", strconv.Itoa(int(errorReportDedupWindow.Minutes()))))
	}
	if entry.version != "" {
		lines = append(lines, tr("error_report_version", lang, "version", html.EscapeString(entry.version)))
	}
	return strings.Join(lines, "\n")
}

// deliveryFailures collects per-track delivery errors so that one playlist or batch delivery
// produces a single report instead of one post per track.
type deliveryFailures struct {
	total int
	count int
	lines []string
}

const maxDeliveryFailureLines = 5

func (f *deliveryFailures) add(index int, message string) {
	f.count++
	if len(f.lines) < maxDeliveryFailureLines {
		f.lines = append(f.lines, "["+strconv.Itoa(index)+"/"+strconv.Itoa(f.total)+"] "+message)
	}
}

func (a *app) reportDeliveryFailures(stage string, chatID int64, format string, failures deliveryFailures) {
	if failures.count == 0 {
		return
	}
	text := strings.Join(failures.lines, "\n")
	if extra := failures.count - len(failures.lines); extra > 0 {
		text += "\n… +" + strconv.Itoa(extra)
	}
	a.reportError(errorReport{Stage: stage, ChatID: chatID, Format: format, Error: text})
}
