package main

import (
	"context"
	"errors"
	"html"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	maxFileSize          int64 = 50 * 1024 * 1024
	playlistZIPThreshold       = 10
	maxPlaylistTracks          = 75
	maxTitleLength             = 200
	maxParallelDownloads       = 2
	maxStoredEntries           = 5000
	pendingZIPTTL              = time.Hour
)

var (
	urlPattern = regexp.MustCompile(`(?i)(https?://)?(www\.)?(youtube\.com|youtu\.be|spotify\.com|soundcloud\.com|music\.apple\.com|deezer\.com|tidal\.com|bandcamp\.com|vk\.com|ok\.ru|music\.yandex\.|mixcloud\.com|audiomack\.com)[\w/\-?=&%.#+@!~]*`)
	unsafeName = regexp.MustCompile(`[<>:"/\\|?*]`)
)

type zipRequest struct {
	Results []downloadResult
	Format  string
	ChatID  int64
	UserID  int64
}

type pendingURL struct {
	URL    string
	ChatID int64
	UserID int64
}

type activeDownload struct {
	cancel context.CancelFunc
	chatID int64
	userID int64
}

type app struct {
	bot        *tgbotapi.BotAPI
	downloader *downloader
	inline     *inlineService
	semaphore  chan struct{}
	ctx        context.Context

	mu         sync.Mutex
	userLang   map[int64]string
	urls       map[string]pendingURL
	urlOrder   []string
	pendingZIP map[string]zipRequest
	zipOrder   []string
	active     map[string]activeDownload
}

func newApp(ctx context.Context, bot *tgbotapi.BotAPI, downloader *downloader) *app {
	return &app{
		bot:        bot,
		downloader: downloader,
		semaphore:  make(chan struct{}, maxParallelDownloads),
		ctx:        ctx,
		userLang:   make(map[int64]string),
		urls:       make(map[string]pendingURL),
		pendingZIP: make(map[string]zipRequest),
		active:     make(map[string]activeDownload),
	}
}

func (a *app) handleUpdate(update tgbotapi.Update) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("Паника при обработке update %d: %v", update.UpdateID, recovered)
		}
	}()
	switch {
	case update.InlineQuery != nil:
		a.handleInlineQuery(update.InlineQuery)
	case update.ChosenInlineResult != nil:
		a.handleChosenInlineResult(update.ChosenInlineResult)
	case update.CallbackQuery != nil:
		a.handleCallback(update.CallbackQuery)
	case update.Message != nil:
		a.handleMessage(update.Message)
	}
}

func (a *app) handleMessage(message *tgbotapi.Message) {
	if message.From == nil {
		return
	}
	userID := message.From.ID
	if message.IsCommand() {
		handled := true
		switch message.Command() {
		case "start":
			if lang, ok := a.getLang(userID); ok {
				a.sendText(message.Chat.ID, tr("welcome", lang), "HTML", nil)
			} else {
				a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
			}
		case "language":
			a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
		case "help":
			if lang, ok := a.getLang(userID); ok {
				a.sendText(message.Chat.ID, tr("help", lang), "", nil)
			} else {
				a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
			}
		default:
			handled = false
		}
		if handled {
			return
		}
	}
	if message.Text == "" {
		return
	}
	lang, ok := a.getLang(userID)
	if !ok {
		a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
		return
	}
	url := detectURL(message.Text)
	if url == "" {
		a.sendText(message.Chat.ID, tr("invalid_link", lang), "", nil)
		return
	}
	info := ""
	lowerURL := strings.ToLower(url)
	for _, marker := range []string{"playlist", "/sets/", "list=", "/album/"} {
		if strings.Contains(lowerURL, marker) {
			info = tr("looks_like_playlist", lang)
			break
		}
	}
	key, err := a.storeURL(pendingURL{URL: url, ChatID: message.Chat.ID, UserID: userID})
	if err != nil {
		log.Printf("Не удалось сохранить URL: %v", err)
		return
	}
	a.sendText(message.Chat.ID, tr("link_received", lang, "info", info), "HTML", formatKeyboard(key, lang))
}

func (a *app) handleCallback(callback *tgbotapi.CallbackQuery) {
	if callback.From == nil {
		return
	}
	if _, err := a.bot.Request(tgbotapi.NewCallback(callback.ID, "")); err != nil {
		log.Printf("Не удалось ответить на callback: %v", err)
	}
	data := callback.Data
	switch {
	case strings.HasPrefix(data, "inline_cancel:"):
		a.handleInlineCancel(callback)
	case strings.HasPrefix(data, "setlang:"):
		lang := strings.TrimPrefix(data, "setlang:")
		if _, ok := languages[lang]; !ok {
			return
		}
		_, existed := a.getLang(callback.From.ID)
		a.setLang(callback.From.ID, lang)
		a.safeEdit(callback, tr("language_changed", lang, "lang_name", languages[lang]), "HTML", nil)
		if !existed {
			a.sendText(callback.From.ID, tr("welcome", lang), "HTML", nil)
		}
	case strings.HasPrefix(data, "cancel:"):
		a.handlePendingCancel(callback)
	case strings.HasPrefix(data, "cancel_download:"):
		a.handleDownloadCancel(callback)
	case strings.HasPrefix(data, "zip:"):
		a.handleZIPChoice(callback)
	case strings.HasPrefix(data, "dl:"):
		a.handleDownload(callback)
	}
}

func (a *app) handleDownload(callback *tgbotapi.CallbackQuery) {
	parts := strings.SplitN(callback.Data, ":", 4)
	if len(parts) != 4 {
		return
	}
	format, quality, urlKey := parts[1], parts[2], parts[3]
	lang := a.langOrDefault(callback.From.ID)
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	pending, ok := a.popURL(urlKey, callback.From.ID, chatID)
	if !ok {
		a.sendText(callback.From.ID, tr("action_unavailable", lang), "", nil)
		return
	}
	url := pending.URL
	status := a.safeEdit(callback, tr("download_starting", lang), "HTML", nil)

	cancelKey, err := randomID()
	if err != nil {
		a.sendText(chatID, tr("download_error", lang, "error", "не удалось создать идентификатор загрузки"), "HTML", nil)
		return
	}
	downloadCtx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.active[cancelKey] = activeDownload{cancel: cancel, chatID: chatID, userID: callback.From.ID}
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		delete(a.active, cancelKey)
		a.mu.Unlock()
	}()
	if status != nil {
		edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, tr("download_starting", lang), *downloadCancelKeyboard(cancelKey, lang))
		edit.ParseMode = "HTML"
		if sent, sendErr := a.bot.Send(edit); sendErr == nil {
			status = &sent
		}
	}

	var trackStarted time.Time
	var lastProgress time.Time
	results, err := a.runDownload(downloadCtx, url, format, quality, func(current, total int) {
		if trackStarted.IsZero() {
			trackStarted = time.Now()
		}
		if status == nil || time.Since(lastProgress) < time.Second {
			return
		}
		lastProgress = time.Now()
		eta := tr("eta_calculating", lang)
		finished := current - 1
		if finished > 0 && current <= total {
			remaining := time.Duration(float64(time.Since(trackStarted)) * float64(total-current) / float64(finished))
			eta = formatETA(remaining, lang)
		}
		text := tr("download_progress", lang, "current", strconv.Itoa(current), "total", strconv.Itoa(total), "eta", eta)
		edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, text, *downloadCancelKeyboard(cancelKey, lang))
		edit.ParseMode = "HTML"
		if _, err := a.bot.Send(edit); err != nil {
			log.Printf("Не удалось обновить прогресс загрузки: %v", err)
		}
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		var tooLarge playlistTooLargeError
		if errors.As(err, &tooLarge) {
			a.sendText(chatID, tr("playlist_too_large", lang, "count", strconv.Itoa(tooLarge.Count), "limit", strconv.Itoa(tooLarge.Limit)), "", nil)
			return
		}
		log.Printf("Ошибка загрузки %s: %v", url, err)
		a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	if len(results) == 0 || allFailed(results) {
		reason := tr("unknown_error", lang)
		if len(results) > 0 && results[0].Error != "" {
			reason = results[0].Error
			a.downloader.clearSession(results[0].Session)
		}
		a.sendText(chatID, tr("nothing_downloaded", lang, "error", reason), "", nil)
		return
	}

	validCount := 0
	for _, result := range results {
		if result.Error == "" && regularFileExists(result.FilePath) {
			validCount++
		}
	}
	if validCount > playlistZIPThreshold {
		key, err := a.storeZIP(zipRequest{Results: results, Format: format, ChatID: chatID, UserID: callback.From.ID})
		if err != nil {
			a.downloader.clearSession(results[0].Session)
			a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
			return
		}
		text := tr("downloaded_count", lang, "count", strconv.Itoa(validCount))
		markup := zipKeyboard(key, lang)
		if status != nil {
			edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, text, *markup)
			edit.ParseMode = "HTML"
			if _, err := a.bot.Send(edit); err == nil {
				return
			}
		}
		a.sendText(chatID, text, "HTML", markup)
		return
	}
	if status != nil {
		edit := tgbotapi.NewEditMessageText(status.Chat.ID, status.MessageID, tr("download_finished", lang))
		edit.ParseMode = "HTML"
		if _, err := a.bot.Send(edit); err != nil {
			log.Printf("Не удалось обновить статус загрузки: %v", err)
		}
	}
	a.sendResultsIndividually(chatID, results, format, lang)
}

func (a *app) handleZIPChoice(callback *tgbotapi.CallbackQuery) {
	parts := strings.SplitN(callback.Data, ":", 3)
	if len(parts) != 3 {
		return
	}
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	request, ok := a.popZIP(parts[2], callback.From.ID, chatID)
	lang := a.langOrDefault(callback.From.ID)
	if !ok {
		a.sendText(callback.From.ID, tr("action_unavailable", lang), "", nil)
		return
	}
	if parts[1] == "yes" {
		a.sendResultsAsZIP(request.ChatID, request.Results, request.Format, lang)
	} else {
		a.sendResultsIndividually(request.ChatID, request.Results, request.Format, lang)
	}
}

func (a *app) runDownload(ctx context.Context, url, format, quality string, progress downloadProgress) ([]downloadResult, error) {
	select {
	case a.semaphore <- struct{}{}:
		defer func() { <-a.semaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return a.downloader.download(ctx, url, format, quality, progress)
}

func (a *app) runInlineLookup(ctx context.Context, query string) ([]inlineCandidate, error) {
	select {
	case a.semaphore <- struct{}{}:
		defer func() { <-a.semaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return a.downloader.inlineLookup(ctx, query)
}

func (a *app) handleDownloadCancel(callback *tgbotapi.CallbackQuery) {
	key := strings.TrimPrefix(callback.Data, "cancel_download:")
	lang := a.langOrDefault(callback.From.ID)
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	download, ok := a.getActiveDownload(key, callback.From.ID, chatID)
	if !ok {
		a.sendText(callback.From.ID, tr("download_already_finished", lang), "", nil)
		return
	}
	download.cancel()
	a.safeEdit(callback, tr("cancelled", lang), "", nil)
}

func (a *app) handlePendingCancel(callback *tgbotapi.CallbackQuery) {
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	lang := a.langOrDefault(callback.From.ID)
	key := strings.TrimPrefix(callback.Data, "cancel:")
	if _, ok := a.popURL(key, callback.From.ID, chatID); !ok {
		a.sendText(callback.From.ID, tr("action_unavailable", lang), "", nil)
		return
	}
	a.safeEdit(callback, tr("cancelled", lang), "", nil)
}

func (a *app) sendResultsIndividually(chatID int64, results []downloadResult, format, lang string) {
	session := ""
	if len(results) > 0 {
		session = results[0].Session
	}
	defer a.downloader.clearSession(session)

	sent := 0
	for i, result := range results {
		idx, total := strconv.Itoa(i+1), strconv.Itoa(len(results))
		if result.Error != "" {
			a.sendText(chatID, tr("send_error", lang, "idx", idx, "total", total, "error", html.EscapeString(result.Error)), "HTML", nil)
			continue
		}
		info, err := os.Stat(result.FilePath)
		if err != nil || !info.Mode().IsRegular() {
			a.sendText(chatID, tr("file_not_found", lang, "idx", idx, "total", total, "name", filepath.Base(result.FilePath)), "", nil)
			continue
		}
		if info.Size() > maxFileSize {
			a.sendText(chatID, tr("file_too_big", lang, "idx", idx, "total", total, "title", html.EscapeString(shortenRunes(result.Title, maxTitleLength)), "size", humanSize(info.Size(), lang)), "HTML", nil)
			continue
		}
		audio := tgbotapi.NewAudio(chatID, tgbotapi.FilePath(result.FilePath))
		audio.Caption = buildCaption(result, info.Size(), format, lang, i+1, len(results))
		audio.ParseMode = "HTML"
		audio.Title = firstNonEmpty(result.Title, strings.TrimSuffix(filepath.Base(result.FilePath), filepath.Ext(result.FilePath)))
		audio.Performer = result.Artist
		if _, err := a.bot.Send(audio); err != nil {
			log.Printf("Не удалось отправить %s: %v", result.FilePath, err)
			a.sendText(chatID, tr("send_failed", lang, "idx", idx, "total", total, "error", html.EscapeString(err.Error())), "HTML", nil)
			continue
		}
		sent++
	}
	summary := tr("all_sent_summary", lang, "sent", strconv.Itoa(sent), "total", strconv.Itoa(len(results)))
	if sent < len(results) {
		summary += tr("some_failed_suffix", lang)
	}
	a.sendText(chatID, summary, "", nil)
}

func (a *app) sendResultsAsZIP(chatID int64, results []downloadResult, format, lang string) {
	session := ""
	if len(results) > 0 {
		session = results[0].Session
	}
	defer a.downloader.clearSession(session)

	valid := make([]downloadResult, 0, len(results))
	var totalSize int64
	for _, result := range results {
		if result.Error == "" && regularFileExists(result.FilePath) {
			valid = append(valid, result)
			if info, err := os.Stat(result.FilePath); err == nil {
				totalSize += info.Size()
			}
		}
	}
	if len(valid) == 0 {
		a.sendText(chatID, tr("no_files_for_zip", lang), "", nil)
		return
	}
	if totalSize > maxFileSize {
		a.sendText(chatID, tr("zip_too_big", lang, "size", humanSize(totalSize, lang)), "", nil)
		return
	}
	a.sendText(chatID, tr("zipping", lang, "count", strconv.Itoa(len(valid))), "", nil)

	zipID, err := randomID()
	if err != nil {
		a.sendText(chatID, tr("zip_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	zipPath := filepath.Join(filepath.Dir(valid[0].FilePath), "playlist_"+zipID+".zip")
	if err := createZIP(zipPath, valid); err != nil {
		log.Printf("Ошибка создания ZIP: %v", err)
		a.sendText(chatID, tr("zip_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	info, err := os.Stat(zipPath)
	if err != nil {
		a.sendText(chatID, tr("zip_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	if info.Size() > maxFileSize {
		a.sendText(chatID, tr("zip_too_big", lang, "size", humanSize(info.Size(), lang)), "", nil)
		return
	}
	skipped := len(results) - len(valid)
	skippedLine := ""
	if skipped > 0 {
		skippedLine = tr("zip_caption_skipped", lang, "skipped", strconv.Itoa(skipped))
	}
	document := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(zipPath))
	document.Caption = tr("zip_caption", lang, "count", strconv.Itoa(len(valid)), "size", humanSize(info.Size(), lang), "fmt", strings.ToUpper(format), "skipped", skippedLine)
	document.ParseMode = "HTML"
	if _, err := a.bot.Send(document); err != nil {
		a.sendText(chatID, tr("zip_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	a.sendText(chatID, tr("zip_sent", lang), "", nil)
}

func (a *app) sendText(chatID int64, text, parseMode string, markup *tgbotapi.InlineKeyboardMarkup) *tgbotapi.Message {
	message := tgbotapi.NewMessage(chatID, text)
	message.ParseMode = parseMode
	if markup != nil {
		message.ReplyMarkup = *markup
	}
	sent, err := a.bot.Send(message)
	if err != nil {
		log.Printf("Не удалось отправить сообщение в чат %d: %v", chatID, err)
		return nil
	}
	return &sent
}

func (a *app) safeEdit(callback *tgbotapi.CallbackQuery, text, parseMode string, markup *tgbotapi.InlineKeyboardMarkup) *tgbotapi.Message {
	if callback.Message != nil && callback.Message.Chat != nil {
		config := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
		config.ParseMode = parseMode
		config.ReplyMarkup = markup
		message, err := a.bot.Send(config)
		if err == nil {
			return &message
		}
		log.Printf("Не удалось изменить сообщение: %v", err)
	}
	return a.sendText(callback.From.ID, text, parseMode, markup)
}
