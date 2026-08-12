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
		a.presentSearchResults(message.Chat.ID, message.From.ID, query, lang, status, true)
		return
	}
	key, err := a.storeURL(pendingURL{URL: rawURL, ChatID: message.Chat.ID, UserID: message.From.ID, Preview: preview})
	if err != nil {
		log.Printf("save pending URL: %v", err)
		return
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
		if _, err := a.bot.Send(edit); err == nil {
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
	a.presentSearchResults(message.Chat.ID, message.From.ID, query, lang, status, false)
}

func (a *app) presentSearchResults(chatID, userID int64, query, lang string, status *tgbotapi.Message, resolved bool) {
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	candidates, err := a.runInlineLookup(ctx, query)
	if err != nil {
		a.handleQueueError(chatID, lang, err)
		return
	}
	if a.store != nil {
		a.store.increment(a.ctx, "searches")
	}
	keys := make([]string, 0, len(candidates))
	for _, item := range candidates {
		seconds := inlineDurationSeconds(item.Duration)
		sourceID := ""
		parts := strings.Split(item.CacheKey, ":")
		if len(parts) >= 2 {
			sourceID = parts[1]
		}
		preview := mediaPreview{URL: item.URL, Title: item.Title, Artist: item.Artist, Duration: item.Duration, DurationSeconds: seconds, TrackCount: 1, SourceID: sourceID, Extractor: "youtube"}
		key, keyErr := a.storeURL(pendingURL{URL: item.URL, ChatID: chatID, UserID: userID, Preview: preview})
		if keyErr == nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		a.sendText(chatID, tr("nothing_found", lang), "", nil)
		return
	}
	prefix := tr("search_results", lang)
	if resolved {
		prefix = tr("resolved_results", lang)
	}
	keyboard := searchKeyboard(keys, candidates, lang)
	if status != nil {
		edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, prefix, *keyboard)
		edit.ParseMode = "HTML"
		if _, err := a.bot.Send(edit); err == nil {
			return
		}
	}
	a.sendText(chatID, prefix, "HTML", keyboard)
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
	case "75":
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

func (a *app) tryCachedDownload(ctx context.Context, chatID int64, pending pendingURL, format, quality, lang string, queued func(int)) (bool, bool) {
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
			if err := a.sendCachedAudio(chatID, entry, lang); err == nil {
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
	entry, err := a.ensureCachedAudio(ctx, pending, format, quality, queued)
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
	if err := a.sendCachedAudio(chatID, entry, lang); err != nil {
		if invalidCachedFileError(err) {
			a.store.deleteCachedAudio(ctx, entry.Key)
			return false, false
		}
		a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return true, false
	}
	a.store.increment(a.ctx, "downloads_ok")
	return true, true
}

func (a *app) ensureCachedAudio(ctx context.Context, pending pendingURL, format, quality string, queued func(int)) (cachedAudio, error) {
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
		audio := tgbotapi.NewAudio(a.cfg.CacheChatID, tgbotapi.FilePath(result.FilePath))
		audio.Title, audio.Performer = result.Title, result.Artist
		sent, sendErr := a.bot.Send(audio)
		if sendErr != nil || sent.Audio == nil {
			return cachedAudio{}, firstError(sendErr, errors.New("Telegram не вернул audio file_id"))
		}
		entry := cachedAudio{Key: urlKey, FileID: sent.Audio.FileID, Title: result.Title, Artist: result.Artist, Duration: result.Duration, Format: format, Quality: quality, Size: info.Size()}
		if putErr := a.store.putCachedAudio(ctx, entry); putErr != nil {
			return cachedAudio{}, putErr
		}
		if result.CacheKey != "" {
			alias := entry
			alias.Key = result.CacheKey
			_ = a.store.putCachedAudio(ctx, alias)
		}
		return entry, nil
	})
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
	audio := tgbotapi.NewAudio(chatID, tgbotapi.FileID(entry.FileID))
	audio.Title, audio.Performer = entry.Title, entry.Artist
	audio.Caption = buildCaption(result, entry.Size, entry.Format, lang, 1, 1)
	audio.ParseMode = "HTML"
	_, err := a.bot.Send(audio)
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
	_, _ = a.bot.Send(edit)
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

func (a *app) isAdmin(userID int64) bool { return a.cfg.AdminIDs[userID] }

func (a *app) handleAdminStats(message *tgbotapi.Message) {
	if !a.isAdmin(message.From.ID) || a.store == nil {
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
	if !a.isAdmin(message.From.ID) {
		return
	}
	lang := a.langOrDefault(message.From.ID)
	da, dw, dc := a.downloads.snapshot()
	la, lw, lc := a.lookups.snapshot()
	text := tr("admin_status", lang,
		"downloads_active", strconv.Itoa(da),
		"downloads_capacity", strconv.Itoa(dc),
		"downloads_waiting", strconv.Itoa(dw),
		"lookups_active", strconv.Itoa(la),
		"lookups_capacity", strconv.Itoa(lc),
		"lookups_waiting", strconv.Itoa(lw),
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
	for adminID := range a.cfg.AdminIDs {
		a.sendText(adminID, tr("admin_cookie_warning", a.langOrDefault(adminID)), "", nil)
	}
}

func isCookieFailure(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "cookies.txt") || strings.Contains(message, "not a bot") || strings.Contains(message, "подтверждения, что запрос не от бота")
}
