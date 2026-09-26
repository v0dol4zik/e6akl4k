package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type downloadTrace struct {
	mu      sync.Mutex
	started time.Time
	id      string
	lines   []string
}

func newDownloadTrace() *downloadTrace {
	id, err := randomID()
	if err != nil {
		id = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	trace := &downloadTrace{started: time.Now(), id: id}
	trace.add("trace started")
	return trace
}

func (t *downloadTrace) add(format string, values ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	line := fmt.Sprintf(format, values...)
	t.lines = append(t.lines, fmt.Sprintf("[%09.3fs] %s", time.Since(t.started).Seconds(), line))
}

func (t *downloadTrace) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return "trace_id=" + t.id + "\n" + strings.Join(t.lines, "\n") + "\n"
}

func safeTraceURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid-url]"
	}
	query := url.Values{}
	for _, key := range []string{"t", "track", "v", "list"} {
		if value := parsed.Query().Get(key); value != "" {
			query.Set(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

func (a *app) handleAdminDownloadLog(message *tgbotapi.Message) {
	if !a.requireAdmin(message, false) || a.downloader == nil {
		return
	}
	arguments := strings.TrimSpace(message.CommandArguments())
	fresh := false
	if strings.HasPrefix(strings.ToLower(arguments), "fresh ") {
		fresh = true
		arguments = strings.TrimSpace(arguments[len("fresh "):])
	}
	rawURL := detectURL(arguments)
	if rawURL == "" {
		a.sendText(message.Chat.ID, adminUsage(a.langOrDefault(message.From.ID), "/log [fresh] <ссылка>"), "HTML", nil)
		return
	}
	a.runDownloadTrace(message.From.ID, message.Chat.ID, rawURL, fresh)
}

// runDownloadTrace executes the diagnostic download flow for an already
// authorised administrator and delivers the redacted trace to chatID.
func (a *app) runDownloadTrace(userID, chatID int64, rawURL string, fresh bool) {
	if !a.beginUserDownload(userID) {
		a.sendText(chatID, tr("user_download_active", a.langOrDefault(userID)), "", nil)
		return
	}
	defer a.finishUserDownload(userID)
	trace := newDownloadTrace()
	trace.add("mode=%s url=%s", map[bool]string{true: "fresh", false: "normal"}[fresh], safeTraceURL(rawURL))
	if a.store != nil {
		a.store.audit(a.ctx, userID, "download_log", userID, "fresh="+strconv.FormatBool(fresh)+" url="+safeTraceURL(rawURL))
	}
	cancelKey, keyErr := randomID()
	if keyErr != nil {
		cancelKey = trace.id
	}
	status := a.sendText(chatID, "🧪 <b>download trace</b>\n<code>"+trace.id+"</code>\ninspect…", "HTML", downloadCancelKeyboard(cancelKey, a.langOrDefault(userID)))

	ctx, cancel := context.WithTimeout(a.ctx, downloadTimeout)
	defer cancel()
	a.mu.Lock()
	a.active[cancelKey] = activeDownload{cancel: cancel, chatID: chatID, userID: userID}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.active, cancelKey)
		a.mu.Unlock()
	}()
	inspectStarted := time.Now()
	preview, err := a.inspectURL(ctx, rawURL)
	trace.add("inspect completed in %s ok=%t", time.Since(inspectStarted).Truncate(time.Millisecond), err == nil)
	a.updateDownloadTraceStatus(status, trace, "inspect complete")
	if err != nil {
		trace.add("inspect error: %s", redactTraceText(err.Error()))
		a.finishDownloadTrace(chatID, status, trace)
		return
	}
	trace.add("metadata extractor=%s source_id=%s title=%q artist=%q duration=%s tracks=%d", preview.Extractor, preview.SourceID, preview.Title, preview.Artist, preview.Duration, preview.TrackCount)
	a.updateDownloadTraceStatus(status, trace, "metadata received")
	if preview.IsPlaylist || preview.TrackCount > 1 {
		trace.add("stopped: /log accepts one track, not a collection")
		a.finishDownloadTrace(chatID, status, trace)
		return
	}
	pending := pendingURL{URL: rawURL, ChatID: chatID, UserID: userID, Preview: preview}
	lang := a.langOrDefault(userID)
	if !fresh {
		if a.store != nil {
			cacheKey := sourceCacheKey(preview.Extractor, preview.SourceID, "mp3", "320")
			_, hit := a.store.cachedAudio(ctx, cacheKey, a.cfg.CacheTTL)
			trace.add("persistent_cache_hit=%t key=%s", hit, cacheKey)
		}
		trace.add("production fast/cache path started")
		a.updateDownloadTraceStatus(status, trace, "cache / fast path")
		handled, succeeded, tooLarge := a.tryCachedDownload(ctx, chatID, pending, "mp3", "320", lang, func(position int) {
			trace.add("download queue position=%d", position)
		})
		trace.add("production fast/cache path handled=%t succeeded=%t too_large=%t", handled, succeeded, tooLarge)
		if handled {
			a.finishDownloadTrace(chatID, status, trace)
			return
		}
	} else {
		trace.add("persistent audio cache bypassed")
	}

	trace.add("local source download started format=mp3 quality=320")
	a.updateDownloadTraceStatus(status, trace, "local source download")
	results, err := a.runDownloadRangeQueued(ctx, rawURL, "mp3", "320", 0, 0, func(position int) {
		trace.add("download queue position=%d", position)
	}, func(current, total int) {
		trace.add("source progress track=%d/%d", current, total)
	})
	if err != nil {
		trace.add("download error: %s", redactTraceText(err.Error()))
		a.finishDownloadTrace(chatID, status, trace)
		return
	}
	for index, result := range results {
		trace.add("result %d title=%q error=%q size=%d file=%s", index+1, result.Title, redactTraceText(result.Error), regularFileSize(result.FilePath), filepath.Base(result.FilePath))
	}
	uploadStarted := time.Now()
	a.updateDownloadTraceStatus(status, trace, "Telegram upload")
	report := a.sendResultsIndividually(chatID, results, "mp3", lang)
	trace.add("telegram delivery completed in %s delivered=%d failed=%d", time.Since(uploadStarted).Truncate(time.Millisecond), report.Delivered, report.Failed)
	a.finishDownloadTrace(chatID, status, trace)
}

func (a *app) updateDownloadTraceStatus(status *tgbotapi.Message, trace *downloadTrace, stage string) {
	if status == nil {
		return
	}
	text := "🧪 <b>download trace</b>\n<code>" + trace.id + "</code>\n" + htmlEscapeTrace(stage) + " · " + adminTraceStamp(trace.started)
	a.editStatusMessage(status, text)
}

func redactTraceText(value string) string {
	for _, marker := range []string{"&k=", "?k=", "Authorization", "Cookie"} {
		if index := strings.Index(strings.ToLower(value), strings.ToLower(marker)); index >= 0 {
			value = value[:index] + "[redacted]"
		}
	}
	if len(value) > 1000 {
		value = value[:1000] + "…"
	}
	return value
}

func (a *app) finishDownloadTrace(chatID int64, status *tgbotapi.Message, trace *downloadTrace) {
	trace.add("trace finished")
	if status != nil {
		a.editStatusMessageFinal(status, "🧪 <b>download trace finished</b>\n<code>"+trace.id+"</code>")
	}
	directory := a.downloader.downloadDir
	file, err := os.CreateTemp(directory, ".download-trace-*.txt")
	if err != nil {
		a.sendText(chatID, "trace:\n<pre>"+htmlEscapeTrace(trace.text())+"</pre>", "HTML", nil)
		return
	}
	path := file.Name()
	defer os.Remove(path)
	_ = file.Chmod(0o600)
	_, writeErr := file.WriteString(trace.text())
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		a.sendText(chatID, "trace:\n<pre>"+htmlEscapeTrace(trace.text())+"</pre>", "HTML", nil)
		return
	}
	document := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(path))
	document.Caption = "trace_id=" + trace.id
	_, _ = sendTelegram(a.bot, document)
}

func htmlEscapeTrace(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	if len([]rune(value)) > 3500 {
		value = string([]rune(value)[:3500]) + "…"
	}
	return value
}
