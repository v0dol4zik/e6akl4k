package main

import (
	"context"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// errorDigestMetadataKey persists the end of the last digest window across restarts.
	errorDigestMetadataKey = "error_digest_at"
	maxErrorDigestRows     = 10
)

// startErrorDigest posts a silent summary of expected failures (deleted, private, geo-blocked
// or missing content) once per ERROR_DIGEST_INTERVAL; real failures are posted as they happen.
func (a *app) startErrorDigest(ctx context.Context) {
	reporter := a.errorReports
	interval := a.cfg.ErrorDigestInterval
	if reporter == nil || a.store == nil || interval <= 0 {
		return
	}
	tick := min(time.Hour, max(time.Minute, interval/4))
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			a.maybePostErrorDigest(ctx, reporter.chatID, interval, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (a *app) maybePostErrorDigest(ctx context.Context, chatID int64, interval time.Duration, now time.Time) {
	last := time.Time{}
	if unix, err := strconv.ParseInt(a.store.metadata(ctx, errorDigestMetadataKey), 10, 64); err == nil && unix > 0 {
		last = time.Unix(unix, 0)
	}
	if last.IsZero() {
		// The first window starts now instead of summarising everything already retained.
		_ = a.store.setMetadata(ctx, errorDigestMetadataKey, strconv.FormatInt(now.Unix(), 10))
		return
	}
	if now.Sub(last) < interval {
		return
	}
	if err := a.store.pruneErrorReports(ctx, now.Add(-errorReportRetention)); err != nil {
		log.Printf("Удалить старые отчёты об ошибках: %v", err)
	}
	rows, err := a.store.errorDigest(ctx, last, maxErrorDigestRows)
	if err != nil {
		log.Printf("Собрать сводку ошибок: %v", err)
		return
	}
	real, expected, err := a.store.errorReportCounts(ctx, last)
	if err != nil {
		log.Printf("Посчитать ошибки для сводки: %v", err)
		return
	}
	if expected > 0 {
		message := tgbotapi.NewMessage(chatID, formatErrorDigest(rows, real, expected, now.Sub(last), defaultLang))
		message.ParseMode = "HTML"
		message.DisableWebPagePreview = true
		message.DisableNotification = true
		if _, err := sendTelegram(a.bot, message); err != nil {
			log.Printf("Не удалось отправить сводку ошибок в чат %d: %v", chatID, err)
			return
		}
	}
	_ = a.store.setMetadata(ctx, errorDigestMetadataKey, strconv.FormatInt(now.Unix(), 10))
}

func formatErrorDigest(rows []errorDigestRow, real, expected int, window time.Duration, lang string) string {
	hours := max(1, int((window + 30*time.Minute).Hours()))
	lines := []string{
		tr("error_digest_title", lang, "hours", strconv.Itoa(hours)),
		tr("error_digest_totals", lang, "expected", strconv.Itoa(expected), "real", strconv.Itoa(real)),
	}
	shown := 0
	for _, row := range rows {
		shown += row.Count
		sample := strings.Join(strings.Fields(row.Sample), " ")
		lines = append(lines, tr("error_digest_line", lang,
			"stage", html.EscapeString(row.Stage),
			"count", strconv.Itoa(row.Count),
			"users", strconv.Itoa(row.Users),
			"error", html.EscapeString(shortenRunes(sample, 160))))
	}
	if rest := expected - shown; rest > 0 {
		lines = append(lines, tr("error_digest_rest", lang, "count", strconv.Itoa(rest)))
	}
	return strings.Join(lines, "\n")
}
