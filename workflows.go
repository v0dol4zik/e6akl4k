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
		a.handleQueueError(message.Chat.ID, lang, err)
		return
	}
	preview, err := a.inspectURL(ctx, rawURL)
	release()
	if err != nil {
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
	candidates, outcome, err := a.runRankedLookup(ctx, query, expectedDuration)
	if err != nil {
		a.handleQueueError(chatID, lang, err)
		return
	}
	if a.store != nil {
		a.store.increment(a.ctx, "searches")
		switch {
		case outcome.Source == "octave":
			a.store.increment(a.ctx, "search_octave")
		case outcome.FallbackReason != "":
			a.store.increment(a.ctx, "search_youtube_fallback")
		}
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
	prefix := searchResultsHeader(outcome, resolved, lang)
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

// searchResultsHeader picks the results caption and appends an explicit notice when YouTube replaced Octave.
func searchResultsHeader(outcome searchOutcome, resolved bool, lang string) string {
	prefix := tr("search_results", lang)
	if resolved {
		prefix = tr("resolved_results", lang)
	}
	if outcome.Source != "youtube" {
		return prefix
	}
	switch outcome.FallbackReason {
	case "no_results":
		return prefix + "\n\n" + tr("search_fallback_no_results", lang)
	case "api_error":
		return prefix + "\n\n" + tr("search_fallback_unavailable", lang)
	}
	return prefix
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
		a.reportDownloadFailure(err.Error())
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
		if strings.EqualFold(format, "mp3") {
			if _, octaveURL := parseOctaveURL(pending.URL); octaveURL {
				reporter.stage(tr("stage_telegram_remote", a.langOrDefault(pending.UserID)))
			}
			if remote, ok, remoteErr := a.cacheOctaveRemoteMP3(ctx, pending, quality, urlKey, queued); remoteErr != nil {
				if errors.Is(remoteErr, context.Canceled) || errors.Is(remoteErr, context.DeadlineExceeded) {
					return cachedAudio{}, remoteErr
				}
				if errors.Is(remoteErr, errQueueFull) {
					if a.store != nil {
						a.store.increment(a.ctx, "queue_rejected")
					}
					return cachedAudio{}, remoteErr
				}
			} else if ok {
				return remote, nil
			}
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

func (a *app) cacheOctaveRemoteMP3(ctx context.Context, pending pendingURL, quality, urlKey string, queued func(int)) (cachedAudio, bool, error) {
	if _, ok := parseOctaveURL(pending.URL); !ok || a.downloader == nil || a.downloader.octave == nil {
		return cachedAudio{}, false, nil
	}
	if !a.octaveRemote.allow() {
		logMediaStage("remote_audio_prepare", "octave", time.Now(), 0, false, "eligible", false, "reason", "circuit_open", "quality", quality)
		return cachedAudio{}, false, nil
	}
	prepareStarted := time.Now()
	remote, ok, err := a.downloader.octaveRemoteMP3(ctx, pending.URL, pending.Preview, quality)
	logMediaStage("remote_audio_prepare", "octave", prepareStarted, remote.EstimatedSize, err == nil, "eligible", ok, "quality", quality)
	if err != nil {
		if countOctaveRemoteFailure(err) {
			a.octaveRemote.failure()
		} else {
			a.octaveRemote.cancelProbe()
		}
		return cachedAudio{}, false, err
	}
	if !ok {
		a.octaveRemote.cancelProbe()
		return cachedAudio{}, false, err
	}
	_, release, acquireErr := a.downloads.acquireNotify(ctx, queued)
	if acquireErr != nil {
		a.octaveRemote.cancelProbe()
		return cachedAudio{}, false, acquireErr
	}
	defer release()
	audio := tgbotapi.NewAudio(a.cfg.CacheChatID, tgbotapi.FileURL(remote.URL))
	audio.Title, audio.Performer, audio.Duration = remote.Title, remote.Artist, remote.Duration
	uploadStarted := time.Now()
	sent, sendErr := sendTelegram(a.bot, audio)
	size := remote.EstimatedSize
	if sent.Audio != nil && sent.Audio.FileSize > 0 {
		size = int64(sent.Audio.FileSize)
	}
	logMediaStage("telegram_upload", "telegram", uploadStarted, size, sendErr == nil && sent.Audio != nil, "mode", "remote_url", "format", "mp3")
	if sendErr != nil || sent.Audio == nil || sent.Audio.FileID == "" {
		failure := sendErr
		if failure == nil {
			failure = errors.New("Telegram не вернул file_id для Octave remote URL")
		}
		if countOctaveRemoteFailure(failure) {
			a.octaveRemote.failure()
		} else {
			a.octaveRemote.cancelProbe()
		}
		return cachedAudio{}, false, nil
	}
	a.octaveRemote.success()
	entry := cachedAudio{
		Key: urlKey, FileID: sent.Audio.FileID, Title: remote.Title, Artist: remote.Artist,
		Duration: secondsToHMS(remote.Duration), Format: "mp3", Quality: quality, Size: size, MediaType: "audio",
	}
	if err := a.storeCachedAudioAliases(ctx, entry, remote.CacheKey); err != nil {
		return cachedAudio{}, false, err
	}
	return entry, true, nil
}

func countOctaveRemoteFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errQueueFull) {
		return false
	}
	if apiErr, ok := telegramAPIError(err); ok && apiErr.Code == 429 {
		return false
	}
	return true
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

func (a *app) handleQueueError(chatID int64, lang string, err error) {
	if errors.Is(err, errQueueFull) {
		a.sendText(chatID, tr("queue_full", lang), "", nil)
		return
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
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
		"search_octave", strconv.FormatInt(stats.SearchOctave, 10),
		"search_fallback", strconv.FormatInt(stats.SearchYouTubeFallback, 10),
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

func (a *app) reportDownloadFailure(message string) {
	if a.store != nil {
		a.store.increment(a.ctx, "downloads_failed")
	}
	if !isCookieFailure(message) {
		return
	}
	if a.store != nil {
		a.store.increment(a.ctx, "youtube_cookie_errors")
	}
	a.mu.Lock()
	if time.Since(a.cookieAlertAt) < time.Hour {
		a.mu.Unlock()
		return
	}
	a.cookieAlertAt = time.Now()
	a.mu.Unlock()
	for _, adminID := range a.administratorIDs(a.ctx) {
		a.sendText(adminID, tr("admin_cookie_warning", a.langOrDefault(adminID)), "", nil)
	}
}

func isCookieFailure(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "cookies.txt") || strings.Contains(message, "not a bot") || strings.Contains(message, "подтверждения, что запрос не от бота")
}
