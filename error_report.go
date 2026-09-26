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

	mu   sync.Mutex
	seen map[string]*errorReportSeen
}

func newErrorReporter(chatID int64) *errorReporter {
	return &errorReporter{
		chatID:   chatID,
		queue:    make(chan errorReportEntry, errorReportQueueSize),
		interval: errorReportInterval,
		seen:     make(map[string]*errorReportSeen),
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

// reportError queues a failure for the operator chat without blocking the user's flow.
// Cancellations never reach the chat; callers skip queue-full, rate-limit and size-limit notices.
func (a *app) reportError(report errorReport) {
	reporter := a.errorReports
	if reporter == nil || strings.TrimSpace(report.Error) == "" || isCancellationText(report.Error) {
		return
	}
	repeats, ok := reporter.admit(report)
	if !ok {
		return
	}
	select {
	case reporter.queue <- errorReportEntry{report: report, repeats: repeats}:
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
	lines := []string{tr("error_report_title", lang, "stage", html.EscapeString(report.Stage))}
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
