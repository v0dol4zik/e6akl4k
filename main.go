package main

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	maxFileSize          int64 = 50 * 1024 * 1024
	playlistZIPThreshold       = 10
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
}

type inlineResult struct {
	URL     string
	Title   string
	Channel string
}

type app struct {
	bot        *tgbotapi.BotAPI
	downloader *downloader
	semaphore  chan struct{}
	ctx        context.Context

	mu          sync.Mutex
	userLang    map[int64]string
	urls        map[string]string
	urlOrder    []string
	pendingZIP  map[string]zipRequest
	zipOrder    []string
	inline      map[string]inlineResult
	inlineOrder []string
}

func newApp(ctx context.Context, bot *tgbotapi.BotAPI, downloader *downloader) *app {
	return &app{
		bot:        bot,
		downloader: downloader,
		semaphore:  make(chan struct{}, maxParallelDownloads),
		ctx:        ctx,
		userLang:   make(map[int64]string),
		urls:       make(map[string]string),
		pendingZIP: make(map[string]zipRequest),
		inline:     make(map[string]inlineResult),
	}
}

func (a *app) handleUpdate(update tgbotapi.Update) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("Паника при обработке update %d: %v", update.UpdateID, recovered)
		}
	}()
	switch {
	case update.CallbackQuery != nil:
		a.handleCallback(update.CallbackQuery)
	case update.InlineQuery != nil:
		a.handleInlineQuery(update.InlineQuery)
	case update.ChosenInlineResult != nil:
		a.handleChosenInlineResult(update.ChosenInlineResult)
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
	key, err := a.storeURL(url)
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
	case data == "cancel":
		lang := a.langOrDefault(callback.From.ID)
		a.safeEdit(callback, tr("cancelled", lang), "", nil)
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
	url, ok := a.popURL(urlKey)
	if !ok {
		a.safeEdit(callback, tr("link_expired", lang), "", nil)
		return
	}
	status := a.safeEdit(callback, tr("download_starting", lang), "HTML", nil)

	results, err := a.runDownload(a.ctx, url, format, quality)
	if err != nil {
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
		key, err := a.storeZIP(zipRequest{Results: results, Format: format, ChatID: chatID})
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
	a.sendResultsIndividually(chatID, results, format, lang)
}

func (a *app) handleZIPChoice(callback *tgbotapi.CallbackQuery) {
	parts := strings.SplitN(callback.Data, ":", 3)
	if len(parts) != 3 {
		return
	}
	request, ok := a.popZIP(parts[2])
	lang := a.langOrDefault(callback.From.ID)
	if !ok {
		a.safeEdit(callback, tr("data_expired", lang), "", nil)
		return
	}
	if parts[1] == "yes" {
		a.sendResultsAsZIP(request.ChatID, request.Results, request.Format, lang)
	} else {
		a.sendResultsIndividually(request.ChatID, request.Results, request.Format, lang)
	}
}

func (a *app) runDownload(ctx context.Context, url, format, quality string) ([]downloadResult, error) {
	select {
	case a.semaphore <- struct{}{}:
		defer func() { <-a.semaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return a.downloader.download(ctx, url, format, quality)
}

func (a *app) runSearch(ctx context.Context, query string) ([]mediaInfo, error) {
	select {
	case a.semaphore <- struct{}{}:
		defer func() { <-a.semaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return a.downloader.search(ctx, query)
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

func (a *app) handleInlineQuery(query *tgbotapi.InlineQuery) {
	searchText := strings.TrimSpace(query.Query)
	if searchText == "" {
		a.answerInline(query.ID, nil, 1)
		return
	}
	items, err := a.runSearch(a.ctx, searchText)
	if err != nil {
		log.Printf("Ошибка inline-поиска: %v", err)
		a.answerInline(query.ID, nil, 5)
		return
	}
	lang := a.langOrDefault(query.From.ID)
	results := make([]interface{}, 0, len(items))
	for _, item := range items {
		title := firstNonEmpty(item.Title, tr("inline_no_title", lang))
		channel := firstNonEmpty(item.Channel, item.Uploader)
		id, err := randomID()
		if err != nil {
			continue
		}
		resultID := "yt_" + id + "_" + item.ID
		a.storeInline(resultID, inlineResult{
			URL:     "https://www.youtube.com/watch?v=" + item.ID,
			Title:   title,
			Channel: channel,
		})
		article := tgbotapi.NewInlineQueryResultArticle(resultID, title, tr("inline_downloading", lang, "title", title))
		article.Description = channel
		article.ThumbURL = "https://img.youtube.com/vi/" + item.ID + "/default.jpg"
		results = append(results, article)
	}
	a.answerInline(query.ID, results, 30)
}

func (a *app) handleChosenInlineResult(chosen *tgbotapi.ChosenInlineResult) {
	data, ok := a.getInline(chosen.ResultID)
	if !ok {
		return
	}
	lang := a.langOrDefault(chosen.From.ID)
	results, err := a.runDownload(a.ctx, data.URL, "mp3", "best")
	if err != nil || len(results) == 0 || results[0].Error != "" || results[0].FilePath == "" {
		if chosen.InlineMessageID != "" {
			a.editInline(chosen.InlineMessageID, tr("inline_failed", lang))
		}
		return
	}
	result := results[0]
	defer a.downloader.clearSession(result.Session)
	info, err := os.Stat(result.FilePath)
	if err != nil {
		return
	}
	if info.Size() > maxFileSize {
		if chosen.InlineMessageID != "" {
			a.editInline(chosen.InlineMessageID, tr("inline_too_big", lang))
		}
		return
	}
	if chosen.InlineMessageID != "" {
		a.editInline(chosen.InlineMessageID, tr("inline_ready", lang, "title", result.Title))
	}
	audio := tgbotapi.NewAudio(chosen.From.ID, tgbotapi.FilePath(result.FilePath))
	audio.Title = result.Title
	audio.Performer = result.Artist
	if _, err := a.bot.Send(audio); err != nil {
		log.Printf("Не удалось отправить inline-результат пользователю %d: %v", chosen.From.ID, err)
	}
}

func (a *app) answerInline(id string, results []interface{}, cacheTime int) {
	if results == nil {
		results = []interface{}{}
	}
	config := tgbotapi.InlineConfig{InlineQueryID: id, Results: results, CacheTime: cacheTime, IsPersonal: true}
	if _, err := a.bot.Request(config); err != nil {
		log.Printf("Не удалось ответить на inline-запрос: %v", err)
	}
}

func (a *app) editInline(inlineMessageID, text string) {
	config := tgbotapi.EditMessageTextConfig{
		BaseEdit: tgbotapi.BaseEdit{InlineMessageID: inlineMessageID},
		Text:     text,
	}
	if _, err := a.bot.Send(config); err != nil {
		log.Printf("Не удалось изменить inline-сообщение: %v", err)
	}
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

func (a *app) getLang(userID int64) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	lang, ok := a.userLang[userID]
	return lang, ok
}

func (a *app) langOrDefault(userID int64) string {
	if lang, ok := a.getLang(userID); ok {
		return lang
	}
	return defaultLang
}

func (a *app) setLang(userID int64, lang string) {
	a.mu.Lock()
	a.userLang[userID] = lang
	a.mu.Unlock()
}

func (a *app) storeURL(url string) (string, error) {
	key, err := randomID()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.urls[key] = url
	a.urlOrder = append(a.urlOrder, key)
	for len(a.urlOrder) > maxStoredEntries {
		delete(a.urls, a.urlOrder[0])
		a.urlOrder = a.urlOrder[1:]
	}
	return key, nil
}

func (a *app) popURL(key string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	url, ok := a.urls[key]
	delete(a.urls, key)
	return url, ok
}

func (a *app) storeZIP(request zipRequest) (string, error) {
	key, err := randomID()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.pendingZIP[key] = request
	a.zipOrder = append(a.zipOrder, key)
	var evicted []zipRequest
	for len(a.zipOrder) > maxStoredEntries {
		oldKey := a.zipOrder[0]
		a.zipOrder = a.zipOrder[1:]
		if old, ok := a.pendingZIP[oldKey]; ok {
			evicted = append(evicted, old)
		}
		delete(a.pendingZIP, oldKey)
	}
	a.mu.Unlock()
	for _, old := range evicted {
		if len(old.Results) > 0 {
			a.downloader.clearSession(old.Results[0].Session)
		}
	}
	time.AfterFunc(pendingZIPTTL, func() { a.expireZIP(key) })
	return key, nil
}

func (a *app) expireZIP(key string) {
	request, ok := a.popZIP(key)
	if ok && len(request.Results) > 0 {
		a.downloader.clearSession(request.Results[0].Session)
	}
}

func (a *app) popZIP(key string) (zipRequest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request, ok := a.pendingZIP[key]
	delete(a.pendingZIP, key)
	return request, ok
}

func (a *app) storeInline(key string, result inlineResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inline[key] = result
	a.inlineOrder = append(a.inlineOrder, key)
	for len(a.inlineOrder) > maxStoredEntries {
		delete(a.inline, a.inlineOrder[0])
		a.inlineOrder = a.inlineOrder[1:]
	}
	time.AfterFunc(2*time.Minute, func() {
		a.mu.Lock()
		delete(a.inline, key)
		a.mu.Unlock()
	})
}

func (a *app) getInline(key string) (inlineResult, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result, ok := a.inline[key]
	return result, ok
}

func languageKeyboard() *tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(languageOrder))
	for _, code := range languageOrder {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(languages[code], "setlang:"+code)))
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

func formatKeyboard(key, lang string) *tgbotapi.InlineKeyboardMarkup {
	buttons := [][2]string{
		{tr("btn_mp3_best", lang), "dl:mp3:best:" + key},
		{tr("btn_mp3_128", lang), "dl:mp3:128:" + key},
		{tr("btn_mp3_320", lang), "dl:mp3:320:" + key},
		{tr("btn_flac", lang), "dl:flac:best:" + key},
		{tr("btn_m4a", lang), "dl:m4a:best:" + key},
		{tr("btn_ogg", lang), "dl:ogg:best:" + key},
		{tr("btn_cancel", lang), "cancel"},
	}
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(buttons))
	for _, button := range buttons {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(button[0], button[1])))
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

func zipKeyboard(key, lang string) *tgbotapi.InlineKeyboardMarkup {
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_zip_yes", lang), "zip:yes:"+key)),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_zip_no", lang), "zip:no:"+key)),
	)
	return &markup
}

func detectURL(text string) string {
	rawURL := urlPattern.FindString(text)
	if rawURL == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(rawURL), "http://") && !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		rawURL = "https://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || !allowedHost(parsed.Hostname()) {
		return ""
	}
	return rawURL
}

func allowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range []string{"youtube.com", "youtu.be", "spotify.com", "soundcloud.com", "music.apple.com", "deezer.com", "tidal.com", "bandcamp.com", "vk.com", "ok.ru", "mixcloud.com", "audiomack.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	parts := strings.Split(host, ".")
	if len(parts) != 3 || parts[0] != "music" || parts[1] != "yandex" {
		return false
	}
	for _, tld := range []string{"ru", "by", "kz", "uz", "com"} {
		if parts[2] == tld {
			return true
		}
	}
	return false
}

func humanSize(size int64, lang string) string {
	units := sizeUnits[lang]
	if len(units) == 0 {
		units = sizeUnits[defaultLang]
	}
	value := float64(size)
	for _, unit := range units[:len(units)-1] {
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%.1f %s", value, units[len(units)-1])
}

func buildCaption(result downloadResult, size int64, format, lang string, index, total int) string {
	title := shortenRunes(firstNonEmpty(result.Title, strings.TrimSuffix(filepath.Base(result.FilePath), filepath.Ext(result.FilePath))), maxTitleLength)
	lines := []string{"<b>" + html.EscapeString(title) + "</b>"}
	if result.Artist != "" {
		lines = append(lines, "👤 "+html.EscapeString(shortenRunes(result.Artist, maxTitleLength)))
	}
	if result.Duration != "" {
		lines = append(lines, "⏱ "+html.EscapeString(result.Duration))
	}
	lines = append(lines, "📦 "+humanSize(size, lang)+" | "+strings.ToUpper(format))
	if total > 1 {
		lines = append(lines, fmt.Sprintf("[%d/%d]", index, total))
	}
	return strings.Join(lines, "\n")
}

func shortenRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func allFailed(results []downloadResult) bool {
	for _, result := range results {
		if result.Error == "" {
			return false
		}
	}
	return true
}

func createZIP(path string, results []downloadResult) (returnErr error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	writer := zip.NewWriter(file)
	defer func() {
		if err := writer.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()

	used := make(map[string]bool)
	for i, result := range results {
		extension := filepath.Ext(result.FilePath)
		name := result.Title
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(result.FilePath), extension)
		}
		if result.Artist != "" {
			name = result.Artist + " - " + name
		}
		name = unsafeName.ReplaceAllString(name+extension, "_")
		if used[name] {
			name = fmt.Sprintf("%02d - %s", i+1, name)
		}
		used[name] = true
		entry, err := writer.Create(name)
		if err != nil {
			return err
		}
		source, err := os.Open(result.FilePath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func loadEnv(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || os.Getenv(strings.TrimSpace(key)) != "" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		if err := os.Setenv(strings.TrimSpace(key), value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	if err := loadEnv(".env"); err != nil {
		log.Fatalf("Не удалось прочитать .env: %v", err)
	}
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		log.Fatal("BOT_TOKEN не задан в .env или переменных окружения")
	}

	dl, err := newDownloader("downloads", maxFileSize)
	if err != nil {
		log.Fatal(err)
	}
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("Не удалось подключиться к Telegram: %v", err)
	}
	if _, err := bot.Request(tgbotapi.DeleteWebhookConfig{DropPendingUpdates: true}); err != nil {
		log.Fatalf("Не удалось удалить webhook: %v", err)
	}
	log.Printf("Бот @%s запущен", bot.Self.UserName)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application := newApp(ctx, bot, dl)
	updates := bot.GetUpdatesChan(tgbotapi.UpdateConfig{Timeout: 60})
	var handlers sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			bot.StopReceivingUpdates()
			finished := make(chan struct{})
			go func() {
				handlers.Wait()
				close(finished)
			}()
			select {
			case <-finished:
			case <-time.After(30 * time.Second):
				log.Print("Не все обработчики успели завершиться за 30 секунд")
			}
			log.Print("Бот остановлен")
			return
		case update, ok := <-updates:
			if !ok {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				application.handleUpdate(update)
			}()
		}
	}
}
