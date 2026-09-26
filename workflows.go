package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (a *app) handleIncomingURL(message *tgbotapi.Message, rawURL, lang string) {
	status := a.sendText(message.Chat.ID, tr("analyzing", lang), "HTML", nil)
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	_, release, err := a.lookups.acquire(ctx)
	if err != nil {
		if errors.Is(err, errQueueFull) && a.store != nil {
			a.store.increment(a.ctx, "queue_rejected")
		}
		a.handleQueueError(message.Chat.ID, lang, err, errorReport{Stage: "preview", UserID: message.From.ID, URL: rawURL})
		return
	}
	preview, err := a.inspectURL(ctx, rawURL)
	release()
	if err != nil {
		a.reportError(errorReport{Stage: "preview", ChatID: message.Chat.ID, UserID: message.From.ID, URL: rawURL, Error: err.Error()})
		a.sendText(message.Chat.ID, tr("preview_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	if requiresMusicResolution(rawURL) {
		query := strings.TrimSpace(strings.Join([]string{preview.Artist, preview.Title}, " "))
		if query == "" {
			query = preview.Title
		}
		a.presentSearchResults(message.Chat.ID, message.From.ID, query, lang, status, true, preview.DurationSeconds)
		return
	}
	key, err := a.storeURL(pendingURL{URL: rawURL, ChatID: message.Chat.ID, UserID: message.From.ID, Preview: preview})
	if err != nil {
		log.Printf("save pending URL: %v", err)
		return
	}
	if !preview.IsPlaylist {
		if format, quality, ok := a.getPreference(message.From.ID); ok {
			a.deleteStatusMessage(status)
			a.startDownload(message.From.ID, message.Chat.ID, key, format, quality, lang, nil)
			return
		}
	}
	text := previewText(preview, lang)
	var keyboard *tgbotapi.InlineKeyboardMarkup
	if preview.IsPlaylist {
		keyboard = rangeKeyboard(key, preview.TrackCount, a.cfg.MaxPlaylistTracks, lang)
	} else {
		keyboard = formatKeyboard(key, lang)
	}
	if status != nil {
		edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, text, *keyboard)
		edit.ParseMode = "HTML"
		if _, err := sendTelegram(a.bot, edit); err == nil {
			return
		}
	}
	a.sendText(message.Chat.ID, text, "HTML", keyboard)
}

func (a *app) handlePrivateSearch(message *tgbotapi.Message, query, lang string) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < inlineMinQueryLen {
		a.sendText(message.Chat.ID, tr("search_too_short", lang), "", nil)
		return
	}
	status := a.sendText(message.Chat.ID, tr("searching", lang), "HTML", nil)
	a.presentSearchResults(message.Chat.ID, message.From.ID, query, lang, status, false, 0)
}

func (a *app) presentSearchResults(chatID, userID int64, query, lang string, status *tgbotapi.Message, resolved bool, expectedDuration int) {
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	candidates, err := a.runRankedLookup(ctx, query, expectedDuration)
	if err != nil {
		a.handleQueueError(chatID, lang, err, errorReport{Stage: "search", UserID: userID, Query: query})
		return
	}
	if a.store != nil {
		a.store.increment(a.ctx, "searches")
	}
	keys := make([]string, 0, len(candidates))
	for _, item := range candidates {
		seconds := inlineDurationSeconds(item.Duration)
		sourceID, extractor := item.SourceID, item.Extractor
		if sourceID == "" || extractor == "" {
			parts := strings.Split(item.CacheKey, ":")
			if len(parts) >= 2 {
				extractor, sourceID = parts[0], parts[1]
			}
		}
		preview := mediaPreview{URL: item.URL, Title: item.Title, Artist: item.Artist, Duration: item.Duration, DurationSeconds: seconds, TrackCount: 1, SourceID: sourceID, Extractor: extractor}
		key, keyErr := a.storeURL(pendingURL{URL: item.URL, ChatID: chatID, UserID: userID, Preview: preview})
		if keyErr == nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		a.sendText(chatID, tr("nothing_found", lang), "", nil)
		return
	}
	prefix := searchResultsHeader(resolved, lang)
	keyboard := searchKeyboard(keys, candidates, lang)
	if status != nil {
		edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, prefix, *keyboard)
		edit.ParseMode = "HTML"
		if _, err := sendTelegram(a.bot, edit); err == nil {
			return
		}
	}
	a.sendText(chatID, prefix, "HTML", keyboard)
}

// searchResultsHeader picks the results caption for a fresh search or a resolved metadata link.
func searchResultsHeader(resolved bool, lang string) string {
	if resolved {
		return tr("resolved_results", lang)
	}
	return tr("search_results", lang)
}

func (a *app) handleSearchPick(callback *tgbotapi.CallbackQuery) {
	key := strings.TrimPrefix(callback.Data, "pick:")
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	lang := a.langOrDefault(callback.From.ID)
	pending, ok := a.getURL(key, callback.From.ID, chatID)
	if !ok {
		a.sendText(callback.From.ID, tr("action_unavailable", lang), "", nil)
		return
	}
	if format, quality, ok := a.getPreference(callback.From.ID); ok {
		a.startDownload(callback.From.ID, chatID, key, format, quality, lang, callback)
		return
	}
	a.safeEdit(callback, previewText(pending.Preview, lang), "HTML", formatKeyboard(key, lang))
}

func (a *app) handleRangeChoice(callback *tgbotapi.CallbackQuery) {
	parts := strings.SplitN(callback.Data, ":", 3)
	if len(parts) != 3 {
		return
	}
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	lang := a.langOrDefault(callback.From.ID)
	pending, ok := a.getURL(parts[2], callback.From.ID, chatID)
	if !ok {
		a.sendText(callback.From.ID, tr("action_unavailable", lang), "", nil)
		return
	}
	start, end := 1, pending.Preview.TrackCount
	switch parts[1] {
	case "10":
		end = min(min(10, a.cfg.MaxPlaylistTracks), end)
	case "25":
		end = min(min(25, a.cfg.MaxPlaylistTracks), end)
	case "limit", "75":
		end = min(a.cfg.MaxPlaylistTracks, end)
	case "all":
		if end > a.cfg.MaxPlaylistTracks {
			end = a.cfg.MaxPlaylistTracks
		}
	default:
		bounds := strings.SplitN(parts[1], "-", 2)
		if len(bounds) != 2 {
			return
		}
		parsedStart, startErr := strconv.Atoi(bounds[0])
		parsedEnd, endErr := strconv.Atoi(bounds[1])
		if startErr != nil || endErr != nil || parsedStart < 1 || parsedEnd < parsedStart || parsedEnd > min(pending.Preview.TrackCount, a.cfg.MaxPlaylistTracks) {
			return
		}
		start, end = parsedStart, parsedEnd
	}
	pending, ok = a.setURLRange(parts[2], callback.From.ID, chatID, start, end)
	if !ok {
		return
	}
	if format, quality, ok := a.getPreference(callback.From.ID); ok {
		a.startDownload(callback.From.ID, chatID, parts[2], format, quality, lang, callback)
		return
	}
	text := previewText(pending.Preview, lang) + "\n" + tr("selected_range", lang, "start", strconv.Itoa(start), "end", strconv.Itoa(end))
	a.safeEdit(callback, text, "HTML", formatKeyboard(parts[2], lang))
}

func requiresMusicResolution(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, domain := range []string{"spotify.com", "music.apple.com", "deezer.com", "tidal.com", "music.yandex.ru", "music.yandex.com", "music.yandex.kz", "music.yandex.by", "music.yandex.uz"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func previewText(preview mediaPreview, lang string) string {
	title := html.EscapeString(shortenRunes(firstNonEmpty(preview.Title, "Unknown"), maxTitleLength))
	artist := html.EscapeString(shortenRunes(preview.Artist, maxTitleLength))
	lines := []string{"<b>" + title + "</b>"}
	if artist != "" {
		lines = append(lines, "👤 "+artist)
	}
	if preview.Duration != "" {
		lines = append(lines, "⏱ "+html.EscapeString(preview.Duration))
	}
	if preview.IsPlaylist {
		lines = append(lines, tr("preview_tracks", lang, "count", strconv.Itoa(preview.TrackCount)))
	}
	if preview.Estimated128 > 0 {
		lines = append(lines, tr("preview_sizes", lang, "size128", humanSize(preview.Estimated128, lang), "size320", humanSize(preview.Estimated320, lang)))
	}
	if preview.IsPlaylist {
		lines = append(lines, tr("choose_range", lang))
	} else {
		lines = append(lines, tr("choose_format", lang))
	}
	return strings.Join(lines, "\n")
}

func sourceCacheKey(extractor, id, format, quality string) string {
	if id == "" {
		return ""
	}
	extractor = strings.ToLower(extractor)
	if strings.Contains(extractor, "youtube") || extractor == "" {
		extractor = "youtube"
	}
	return extractor + ":" + id + ":" + strings.ToLower(format) + ":" + strings.ToLower(quality)
}

func generalCacheKey(rawURL, format, quality string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil {
		parsed.Fragment = ""
		parsed.Host = strings.ToLower(parsed.Host)
		rawURL = parsed.String()
	}
	sum := sha256.Sum256([]byte(rawURL))
	return "url:" + hex.EncodeToString(sum[:16]) + ":" + strings.ToLower(format) + ":" + strings.ToLower(quality)
}

func (a *app) tryCachedDownload(ctx context.Context, chatID int64, pending pendingURL, format, quality, lang string, queued func(int), reporters ...*statusReporter) (bool, bool) {
	if a.store == nil {
		return false, false
	}
	urlKey := generalCacheKey(pending.URL, format, quality)
	keys := []string{}
	if source := sourceCacheKey(pending.Preview.Extractor, pending.Preview.SourceID, format, quality); source != "" {
		keys = append(keys, source)
	}
	keys = append(keys, urlKey)
	for _, key := range keys {
		if entry, ok := a.store.cachedAudio(ctx, key, a.cfg.CacheTTL); ok {
			started := time.Now()
			err := a.sendCachedAudio(chatID, entry, lang)
			logMediaStage("telegram_file_id_send", "telegram", started, 0, err == nil, "media_size_bytes", entry.Size, "cache_hit", true, "format", entry.Format)
			if err == nil {
				a.store.increment(a.ctx, "cache_hits")
				return true, true
			} else if !invalidCachedFileError(err) {
				a.reportError(errorReport{Stage: "cache_send", ChatID: chatID, UserID: pending.UserID, URL: pending.URL, Format: format + " " + quality, Error: err.Error()})
				a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
				return true, false
			}
			a.store.deleteCachedAudio(ctx, key)
		}
	}
	if a.cfg.CacheChatID == 0 {
		return false, false
	}
	entry, err := a.ensureCachedAudio(ctx, pending, format, quality, queued, reporters...)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if a.store != nil {
				a.store.increment(a.ctx, "downloads_cancelled")
			}
			return true, false
		}
		a.reportDownloadFailure(err.Error(), sourceHost(pending.URL))
		a.reportError(errorReport{Stage: "download", ChatID: chatID, UserID: pending.UserID, URL: pending.URL, Format: format + " " + quality, Error: err.Error()})
		a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return true, false
	}
	started := time.Now()
	err = a.sendCachedAudio(chatID, entry, lang)
	logMediaStage("telegram_file_id_send", "telegram", started, 0, err == nil, "media_size_bytes", entry.Size, "cache_hit", false, "format", entry.Format)
	if err != nil {
		if invalidCachedFileError(err) {
			a.store.deleteCachedAudio(ctx, entry.Key)
			return false, false
		}
		a.reportError(errorReport{Stage: "cache_send", ChatID: chatID, UserID: pending.UserID, URL: pending.URL, Format: format + " " + quality, Error: err.Error()})
		a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return true, false
	}
	return true, true
}

func (a *app) ensureCachedAudio(ctx context.Context, pending pendingURL, format, quality string, queued func(int), reporters ...*statusReporter) (cachedAudio, error) {
	var reporter *statusReporter
	if len(reporters) > 0 {
		reporter = reporters[0]
	}
	ctx = withStatusReporter(ctx, reporter)
	if a.store == nil {
		return cachedAudio{}, errors.New("SQLite-кэш недоступен")
	}
	if a.cfg.CacheChatID == 0 {
		return cachedAudio{}, errors.New("cache-канал не настроен")
	}
	urlKey := generalCacheKey(pending.URL, format, quality)
	return a.flights.do(ctx, urlKey, func() (cachedAudio, error) {
		if cached, ok := a.store.cachedAudio(ctx, urlKey, a.cfg.CacheTTL); ok {
			return cached, nil
		}
		results, downloadErr := a.runDownloadRangeQueued(ctx, pending.URL, format, quality, pending.RangeStart, pending.RangeEnd, queued, nil)
		if downloadErr != nil {
			return cachedAudio{}, downloadErr
		}
		if len(results) != 1 || results[0].Error != "" || !regularFileExists(results[0].FilePath) {
			reason := "загрузка не вернула один готовый трек"
			if len(results) > 0 && results[0].Error != "" {
				reason = results[0].Error
			}
			if len(results) > 0 {
				a.downloader.clearSession(results[0].Session)
			}
			return cachedAudio{}, errors.New(reason)
		}
		result := results[0]
		defer a.downloader.clearSession(result.Session)
		info, statErr := os.Stat(result.FilePath)
		if statErr != nil {
			return cachedAudio{}, statErr
		}
		if info.Size() > a.fileLimit() {
			return cachedAudio{}, fmt.Errorf("файл слишком большой (%s)", humanSize(info.Size(), defaultLang))
		}
		entry := cachedAudio{Key: urlKey, Title: result.Title, Artist: result.Artist, Duration: result.Duration, Format: format, Quality: quality, Size: info.Size()}
		if telegramAudioFormat(format) {
			progress := newUploadBatchProgress(reporter, info.Size())
			audio := tgbotapi.NewAudio(a.cfg.CacheChatID, progressFile{path: result.FilePath, progress: progress})
			audio.Title, audio.Performer = result.Title, result.Artist
			uploadStarted := time.Now()
			sent, sendErr := sendTelegram(a.bot, audio)
			logMediaStage("telegram_upload", "telegram", uploadStarted, info.Size(), sendErr == nil && sent.Audio != nil, "mode", "multipart", "format", format)
			if sendErr != nil || sent.Audio == nil {
				return cachedAudio{}, firstError(sendErr, errors.New("Telegram не вернул audio file_id"))
			}
			entry.FileID, entry.MediaType = sent.Audio.FileID, "audio"
		} else {
			progress := newUploadBatchProgress(reporter, info.Size())
			document := tgbotapi.NewDocument(a.cfg.CacheChatID, progressFile{path: result.FilePath, progress: progress})
			uploadStarted := time.Now()
			sent, sendErr := sendTelegram(a.bot, document)
			logMediaStage("telegram_upload", "telegram", uploadStarted, info.Size(), sendErr == nil && sent.Document != nil, "mode", "multipart", "format", format)
			if sendErr != nil || sent.Document == nil {
				return cachedAudio{}, firstError(sendErr, errors.New("Telegram не вернул document file_id"))
			}
			entry.FileID, entry.MediaType = sent.Document.FileID, "document"
		}
		if putErr := a.storeCachedAudioAliases(ctx, entry, result.CacheKey); putErr != nil {
			return cachedAudio{}, putErr
		}
		return entry, nil
	})
}

func (a *app) storeCachedAudioAliases(ctx context.Context, entry cachedAudio, aliasKey string) error {
	if err := a.store.putCachedAudio(ctx, entry); err != nil {
		return err
	}
	if aliasKey != "" && aliasKey != entry.Key {
		alias := entry
		alias.Key = aliasKey
		_ = a.store.putCachedAudio(ctx, alias)
	}
	return nil
}

func sourceHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "unknown"
	}
	return strings.ToLower(parsed.Hostname())
}

func (a *app) sendCachedAudio(chatID int64, entry cachedAudio, lang string) error {
	result := downloadResult{Title: entry.Title, Artist: entry.Artist, Duration: entry.Duration}
	if entry.MediaType == "document" || !telegramAudioFormat(entry.Format) {
		document := tgbotapi.NewDocument(chatID, tgbotapi.FileID(entry.FileID))
		document.Caption = buildCaption(result, entry.Size, entry.Format, lang, 1, 1)
		document.ParseMode = "HTML"
		_, err := sendTelegram(a.bot, document)
		return err
	}
	audio := tgbotapi.NewAudio(chatID, tgbotapi.FileID(entry.FileID))
	audio.Title, audio.Performer = entry.Title, entry.Artist
	audio.Caption = buildCaption(result, entry.Size, entry.Format, lang, 1, 1)
	audio.ParseMode = "HTML"
	_, err := sendTelegram(a.bot, audio)
	return err
}

func invalidCachedFileError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "wrong file identifier") || strings.Contains(message, "file_id") || strings.Contains(message, "file reference")
}

func firstError(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}

func (a *app) editStatusMessage(message *tgbotapi.Message, text string) {
	if message == nil {
		return
	}
	edit := tgbotapi.NewEditMessageText(message.Chat.ID, message.MessageID, text)
	edit.ParseMode = "HTML"
	_, _ = sendTelegram(a.bot, edit)
}

func (a *app) editStatusMessageFinal(message *tgbotapi.Message, text string) {
	if message == nil {
		return
	}
	empty := tgbotapi.NewInlineKeyboardMarkup()
	edit := tgbotapi.NewEditMessageTextAndMarkup(message.Chat.ID, message.MessageID, text, empty)
	edit.ParseMode = "HTML"
	_, _ = sendTelegram(a.bot, edit)
}

func (a *app) deleteStatusMessage(message *tgbotapi.Message) {
	if message == nil || message.Chat == nil {
		return
	}
	if _, err := requestTelegram(a.bot, tgbotapi.NewDeleteMessage(message.Chat.ID, message.MessageID)); err != nil {
		log.Printf("Не удалось удалить завершённый статус загрузки: %v", err)
	}
}

// handleQueueError tells the user why a queued job failed; real failures (not a full queue or a
// cancellation) are also forwarded to the operator chat with the given report context.
func (a *app) handleQueueError(chatID int64, lang string, err error, report errorReport) {
	if errors.Is(err, errQueueFull) {
		a.sendText(chatID, tr("queue_full", lang), "", nil)
		return
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		report.ChatID, report.Error = chatID, err.Error()
		a.reportError(report)
		a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
	}
}

func (a *app) handleAdminStats(message *tgbotapi.Message) {
	if !a.requireAdmin(message, false) || a.store == nil {
		return
	}
	lang := a.langOrDefault(message.From.ID)
	stats, err := a.store.stats(a.ctx)
	if err != nil {
		a.sendText(message.Chat.ID, tr("admin_stats_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	text := tr("admin_stats", lang,
		"users", strconv.FormatInt(stats.UniqueUsers, 10),
		"cached", strconv.FormatInt(stats.CachedTracks, 10),
		"ok", strconv.FormatInt(stats.DownloadsOK, 10),
		"partial", strconv.FormatInt(stats.DownloadsPartial, 10),
		"failed", strconv.FormatInt(stats.DownloadsFailed, 10),
		"cancelled", strconv.FormatInt(stats.Cancelled, 10),
		"cookies", strconv.FormatInt(stats.CookieErrors, 10),
		"hits", strconv.FormatInt(stats.CacheHits, 10),
		"searches", strconv.FormatInt(stats.Searches, 10),
		"limited", strconv.FormatInt(stats.RateLimited, 10),
		"rejected", strconv.FormatInt(stats.QueueRejected, 10))
	a.sendText(message.Chat.ID, text, "HTML", nil)
}

func (a *app) handleAdminStatus(message *tgbotapi.Message) {
	if !a.requireAdmin(message, false) {
		return
	}
	lang := a.langOrDefault(message.From.ID)
	da, dw, dc := a.downloads.snapshot()
	la, lw, lc := a.lookups.snapshot()
	aa, aw, ac := a.archives.snapshot()
	text := tr("admin_status", lang,
		"downloads_active", strconv.Itoa(da),
		"downloads_capacity", strconv.Itoa(dc),
		"downloads_waiting", strconv.Itoa(dw),
		"lookups_active", strconv.Itoa(la),
		"lookups_capacity", strconv.Itoa(lc),
		"lookups_waiting", strconv.Itoa(lw),
		"archives_active", strconv.Itoa(aa),
		"archives_capacity", strconv.Itoa(ac),
		"archives_waiting", strconv.Itoa(aw),
		"active_users", strconv.Itoa(a.activeUserCount()))
	a.sendText(message.Chat.ID, text, "HTML", nil)
}

func (a *app) activeUserCount() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.activeUser) }

// handleIncomingBatch previews every link of a multi-link message and offers one format for all of them.
// Playlists are rejected as a whole; links that fail to preview are listed and skipped.
func (a *app) handleIncomingBatch(message *tgbotapi.Message, urls []string, lang string) {
	chatID, userID := message.Chat.ID, message.From.ID
	if len(urls) > maxBatchLinks {
		a.sendText(chatID, tr("batch_limit", lang, "max", strconv.Itoa(maxBatchLinks)), "HTML", nil)
		urls = urls[:maxBatchLinks]
	}
	status := a.sendText(chatID, tr("analyzing", lang), "HTML", nil)
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	_, release, err := a.lookups.acquire(ctx)
	if err != nil {
		if errors.Is(err, errQueueFull) && a.store != nil {
			a.store.increment(a.ctx, "queue_rejected")
		}
		a.handleQueueError(chatID, lang, err, errorReport{Stage: "batch_preview", UserID: userID})
		return
	}
	kept := make([]string, 0, len(urls))
	previews := make([]mediaPreview, 0, len(urls))
	var failed []string
	for _, rawURL := range urls {
		if requiresMusicResolution(rawURL) {
			failed = append(failed, tr("batch_link_failed", lang, "url", html.EscapeString(rawURL), "error", tr("batch_search_only", lang)))
			continue
		}
		linkCtx, linkCancel := context.WithTimeout(ctx, 20*time.Second)
		preview, previewErr := a.inspectURL(linkCtx, rawURL)
		linkCancel()
		if previewErr != nil {
			if ctx.Err() != nil {
				release()
				a.sendText(chatID, tr("preview_error", lang, "error", html.EscapeString(ctx.Err().Error())), "HTML", nil)
				return
			}
			a.reportError(errorReport{Stage: "batch_preview", ChatID: chatID, UserID: userID, URL: rawURL, Error: previewErr.Error()})
			failed = append(failed, tr("batch_link_failed", lang, "url", html.EscapeString(rawURL), "error", html.EscapeString(previewErr.Error())))
			continue
		}
		if preview.IsPlaylist {
			release()
			a.sendText(chatID, tr("batch_no_playlists", lang), "HTML", nil)
			return
		}
		kept = append(kept, rawURL)
		previews = append(previews, preview)
	}
	release()
	if len(kept) == 0 {
		a.sendText(chatID, strings.Join(append([]string{tr("nothing_found", lang)}, failed...), "\n"), "HTML", nil)
		return
	}
	batchPreview := mediaPreview{
		URL: kept[0], Title: tr("batch_title", lang, "count", strconv.Itoa(len(kept))),
		TrackCount: len(kept), Extractor: "batch",
	}
	for _, preview := range previews {
		batchPreview.DurationSeconds += preview.DurationSeconds
		batchPreview.Estimated128 += preview.Estimated128
		batchPreview.Estimated320 += preview.Estimated320
	}
	key, err := a.storeURL(pendingURL{URL: kept[0], ChatID: chatID, UserID: userID, Preview: batchPreview, Batch: kept, BatchPreviews: previews})
	if err != nil {
		log.Printf("save pending batch: %v", err)
		return
	}
	if format, quality, ok := a.getPreference(userID); ok {
		// The format keyboard is skipped, so the skipped-link report must be shown on its own:
		// the status message is reused for it instead of being deleted.
		if len(failed) > 0 {
			a.replaceStatusText(chatID, status, strings.Join(failed, "\n"))
		} else {
			a.deleteStatusMessage(status)
		}
		a.startDownload(userID, chatID, key, format, quality, lang, nil)
		return
	}
	text := batchPreviewText(previews, failed, lang)
	keyboard := formatKeyboard(key, lang)
	if status != nil {
		edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, text, *keyboard)
		edit.ParseMode = "HTML"
		if _, err := sendTelegram(a.bot, edit); err == nil {
			return
		}
	}
	a.sendText(chatID, text, "HTML", keyboard)
}

// replaceStatusText rewrites a status message with text (HTML) and falls back to a new message.
func (a *app) replaceStatusText(chatID int64, status *tgbotapi.Message, text string) {
	if status != nil {
		edit := tgbotapi.NewEditMessageText(status.Chat.ID, status.MessageID, text)
		edit.ParseMode = "HTML"
		if _, err := sendTelegram(a.bot, edit); err == nil {
			return
		}
	}
	a.sendText(chatID, text, "HTML", nil)
}

func batchPreviewText(previews []mediaPreview, failed []string, lang string) string {
	lines := []string{tr("batch_preview", lang, "count", strconv.Itoa(len(previews)))}
	for i, preview := range previews {
		lines = append(lines, strconv.Itoa(i+1)+". "+batchTrackLabel(preview))
	}
	lines = append(lines, failed...)
	lines = append(lines, tr("choose_format", lang))
	return strings.Join(lines, "\n")
}

func batchTrackLabel(preview mediaPreview) string {
	label := html.EscapeString(shortenRunes(firstNonEmpty(preview.Title, preview.URL, "Unknown"), 80))
	if preview.Artist != "" {
		label = html.EscapeString(shortenRunes(preview.Artist, 60)) + " — " + label
	}
	if preview.Duration != "" {
		label += " · " + html.EscapeString(preview.Duration)
	}
	return label
}

// downloadBatch processes pending.Batch sequentially inside one download slot. Cached tracks are
// re-sent by file_id, the rest are downloaded one by one; a failing link does not stop the batch.
func (a *app) downloadBatch(ctx context.Context, chatID int64, pending pendingURL, format, quality, lang string, status *tgbotapi.Message, reporter *statusReporter) (deliveryReport, error) {
	_, release, err := a.downloads.acquireNotify(ctx, func(position int) {
		if status != nil {
			a.editStatusMessage(status, tr("queued", lang, "position", strconv.Itoa(position)))
		}
	})
	if err != nil {
		if errors.Is(err, errQueueFull) && a.store != nil {
			a.store.increment(a.ctx, "queue_rejected")
		}
		return deliveryReport{}, err
	}
	// The download slot only covers yt-dlp work. Telegram delivery below runs after
	// releaseDownloads so cached re-sends, archive waits and uploads never block other users.
	released := false
	releaseDownloads := func() {
		if !released {
			released = true
			release()
		}
	}
	defer releaseDownloads()

	total := len(pending.Batch)
	report := deliveryReport{}
	var results []downloadResult
	var cached []pendingURL
	sessions := make(map[string]struct{})
	defer func() {
		for session := range sessions {
			a.downloader.clearSession(session)
		}
	}()
	for i, rawURL := range pending.Batch {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		preview := mediaPreview{URL: rawURL}
		if i < len(pending.BatchPreviews) {
			preview = pending.BatchPreviews[i]
		}
		reporter.stage(tr("batch_progress", lang, "current", strconv.Itoa(i+1), "total", strconv.Itoa(total), "title", html.EscapeString(shortenRunes(firstNonEmpty(preview.Title, rawURL), 80))))
		item := pendingURL{URL: rawURL, ChatID: pending.ChatID, UserID: pending.UserID, Preview: preview}
		if pending.Delivery != "zip" && a.hasCachedAudio(ctx, item, format, quality) {
			// Cached tracks are re-sent by file_id after the download slot is released.
			cached = append(cached, item)
			continue
		}
		linkResults, downloadErr := a.downloader.downloadRange(ctx, rawURL, format, quality, 0, 0, nil)
		for _, result := range linkResults {
			if result.Session != "" {
				sessions[result.Session] = struct{}{}
			}
		}
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if downloadErr != nil {
			log.Printf("Ошибка пакетной загрузки source=%s user_id=%d: %v", sourceHost(rawURL), pending.UserID, downloadErr)
			linkResults = []downloadResult{{Title: preview.Title, Artist: preview.Artist, Error: downloadErr.Error()}}
		}
		if len(linkResults) == 0 {
			linkResults = []downloadResult{{Title: preview.Title, Artist: preview.Artist, Error: tr("unknown_error", lang)}}
		}
		for _, result := range linkResults {
			if result.Error != "" {
				a.reportDownloadFailure(result.Error, sourceHost(rawURL))
			}
		}
		results = append(results, linkResults...)
	}
	releaseDownloads()
	if err := ctx.Err(); err != nil {
		return report, err
	}
	for _, item := range cached {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if delivered, handled := a.sendBatchCached(ctx, chatID, item, format, quality, lang); handled {
			if delivered {
				report.Delivered++
			} else {
				report.Failed++
			}
			continue
		}
		// The cache entry disappeared between the check and the send: report it as failed rather
		// than re-acquiring a download slot mid-delivery.
		report.Failed++
		results = append(results, downloadResult{Title: item.Preview.Title, Artist: item.Preview.Artist, Error: tr("history_expired", lang)})
	}
	if len(results) > 0 {
		if pending.Delivery == "zip" {
			_, releaseArchive, queueErr := a.archives.acquireNotify(ctx, func(position int) {
				a.editStatusMessage(status, tr("archive_queued", lang, "position", strconv.Itoa(position)))
			})
			if queueErr != nil {
				return report, queueErr
			}
			zipReport := a.sendResultsAsZIP(chatID, results, format, lang)
			releaseArchive()
			report.Delivered += zipReport.Delivered
			report.Failed += zipReport.Failed
			return report, nil
		}
		if status != nil {
			a.editStatusMessageFinal(status, tr("download_finished", lang))
		}
		reporter.stage(tr("stage_prepare", lang))
		fileReport := a.sendResultsIndividuallyWithSummary(chatID, results, format, lang, false, reporter)
		report.Delivered += fileReport.Delivered
		report.Failed += fileReport.Failed
	}
	summary := tr("all_sent_summary", lang, "sent", strconv.Itoa(report.Delivered), "total", strconv.Itoa(report.Delivered+report.Failed))
	if report.Failed > 0 {
		summary = tr("batch_partial", lang, "ok", strconv.Itoa(report.Delivered), "failed", strconv.Itoa(report.Failed))
	}
	a.sendText(chatID, summary, "HTML", nil)
	return report, nil
}

// hasCachedAudio reports whether a usable file_id cache entry exists for the item without sending it.
func (a *app) hasCachedAudio(ctx context.Context, item pendingURL, format, quality string) bool {
	if a.store == nil {
		return false
	}
	if source := sourceCacheKey(item.Preview.Extractor, item.Preview.SourceID, format, quality); source != "" {
		if _, ok := a.store.cachedAudio(ctx, source, a.cfg.CacheTTL); ok {
			return true
		}
	}
	_, ok := a.store.cachedAudio(ctx, generalCacheKey(item.URL, format, quality), a.cfg.CacheTTL)
	return ok
}

// sendBatchCached re-sends a batch link from the file_id cache. handled is false when there is no
// usable cache entry and the link must be downloaded.
func (a *app) sendBatchCached(ctx context.Context, chatID int64, item pendingURL, format, quality, lang string) (delivered, handled bool) {
	if a.store == nil {
		return false, false
	}
	keys := []string{}
	if source := sourceCacheKey(item.Preview.Extractor, item.Preview.SourceID, format, quality); source != "" {
		keys = append(keys, source)
	}
	keys = append(keys, generalCacheKey(item.URL, format, quality))
	for _, key := range keys {
		entry, ok := a.store.cachedAudio(ctx, key, a.cfg.CacheTTL)
		if !ok {
			continue
		}
		started := time.Now()
		err := a.sendCachedAudio(chatID, entry, lang)
		logMediaStage("telegram_file_id_send", "telegram", started, 0, err == nil, "media_size_bytes", entry.Size, "cache_hit", true, "format", entry.Format)
		if err == nil {
			a.store.increment(a.ctx, "cache_hits")
			return true, true
		}
		if invalidCachedFileError(err) {
			a.store.deleteCachedAudio(ctx, key)
			continue
		}
		a.reportError(errorReport{Stage: "cache_send", ChatID: chatID, UserID: item.UserID, URL: item.URL, Format: format + " " + quality, Error: err.Error()})
		a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return false, true
	}
	return false, false
}
