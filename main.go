package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	maxFileSize int64 = 50 * 1024 * 1024
	// localMaxFileSize is the upload limit of a local Telegram Bot API server in --local mode.
	localMaxFileSize     int64 = 2000 * 1024 * 1024
	mediaGroupMaxBytes         = 10 * maxFileSize
	playlistZIPThreshold       = 10
	// maxPlaylistTracks is the default cap on the tracks of one request, and
	// maxPlaylistTracksCeiling the highest MAX_PLAYLIST_TRACKS; a longer playlist is reachable
	// through the range buttons, and any selection is downloaded in bounded batches.
	maxPlaylistTracks        = 200
	maxPlaylistTracksCeiling = 1000
	// playlistBatchTracks and playlistZIPBatchTracks are the batch sizes of individual and ZIP
	// playlist delivery: each batch takes its own download slot and at most two are on disk. A ZIP
	// batch is also capped at about playlistZIPBatchBytes of average tracks of averageTrackSeconds,
	// so a lossless one holds fewer tracks (see playlistZIPBatchSize).
	playlistBatchTracks    = 10
	playlistZIPBatchTracks = 50
	playlistZIPBatchBytes  = 700 << 20
	averageTrackSeconds    = 240
	maxTitleLength         = 200
	maxParallelDownloads   = 7
	maxStoredEntries       = 5000
	pendingURLTTL          = time.Hour
	// maxBatchLinks caps how many links from one message are downloaded together.
	maxBatchLinks = 5
)

var (
	urlPattern = regexp.MustCompile(`(?i)(https?://)?(www\.)?(youtube\.com|youtu\.be|spotify\.com|soundcloud\.com|music\.apple\.com|deezer\.com|tidal\.com|bandcamp\.com|vk\.com|ok\.ru|music\.yandex\.|mixcloud\.com|audiomack\.com)[\w/\-?=&%.#+@!~]*`)
	unsafeName = regexp.MustCompile(`[<>:"/\\|?*]`)
)

type deliveryReport struct {
	Delivered int
	Failed    int
	// TooLarge lists the failed tracks that did not fit the Telegram upload limit; they are
	// answered with lighter formats instead of an error.
	TooLarge []downloadResult
}

func (r *deliveryReport) add(other deliveryReport) {
	r.Delivered += other.Delivered
	r.Failed += other.Failed
	r.TooLarge = append(r.TooLarge, other.TooLarge...)
}

type pendingURL struct {
	URL        string
	ChatID     int64
	UserID     int64
	Preview    mediaPreview
	RangeStart int
	RangeEnd   int
	Delivery   string
	ExpiresAt  time.Time
	// Batch holds every link of a multi-link message; BatchPreviews mirrors it index by index.
	Batch         []string
	BatchPreviews []mediaPreview
}

type activeDownload struct {
	cancel context.CancelFunc
	chatID int64
	userID int64
}

type app struct {
	bot           *tgbotapi.BotAPI
	downloader    *downloader
	inline        *inlineService
	ctx           context.Context
	cfg           config
	store         *store
	downloads     *jobGate
	lookups       *jobGate
	archives      *jobGate
	disk          *diskBudget
	limiter       *rateLimiter
	inlineLimiter *rateLimiter
	flights       flightGroup
	metrics       metricsCache
	errorReports  *errorReporter
	// cookieLoginCheck confirms a suspected cookie failure before administrators are alerted.
	cookieLoginCheck func(context.Context) (cookieLogin, string)

	mu           sync.Mutex
	userLang     map[int64]string
	userPref     map[int64]userPreference
	urls         map[string]pendingURL
	urlOrder     []string
	active       map[string]activeDownload
	activeUser   map[int64]bool
	cookieAlerts cookieAlertState
	notices      map[string]pendingNotice
	broadcasting bool
}

func newApp(ctx context.Context, bot *tgbotapi.BotAPI, downloader *downloader) *app {
	cfg := config{DownloadWorkers: maxParallelDownloads, LookupWorkers: 2, LookupQueueSize: 40, RateLimit: 12, RateWindow: time.Minute, CacheTTL: 180 * 24 * time.Hour, MaxFileSize: maxFileSize, MaxPlaylistTracks: maxPlaylistTracks}
	return newAppWithServices(ctx, bot, downloader, nil, cfg)
}

func newAppWithServices(ctx context.Context, bot *tgbotapi.BotAPI, downloader *downloader, state *store, cfg config) *app {
	if cfg.DownloadWorkers <= 0 {
		cfg.DownloadWorkers = maxParallelDownloads
	}
	if cfg.LookupWorkers <= 0 {
		cfg.LookupWorkers = 2
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 12
	}
	if cfg.InlineRateLimit <= 0 {
		cfg.InlineRateLimit = 60
	}
	if cfg.RateWindow <= 0 {
		cfg.RateWindow = time.Minute
	}
	application := &app{
		bot:           bot,
		downloader:    downloader,
		ctx:           ctx,
		cfg:           cfg,
		store:         state,
		downloads:     newJobGate(cfg.DownloadWorkers, cfg.DownloadQueueSize),
		lookups:       newJobGate(cfg.LookupWorkers, cfg.LookupQueueSize),
		archives:      newJobGate(cfg.ArchiveWorkers, cfg.ArchiveQueueSize),
		limiter:       newRateLimiter(cfg.RateLimit, cfg.RateWindow),
		inlineLimiter: newRateLimiter(cfg.InlineRateLimit, cfg.RateWindow),
		userLang:      make(map[int64]string),
		userPref:      make(map[int64]userPreference),
		urls:          make(map[string]pendingURL),
		active:        make(map[string]activeDownload),
		activeUser:    make(map[int64]bool),
	}
	if downloader != nil {
		application.cookieLoginCheck = downloader.checkCookieLogin
		application.disk = newDiskBudget(func() (int64, error) { return diskFree(downloader.downloadDir) }, cfg.DiskWarningBytes)
	}
	return application
}

func (a *app) handleUpdate(update tgbotapi.Update) (success bool) {
	if a.ctx.Err() != nil {
		return false
	}
	success = true
	defer func() {
		if recovered := recover(); recovered != nil {
			success = false
			log.Printf("Паника при обработке update %d: %v\n%s", update.UpdateID, recovered, debug.Stack())
		}
		if a.ctx.Err() != nil {
			success = false
		}
	}()
	a.observeTelegramUsers(update)
	if a.rejectBannedUpdate(update) {
		return true
	}
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
	return success
}

func (a *app) handleMessage(message *tgbotapi.Message) {
	if message.From == nil {
		return
	}
	userID := message.From.ID
	if message.IsCommand() {
		if a.handleAdminCommand(message) {
			return
		}
		handled := true
		switch message.Command() {
		case "start":
			if lang, ok := a.getLang(userID); ok {
				a.sendText(message.Chat.ID, a.guideText("welcome", lang), "HTML", nil)
			} else {
				a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
			}
		case "language":
			a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
		case "help":
			if lang, ok := a.getLang(userID); ok {
				a.sendText(message.Chat.ID, a.guideText("help", lang), "HTML", nil)
			} else {
				a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
			}
		case "settings":
			if lang, ok := a.getLang(userID); ok {
				a.sendText(message.Chat.ID, a.settingsText(userID, lang), "HTML", settingsKeyboard(lang))
			} else {
				a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
			}
		case "history":
			if lang, ok := a.getLang(userID); ok {
				a.sendHistory(message.Chat.ID, userID, lang)
			} else {
				a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
			}
		case "export", "cover":
			if lang, ok := a.getLang(userID); ok {
				a.handleExportCommand(message, lang, message.Command() == "cover")
			} else {
				a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
			}
		case "lastfm":
			if lang, ok := a.getLang(userID); ok {
				a.handleLastfmCommand(message, lang)
			} else {
				a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
			}
		case "notify":
			if lang, ok := a.getLang(userID); ok {
				a.handleNotifyCommand(message, lang)
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
	if message.Audio != nil && message.Chat.IsPrivate() {
		a.handleAudioSearch(message)
		return
	}
	if message.Text == "" {
		return
	}
	lang, ok := a.getLang(userID)
	if !ok {
		a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
		return
	}
	if allowed, retry := a.limiter.allow(userID); !allowed {
		if a.store != nil {
			a.store.increment(a.ctx, "rate_limited")
		}
		a.sendText(message.Chat.ID, tr("rate_limited", lang, "seconds", strconv.Itoa(int(retry.Seconds())+1)), "", nil)
		return
	}
	urls := detectURLs(message.Text, maxBatchLinks+1)
	if len(urls) >= 2 {
		a.handleIncomingBatch(message, urls, lang)
		return
	}
	if len(urls) == 1 {
		a.handleIncomingURL(message, urls[0], lang)
		return
	}
	if message.Chat.IsPrivate() {
		if tracks := pastedTracklist(message.Text); tracks != nil {
			a.presentPastedTracklist(message, tracks, lang)
			return
		}
		a.handlePrivateSearch(message, message.Text, lang)
		return
	}
	a.sendText(message.Chat.ID, a.guideText("invalid_link", lang), "HTML", nil)
}

// handleAudioSearch turns a forwarded audio file into a title search using its
// performer/title tags or, failing that, its file name.
func (a *app) handleAudioSearch(message *tgbotapi.Message) {
	userID := message.From.ID
	lang, ok := a.getLang(userID)
	if !ok {
		a.sendText(message.Chat.ID, chooseLanguageText, "", languageKeyboard())
		return
	}
	if allowed, retry := a.limiter.allow(userID); !allowed {
		if a.store != nil {
			a.store.increment(a.ctx, "rate_limited")
		}
		a.sendText(message.Chat.ID, tr("rate_limited", lang, "seconds", strconv.Itoa(int(retry.Seconds())+1)), "", nil)
		return
	}
	query := audioSearchQuery(message.Audio)
	if query == "" {
		a.sendText(message.Chat.ID, tr("audio_no_metadata", lang), "", nil)
		return
	}
	status := a.sendText(message.Chat.ID, tr("searching_by_audio", lang), "HTML", nil)
	a.presentSearchResults(message.Chat.ID, userID, query, lang, status, false, message.Audio.Duration)
}

// audioSearchQuery builds a search string from audio tags, falling back to the
// file name without its extension. It returns "" when nothing usable is present.
func audioSearchQuery(audio *tgbotapi.Audio) string {
	if audio == nil {
		return ""
	}
	query := strings.TrimSpace(strings.TrimSpace(audio.Performer) + " " + strings.TrimSpace(audio.Title))
	if query != "" {
		return query
	}
	name := strings.TrimSpace(audio.FileName)
	return strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name)))
}

func (a *app) handleCallback(callback *tgbotapi.CallbackQuery) {
	if callback.From == nil {
		return
	}
	if _, err := requestTelegram(a.bot, tgbotapi.NewCallback(callback.ID, "")); err != nil {
		log.Printf("Не удалось ответить на callback: %v", err)
	}
	data := callback.Data
	switch {
	case strings.HasPrefix(data, supportNoticeCallback):
		a.handleSupportNoticeDismiss(callback)
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
			a.sendText(callback.From.ID, a.guideText("welcome", lang), "HTML", nil)
		}
	case data == cookieCheckCallback:
		a.handleCookieCheck(callback)
	case strings.HasPrefix(data, "pref:"):
		a.handlePreferenceChoice(callback)
	case strings.HasPrefix(data, "hist:"):
		a.handleHistoryChoice(callback)
	case strings.HasPrefix(data, "cancel:"):
		a.handlePendingCancel(callback)
	case strings.HasPrefix(data, "cancel_download:"):
		a.handleDownloadCancel(callback)
	case strings.HasPrefix(data, "delivery:"):
		a.handleDeliveryChoice(callback)
	case strings.HasPrefix(data, "pick:"):
		a.handleSearchPick(callback)
	case strings.HasPrefix(data, "range:"):
		a.handleRangeChoice(callback)
	case strings.HasPrefix(data, "dl:"):
		a.handleDownload(callback)
	case strings.HasPrefix(data, "export:"):
		a.handleExportCallback(callback)
	case strings.HasPrefix(data, "cover:"):
		a.handleCoverCallback(callback)
	case strings.HasPrefix(data, "lfm:"):
		a.handleLastfmCallback(callback)
	case strings.HasPrefix(data, "lfs:"):
		a.handleLastfmPick(callback)
	case strings.HasPrefix(data, notifyCallback):
		a.handleNotifyCallback(callback)
	case strings.HasPrefix(data, noticeConfirmCallback):
		a.handleNoticeConfirm(callback)
	case strings.HasPrefix(data, errorReportCallback):
		a.handleErrorReportCallback(callback)
	}
}

func (a *app) handleDownload(callback *tgbotapi.CallbackQuery) {
	parts := strings.SplitN(callback.Data, ":", 4)
	if len(parts) != 4 {
		return
	}
	format, quality, urlKey := parts[1], parts[2], parts[3]
	if !validDownloadOption(format, quality) {
		return
	}
	lang := a.langOrDefault(callback.From.ID)
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	a.startDownload(callback.From.ID, chatID, urlKey, format, quality, lang, callback)
}

// startDownload runs the download flow for a stored pending URL. When callback is nil
// (a default format was applied without a button press) status updates go to a fresh
// message instead of editing the callback message.
func (a *app) startDownload(userID, chatID int64, urlKey, format, quality, lang string, callback *tgbotapi.CallbackQuery) {
	if !validDownloadOption(format, quality) {
		return
	}
	showStatus := func(text, parseMode string, markup *tgbotapi.InlineKeyboardMarkup) *tgbotapi.Message {
		if callback != nil {
			return a.safeEdit(callback, text, parseMode, markup)
		}
		return a.sendText(chatID, text, parseMode, markup)
	}
	if pending, ok := a.getURL(urlKey, userID, chatID); ok && needsDeliveryChoice(pending) {
		prompt := tr("choose_delivery", lang)
		if len(pending.Batch) > 0 {
			prompt = tr("choose_delivery_batch", lang, "count", strconv.Itoa(len(pending.Batch)))
		} else {
			prompt += "\n\n" + selectionEstimate(pending, format, quality, lang)
		}
		showStatus(prompt, "HTML", deliveryKeyboard(urlKey, format, quality, lang))
		return
	}
	if !a.beginUserDownload(userID) {
		a.sendText(chatID, tr("user_download_active", lang), "", nil)
		return
	}
	defer a.finishUserDownload(userID)
	pending, ok := a.popURL(urlKey, userID, chatID)
	if !ok {
		a.sendText(userID, tr("action_unavailable", lang), "", nil)
		return
	}
	url := pending.URL
	historyStarted := time.Now()
	historyStatus := "failed"
	historyError := ""
	historyKey := ""
	if !pending.Preview.IsPlaylist && len(pending.Batch) == 0 {
		historyKey = sourceCacheKey(pending.Preview.Extractor, pending.Preview.SourceID, format, quality)
		if historyKey == "" {
			historyKey = generalCacheKey(url, format, quality)
		}
	}
	starting := a.appliedPreferenceHint(userID, format, quality, lang) + tr("download_starting", lang)
	status := showStatus(starting, "HTML", nil)

	cancelKey, err := randomID()
	if err != nil {
		a.reportError(errorReport{Stage: "download", ChatID: chatID, UserID: userID, URL: url, Error: "randomID: " + err.Error()})
		a.sendText(chatID, tr("download_error", lang, "error", tr("internal_id_error", lang)), "HTML", nil)
		return
	}
	downloadCtx, cancel := context.WithCancel(a.ctx)
	defer func() {
		if a.store != nil {
			a.store.recordDownload(a.ctx, userID, sourceHost(url), format+":"+quality, historyStatus, time.Since(historyStarted), historyError, historyKey, pending.Preview.Title, pending.Preview.Artist)
		}
		if historyStatus == "delivered" || historyStatus == "partial" {
			a.deleteStatusMessage(status)
			a.maybeSendSupportNotice(chatID, userID, lang)
		}
		if historyStatus == "too_large" {
			a.deleteStatusMessage(status)
		}
	}()
	a.mu.Lock()
	a.active[cancelKey] = activeDownload{cancel: cancel, chatID: chatID, userID: userID}
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		delete(a.active, cancelKey)
		a.mu.Unlock()
	}()
	if status != nil {
		edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, starting, *downloadCancelKeyboard(cancelKey, lang))
		edit.ParseMode = "HTML"
		if sent, sendErr := sendTelegram(a.bot, edit); sendErr == nil {
			status = &sent
		}
	}
	reporter := newStatusReporter(downloadCtx, a, status, lang, cancelKey)
	defer reporter.close()
	downloadCtx = withStatusReporter(downloadCtx, reporter)

	var trackStarted time.Time
	var lastProgress time.Time
	if len(pending.Batch) > 0 {
		report, batchErr := a.downloadBatch(downloadCtx, chatID, pending, format, quality, lang, status, reporter)
		historyStatus = deliveryStatus(report)
		historyError = deliveryError(report)
		if batchErr != nil {
			historyError = batchErr.Error()
			if errors.Is(batchErr, context.Canceled) || downloadCtx.Err() != nil {
				historyStatus = "cancelled"
				if a.store != nil {
					a.store.increment(a.ctx, "downloads_cancelled")
				}
				return
			}
			if errors.Is(batchErr, errQueueFull) {
				a.restoreURL(urlKey, pending)
				showStatus(tr("queue_full", lang), "", formatKeyboard(urlKey, lang))
				return
			}
			a.handleQueueError(chatID, lang, batchErr, errorReport{Stage: "batch", UserID: userID, Format: format + " " + quality})
			return
		}
		a.recordDeliveryMetrics(report)
		a.offerLighterFormats(chatID, userID, report.TooLarge, format, quality, pending.Delivery, lang, nil)
		return
	}
	if !pending.Preview.IsPlaylist {
		reporter.stage(tr("stage_cache", lang))
		if handled, succeeded, tooLarge := a.tryCachedDownload(downloadCtx, chatID, pending, format, quality, lang, func(position int) {
			if status != nil {
				a.editStatusMessage(status, tr("queued", lang, "position", strconv.Itoa(position)))
			}
		}, reporter); handled {
			if succeeded {
				historyStatus = "delivered"
				a.recordDeliveryMetrics(deliveryReport{Delivered: 1})
			}
			if tooLarge {
				historyStatus = "too_large"
			}
			if !succeeded && downloadCtx.Err() != nil {
				historyStatus = "cancelled"
			}
			if status != nil && succeeded {
				a.editStatusMessageFinal(status, tr("download_finished", lang))
			}
			return
		}
	}
	if batchedPlaylistDelivery(pending, format, quality) {
		var report deliveryReport
		var batchErr error
		if pending.Delivery == "zip" {
			report, batchErr = a.downloadAndSendPlaylistZIPs(downloadCtx, chatID, pending, format, quality, lang, reporter)
		} else {
			report, batchErr = a.downloadAndSendPlaylistBatches(downloadCtx, chatID, pending, format, quality, lang, status, reporter)
		}
		historyStatus = deliveryStatus(report)
		historyError = deliveryError(report)
		a.recordDeliveryMetrics(report)
		if batchErr == nil {
			a.offerLighterFormats(chatID, userID, report.TooLarge, format, quality, pending.Delivery, lang, nil)
		}
		if batchErr != nil {
			historyError = batchErr.Error()
			if errors.Is(batchErr, context.Canceled) {
				historyStatus = "cancelled"
				if a.store != nil {
					a.store.increment(a.ctx, "downloads_cancelled")
				}
				return
			}
			if errors.Is(batchErr, errQueueFull) {
				if report.Delivered == 0 {
					a.restoreURL(urlKey, pending)
					showStatus(tr("queue_full", lang), "", formatKeyboard(urlKey, lang))
				} else {
					a.sendText(chatID, tr("queue_full", lang), "", nil)
				}
				return
			}
			if errors.Is(batchErr, errDeliveryReported) {
				return
			}
			if errors.Is(batchErr, errDiskFull) {
				a.reportError(errorReport{Stage: "disk", ChatID: chatID, UserID: userID, URL: url, Format: format + " " + quality, Error: batchErr.Error()})
				if report.Delivered+report.Failed > 0 {
					a.sendText(chatID, tr("disk_busy_partial", lang, "sent", strconv.Itoa(report.Delivered)), "", nil)
				} else {
					a.restoreURL(urlKey, pending)
					showStatus(tr("disk_busy", lang), "", formatKeyboard(urlKey, lang))
				}
				return
			}
			a.reportDownloadFailure(batchErr.Error(), sourceHost(url))
			a.reportError(errorReport{Stage: "playlist", ChatID: chatID, UserID: userID, URL: url, Format: format + " " + quality, Error: batchErr.Error()})
			a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(batchErr.Error())), "HTML", nil)
		}
		return
	}
	reporter.stage(tr("stage_source", lang))
	results, err := a.runDownloadRangeQueued(downloadCtx, url, format, quality, pending.RangeStart, pending.RangeEnd, func(position int) {
		if status != nil {
			a.editStatusMessage(status, tr("queued", lang, "position", strconv.Itoa(position)))
		}
	}, func(current, total int) {
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
		if _, err := sendTelegram(a.bot, edit); err != nil {
			log.Printf("Не удалось обновить прогресс загрузки: %v", err)
		}
	})
	if err != nil {
		historyError = err.Error()
		if errors.Is(err, context.Canceled) {
			historyStatus = "cancelled"
			if a.store != nil {
				a.store.increment(a.ctx, "downloads_cancelled")
			}
			return
		}
		if errors.Is(err, errQueueFull) {
			a.restoreURL(urlKey, pending)
			showStatus(tr("queue_full", lang), "", formatKeyboard(urlKey, lang))
			return
		}
		var tooLarge playlistTooLargeError
		if errors.As(err, &tooLarge) {
			a.sendText(chatID, tr("playlist_too_large", lang, "count", strconv.Itoa(tooLarge.Count), "limit", strconv.Itoa(tooLarge.Limit)), "", nil)
			return
		}
		log.Printf("Ошибка загрузки source=%s user_id=%d: %v", sourceHost(url), userID, err)
		a.reportDownloadFailure(err.Error(), sourceHost(url))
		a.reportError(errorReport{Stage: "download", ChatID: chatID, UserID: userID, URL: url, Format: format + " " + quality, Error: err.Error()})
		a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	if len(results) == 0 || allFailed(results) {
		a.downloader.clearSessions(results)
		oversized := tooLargeResults(results)
		if len(results) > 0 && len(oversized) == len(results) {
			historyStatus = "too_large"
			a.offerLighterFormats(chatID, userID, oversized, format, quality, pending.Delivery, lang, singleRequest(pending))
			return
		}
		reason := tr("unknown_error", lang)
		for _, result := range results {
			if result.Error != "" && !result.TooLarge {
				reason = result.Error
				break
			}
		}
		a.reportDownloadFailure(reason, sourceHost(url))
		a.reportError(errorReport{Stage: "download", ChatID: chatID, UserID: userID, URL: url, Format: format + " " + quality, Error: reason})
		historyError = reason
		a.sendText(chatID, tr("nothing_downloaded", lang, "error", reason), "", nil)
		a.offerLighterFormats(chatID, userID, oversized, format, quality, pending.Delivery, lang, nil)
		return
	}
	validCount := 0
	for _, result := range results {
		if result.Error == "" && regularFileExists(result.FilePath) {
			validCount++
		}
	}
	if validCount > playlistZIPThreshold && pending.Delivery == "zip" {
		_, releaseArchive, queueErr := a.archives.acquireNotify(downloadCtx, func(position int) {
			a.editStatusMessage(status, tr("archive_queued", lang, "position", strconv.Itoa(position)))
		})
		if queueErr != nil {
			historyError = queueErr.Error()
			a.downloader.clearSessions(results)
			a.restoreURL(urlKey, pending)
			a.handleQueueError(chatID, lang, queueErr, errorReport{Stage: "archive", UserID: userID, URL: url, Format: format + " " + quality})
			return
		}
		defer releaseArchive()
		report := a.sendResultsAsZIP(chatID, results, format, lang)
		historyStatus = deliveryStatus(report)
		historyError = deliveryError(report)
		a.recordDeliveryMetrics(report)
		a.offerLighterFormats(chatID, userID, report.TooLarge, format, quality, pending.Delivery, lang, singleRequest(pending))
		return
	}
	if status != nil {
		a.editStatusMessageFinal(status, tr("download_finished", lang))
	}
	reporter.stage(tr("stage_prepare", lang))
	report := a.sendResultsIndividually(chatID, results, format, lang, reporter)
	historyStatus = deliveryStatus(report)
	historyError = deliveryError(report)
	a.recordDeliveryMetrics(report)
	a.offerLighterFormats(chatID, userID, report.TooLarge, format, quality, pending.Delivery, lang, singleRequest(pending))
}

// singleRequest returns the request itself when it is a single track, so that the lighter-format
// buttons reuse it; a playlist or a batch is retried by the links of its oversized tracks.
func singleRequest(pending pendingURL) *pendingURL {
	if pending.Preview.IsPlaylist || len(pending.Batch) > 0 {
		return nil
	}
	return &pending
}

func (a *app) downloadAndSendPlaylistBatches(ctx context.Context, chatID int64, pending pendingURL, format, quality, lang string, status *tgbotapi.Message, reporter *statusReporter) (deliveryReport, error) {
	report, err := a.downloadPlaylistBatches(ctx, pending, format, quality, lang, reporter, playlistBatchTracks, func(_ playlistPart, results []downloadResult) (deliveryReport, error) {
		return a.sendResultsIndividuallyWithSummary(chatID, results, format, lang, false, reporter), nil
	})
	if err != nil {
		return report, err
	}
	a.sendPlaylistSummary(chatID, report, lang)
	return report, nil
}

// downloadAndSendPlaylistZIPs delivers a long playlist selection as archives batch by batch, so
// its files never pile up on disk; each batch waits for an archive slot of its own.
func (a *app) downloadAndSendPlaylistZIPs(ctx context.Context, chatID int64, pending pendingURL, format, quality, lang string, reporter *statusReporter) (deliveryReport, error) {
	report, err := a.downloadPlaylistBatches(ctx, pending, format, quality, lang, reporter, playlistZIPBatchSize(format, quality), func(batch playlistPart, results []downloadResult) (deliveryReport, error) {
		_, releaseArchive, err := a.archives.acquireNotify(ctx, func(position int) {
			reporter.stage(tr("archive_queued", lang, "position", strconv.Itoa(position)))
		})
		if err != nil {
			a.downloader.clearSessions(results)
			return deliveryReport{}, err
		}
		defer releaseArchive()
		reporter.stage(tr("zip_batch", lang, "start", strconv.Itoa(batch.start), "end", strconv.Itoa(batch.end), "total", strconv.Itoa(batch.last)))
		return a.sendZIPArchives(chatID, results, format, lang, &batch)
	})
	if err != nil {
		return report, err
	}
	a.sendPlaylistSummary(chatID, report, lang)
	return report, nil
}

// downloadPlaylistBatches downloads the selected tracks of a playlist in batches of size tracks
// and hands each batch to deliver while the next one downloads, so at most two batches are on
// disk at once. Every batch first reserves its estimated disk space, then waits for a download
// slot of its own; the space is released once the batch is delivered and removed.
func (a *app) downloadPlaylistBatches(ctx context.Context, pending pendingURL, format, quality, lang string, reporter *statusReporter, size int, deliver func(playlistPart, []downloadResult) (deliveryReport, error)) (deliveryReport, error) {
	start, end := pending.RangeStart, pending.RangeEnd
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > pending.Preview.TrackCount {
		end = pending.Preview.TrackCount
	}
	type playlistBatch struct {
		tracks  playlistPart
		results []downloadResult
		err     error
		release func()
	}
	pipelineCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pipelineCtx = withStatusReporter(pipelineCtx, reporter)
	batches := make(chan playlistBatch)
	go func() {
		defer close(batches)
		for batchStart := start; batchStart <= end; batchStart += size {
			batchEnd := min(batchStart+size-1, end)
			part := playlistPart{start: batchStart, end: batchEnd, last: end}
			release, err := a.disk.acquire(pipelineCtx, diskBatchFactor*estimateSelectionBytes(pending.Preview, batchStart, batchEnd, format, quality), func() {
				reporter.stage(tr("disk_wait", lang, "start", strconv.Itoa(batchStart), "end", strconv.Itoa(batchEnd), "total", strconv.Itoa(end)))
			})
			if err != nil {
				select {
				case batches <- playlistBatch{tracks: part, err: err, release: func() {}}:
				case <-pipelineCtx.Done():
				}
				return
			}
			reporter.stage(tr("download_batch", lang, "start", strconv.Itoa(batchStart), "end", strconv.Itoa(batchEnd), "total", strconv.Itoa(end)))
			results, err := a.downloadPlaylistRange(pipelineCtx, pending, format, quality, lang, batchStart, batchEnd, reporter)
			batch := playlistBatch{tracks: part, results: results, err: err, release: release}
			select {
			case batches <- batch:
			case <-pipelineCtx.Done():
				a.downloader.clearSessions(results)
				release()
				return
			}
			if err != nil {
				return
			}
		}
	}()
	report := deliveryReport{}
	for batch := range batches {
		if batch.err != nil {
			batch.release()
			return report, batch.err
		}
		delivered, err := deliver(batch.tracks, batch.results)
		batch.release()
		report.add(delivered)
		if err != nil {
			return report, err
		}
		if err := pipelineCtx.Err(); err != nil {
			return report, err
		}
	}
	return report, nil
}

func (a *app) sendPlaylistSummary(chatID int64, report deliveryReport, lang string) {
	summary := tr("all_sent_summary", lang, "sent", strconv.Itoa(report.Delivered), "total", strconv.Itoa(report.Delivered+report.Failed))
	if report.Failed > 0 {
		summary += tr("some_failed_suffix", lang)
	}
	a.sendText(chatID, summary, "", nil)
}

// batchedPlaylistDelivery reports whether a playlist selection is downloaded and sent in
// batches: individual files beyond one media group, archives beyond one ZIP batch, and every
// selection of a searched tracklist, which only the batch pipeline can download.
func batchedPlaylistDelivery(pending pendingURL, format, quality string) bool {
	if !pending.Preview.IsPlaylist || len(pending.Batch) > 0 {
		return false
	}
	if searchedTracklist(pending.Preview) {
		return true
	}
	switch pending.Delivery {
	case "individual":
		return selectedTrackCount(pending) > playlistZIPThreshold
	case "zip":
		return selectedTrackCount(pending) > playlistZIPBatchSize(format, quality)
	}
	return false
}

func deliveryStatus(report deliveryReport) string {
	if report.Delivered > 0 && report.Failed == 0 {
		return "delivered"
	}
	if report.Delivered > 0 {
		return "partial"
	}
	if report.Failed > 0 && report.Failed == len(report.TooLarge) {
		return "too_large"
	}
	return "delivery_failed"
}

func deliveryError(report deliveryReport) string {
	if report.Failed == 0 {
		return ""
	}
	return fmt.Sprintf("не доставлено треков: %d", report.Failed)
}

func (a *app) recordDeliveryMetrics(report deliveryReport) {
	if a.store == nil {
		return
	}
	switch deliveryStatus(report) {
	case "delivered":
		a.store.increment(a.ctx, "downloads_ok")
	case "partial":
		a.store.increment(a.ctx, "downloads_partial")
	case "delivery_failed":
		a.store.increment(a.ctx, "downloads_failed")
	}
}

// needsDeliveryChoice reports whether the user must pick ZIP or individual delivery before the
// download starts: large playlist selections and every multi-link batch (a batch is at most
// maxBatchLinks links, so it never reaches playlistZIPThreshold and gets its own rule).
func needsDeliveryChoice(pending pendingURL) bool {
	if pending.Delivery != "" {
		return false
	}
	if len(pending.Batch) > 0 {
		return len(pending.Batch) > 1
	}
	return pending.Preview.IsPlaylist && selectedTrackCount(pending) > playlistZIPThreshold
}

// selectionEstimate tells the size of a playlist selection in the chosen format before it is
// downloaded, and suggests MP3 for a long lossless one.
func selectionEstimate(pending pendingURL, format, quality, lang string) string {
	start := max(pending.RangeStart, 1)
	end := start + selectedTrackCount(pending) - 1
	count := strconv.Itoa(selectedTrackCount(pending))
	text := tr("selection_estimate", lang, "size", humanSize(estimateSelectionBytes(pending.Preview, start, end, format, quality), lang), "format", formatName(format, quality), "count", count)
	if strings.EqualFold(format, "flac") && selectedTrackCount(pending) > playlistZIPBatchTracks {
		text += "\n" + tr("selection_estimate_lossless", lang, "size", humanSize(estimateSelectionBytes(pending.Preview, start, end, "mp3", "320"), lang))
	}
	return text
}

func selectedTrackCount(pending pendingURL) int {
	if len(pending.Batch) > 0 {
		return len(pending.Batch)
	}
	if !pending.Preview.IsPlaylist {
		return 1
	}
	start, end := pending.RangeStart, pending.RangeEnd
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > pending.Preview.TrackCount {
		end = pending.Preview.TrackCount
	}
	if end < start {
		return 0
	}
	return end - start + 1
}

// handlePreferenceChoice stores "pref:<format>:<quality>" or "pref:ask" as the user's default.
func (a *app) handlePreferenceChoice(callback *tgbotapi.CallbackQuery) {
	lang := a.langOrDefault(callback.From.ID)
	format, quality := "", ""
	if callback.Data != "pref:ask" {
		parts := strings.SplitN(callback.Data, ":", 3)
		if len(parts) != 3 || !validDownloadOption(parts[1], parts[2]) {
			return
		}
		format, quality = parts[1], parts[2]
	}
	if err := a.setPreference(callback.From.ID, format, quality); err != nil {
		log.Printf("Не удалось сохранить настройки user_id=%d: %v", callback.From.ID, err)
		a.safeEdit(callback, tr("download_error", lang, "error", tr("unknown_error", lang)), "HTML", nil)
		return
	}
	a.safeEdit(callback, tr("settings_saved", lang, "value", preferenceLabel(format, quality, lang)), "HTML", nil)
}

const historyLimit = 10

// sendHistory lists the user's recent cached downloads with one re-delivery button per item.
func (a *app) sendHistory(chatID, userID int64, lang string) {
	var items []historyItem
	if a.store != nil {
		var err error
		if items, err = a.store.recentDownloads(a.ctx, userID, historyLimit); err != nil {
			log.Printf("Не удалось прочитать историю user_id=%d: %v", userID, err)
		}
	}
	if len(items) == 0 {
		a.sendText(chatID, tr("history_empty", lang), "HTML", nil)
		return
	}
	a.sendText(chatID, historyText(items, lang), "HTML", historyKeyboard(items, lang))
}

// handleHistoryChoice re-sends a cached track from "hist:<id>" or clears the history on "hist:clear".
// History never starts a new download: an expired cache entry only asks for the link again.
func (a *app) handleHistoryChoice(callback *tgbotapi.CallbackQuery) {
	if a.store == nil {
		return
	}
	userID := callback.From.ID
	lang := a.langOrDefault(userID)
	chatID := userID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	payload := strings.TrimPrefix(callback.Data, "hist:")
	if payload == "clear" {
		if err := a.store.clearHistory(a.ctx, userID); err != nil {
			log.Printf("Не удалось очистить историю user_id=%d: %v", userID, err)
			a.safeEdit(callback, tr("download_error", lang, "error", tr("unknown_error", lang)), "HTML", nil)
			return
		}
		a.safeEdit(callback, tr("history_cleared", lang), "HTML", nil)
		return
	}
	id, err := strconv.ParseInt(payload, 10, 64)
	if err != nil || id <= 0 {
		return
	}
	item, ok := a.store.historyEntry(a.ctx, userID, id)
	if !ok || item.CacheKey == "" {
		return
	}
	entry, ok := a.store.cachedAudio(a.ctx, item.CacheKey, a.cfg.CacheTTL)
	if !ok {
		a.sendText(chatID, tr("history_expired", lang), "HTML", nil)
		return
	}
	if err := a.sendCachedAudio(chatID, entry, lang); err != nil {
		if invalidCachedFileError(err) {
			a.store.deleteCachedAudio(a.ctx, item.CacheKey)
			a.sendText(chatID, tr("history_expired", lang), "HTML", nil)
			return
		}
		a.reportError(errorReport{Stage: "history_send", ChatID: chatID, UserID: userID, Format: entry.Format, Error: err.Error()})
		a.sendText(chatID, tr("download_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	a.store.increment(a.ctx, "cache_hits")
}

func (a *app) settingsText(userID int64, lang string) string {
	format, quality, _ := a.getPreference(userID)
	return tr("settings_title", lang) + "\n" + tr("settings_current", lang, "value", preferenceLabel(format, quality, lang))
}

// appliedPreferenceHint returns a status line when the download option matches the user's default.
func (a *app) appliedPreferenceHint(userID int64, format, quality, lang string) string {
	if prefFormat, prefQuality, ok := a.getPreference(userID); ok && prefFormat == format && prefQuality == quality {
		return tr("settings_applied_hint", lang, "value", preferenceLabel(format, quality, lang)) + "\n"
	}
	return ""
}

func (a *app) handleDeliveryChoice(callback *tgbotapi.CallbackQuery) {
	parts := strings.SplitN(callback.Data, ":", 5)
	if len(parts) != 5 || (parts[1] != "zip" && parts[1] != "individual") || !validDownloadOption(parts[2], parts[3]) {
		return
	}
	chatID := callback.From.ID
	if callback.Message != nil && callback.Message.Chat != nil {
		chatID = callback.Message.Chat.ID
	}
	if _, ok := a.setURLDelivery(parts[4], callback.From.ID, chatID, parts[1]); !ok {
		a.sendText(callback.From.ID, tr("action_unavailable", a.langOrDefault(callback.From.ID)), "", nil)
		return
	}
	callback.Data = "dl:" + parts[2] + ":" + parts[3] + ":" + parts[4]
	a.handleDownload(callback)
}

func (a *app) runDownload(ctx context.Context, url, format, quality string, progress downloadProgress) ([]downloadResult, error) {
	return a.runDownloadRange(ctx, url, format, quality, 0, 0, progress)
}

// runRankedLookup performs a YouTube text search and ranks the candidates.
func (a *app) runRankedLookup(ctx context.Context, query string, expectedDuration int) ([]inlineCandidate, error) {
	_, release, err := a.lookups.acquire(ctx)
	if err != nil {
		if errors.Is(err, errQueueFull) && a.store != nil {
			a.store.increment(a.ctx, "queue_rejected")
		}
		return nil, err
	}
	defer release()
	return a.downloader.searchLookup(ctx, query, expectedDuration)
}

func (a *app) runDownloadRange(ctx context.Context, url, format, quality string, start, end int, progress downloadProgress) ([]downloadResult, error) {
	return a.runDownloadRangeQueued(ctx, url, format, quality, start, end, nil, progress)
}

func (a *app) runDownloadRangeQueued(ctx context.Context, url, format, quality string, start, end int, queued func(int), progress downloadProgress) ([]downloadResult, error) {
	_, release, err := a.downloads.acquireNotify(ctx, queued)
	if err != nil {
		if errors.Is(err, errQueueFull) && a.store != nil {
			a.store.increment(a.ctx, "queue_rejected")
		}
		return nil, err
	}
	defer release()
	return a.downloader.downloadRange(ctx, url, format, quality, start, end, progress)
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

func (a *app) sendResultsIndividually(chatID int64, results []downloadResult, format, lang string, reporters ...*statusReporter) deliveryReport {
	return a.sendResultsIndividuallyWithSummary(chatID, results, format, lang, true, reporters...)
}

func (a *app) sendResultsIndividuallyWithSummary(chatID int64, results []downloadResult, format, lang string, showSummary bool, reporters ...*statusReporter) deliveryReport {
	var reporter *statusReporter
	if len(reporters) > 0 {
		reporter = reporters[0]
	}
	defer a.downloader.clearSessions(results)
	failures := deliveryFailures{total: len(results)}
	defer func() { a.reportDeliveryFailures("send", chatID, format, failures) }()

	type batchAudio struct {
		index  int
		result downloadResult
		size   int64
	}
	items := make([]batchAudio, 0, len(results))
	var tooLarge []downloadResult
	for i, result := range results {
		idx, total := strconv.Itoa(i+1), strconv.Itoa(len(results))
		if result.TooLarge {
			tooLarge = append(tooLarge, result)
			continue
		}
		if result.Error != "" {
			failures.add(i+1, result.Error)
			a.sendText(chatID, tr("send_error", lang, "idx", idx, "total", total, "error", html.EscapeString(result.Error)), "HTML", nil)
			continue
		}
		info, err := os.Stat(result.FilePath)
		if err != nil || !info.Mode().IsRegular() {
			failures.add(i+1, "file not found: "+filepath.Base(result.FilePath))
			a.sendText(chatID, tr("file_not_found", lang, "idx", idx, "total", total, "name", filepath.Base(result.FilePath)), "", nil)
			continue
		}
		if info.Size() > a.fileLimit() {
			result.TooLarge, result.Size = true, info.Size()
			tooLarge = append(tooLarge, result)
			continue
		}
		items = append(items, batchAudio{index: i, result: result, size: info.Size()})
	}
	if !telegramAudioFormat(format) {
		sent := 0
		for _, item := range items {
			progress := newUploadBatchProgress(reporter, item.size)
			document := tgbotapi.NewDocument(chatID, progressFile{path: item.result.FilePath, progress: progress})
			document.Caption = buildCaption(item.result, item.size, format, lang, item.index+1, len(results))
			document.ParseMode = "HTML"
			message, err := sendTelegram(a.bot, document)
			if err != nil {
				failures.add(item.index+1, err.Error())
				a.sendText(chatID, tr("send_failed", lang, "idx", strconv.Itoa(item.index+1), "total", strconv.Itoa(len(results)), "error", html.EscapeString(err.Error())), "HTML", nil)
				continue
			}
			a.cacheDeliveredAudio(item.result, format, item.size, message)
			sent++
		}
		summary := tr("all_sent_summary", lang, "sent", strconv.Itoa(sent), "total", strconv.Itoa(len(results)))
		if sent < len(results) {
			summary += tr("some_failed_suffix", lang)
		}
		if showSummary {
			a.sendText(chatID, summary, "", nil)
		}
		return deliveryReport{Delivered: sent, Failed: len(results) - sent, TooLarge: tooLarge}
	}
	sent := 0
	sizes := make([]int64, len(items))
	for i, item := range items {
		sizes[i] = item.size
	}
	for start := 0; start < len(items); {
		end := mediaGroupEnd(sizes, start)
		batch := items[start:end]
		start = end
		var batchSize int64
		for _, item := range batch {
			batchSize += item.size
		}
		progress := newUploadBatchProgress(reporter, batchSize)
		media := make([]interface{}, 0, len(batch))
		for _, item := range batch {
			input := tgbotapi.NewInputMediaAudio(progressFile{path: item.result.FilePath, progress: progress})
			input.Caption = buildCaption(item.result, item.size, format, lang, item.index+1, len(results))
			input.ParseMode = "HTML"
			input.Title = firstNonEmpty(item.result.Title, strings.TrimSuffix(filepath.Base(item.result.FilePath), filepath.Ext(item.result.FilePath)))
			input.Performer = item.result.Artist
			input.Duration = inlineDurationSeconds(item.result.Duration)
			media = append(media, input)
		}
		var delivered []tgbotapi.Message
		var sendErr error
		if len(batch) == 1 {
			audio := tgbotapi.NewAudio(chatID, progressFile{path: batch[0].result.FilePath, progress: progress})
			audio.Caption, audio.ParseMode = buildCaption(batch[0].result, batch[0].size, format, lang, batch[0].index+1, len(results)), "HTML"
			audio.Title, audio.Performer = batch[0].result.Title, batch[0].result.Artist
			message, err := sendTelegram(a.bot, audio)
			sendErr = err
			delivered = []tgbotapi.Message{message}
		} else {
			delivered, sendErr = sendMediaGroupTelegram(a.bot, tgbotapi.NewMediaGroup(chatID, media))
		}
		if sendErr != nil {
			log.Printf("Не удалось отправить группу аудио: %v", sendErr)
			if !canFallbackToIndividual(sendErr) {
				for _, item := range batch {
					failures.add(item.index+1, sendErr.Error())
					a.sendText(chatID, tr("send_failed", lang, "idx", strconv.Itoa(item.index+1), "total", strconv.Itoa(len(results)), "error", html.EscapeString(sendErr.Error())), "HTML", nil)
				}
				continue
			}
			for _, item := range batch {
				fallbackProgress := newUploadBatchProgress(reporter, item.size)
				audio := tgbotapi.NewAudio(chatID, progressFile{path: item.result.FilePath, progress: fallbackProgress})
				audio.Title, audio.Performer = item.result.Title, item.result.Artist
				audio.Caption, audio.ParseMode = buildCaption(item.result, item.size, format, lang, item.index+1, len(results)), "HTML"
				message, err := sendTelegram(a.bot, audio)
				if err != nil {
					failures.add(item.index+1, err.Error())
					a.sendText(chatID, tr("send_failed", lang, "idx", strconv.Itoa(item.index+1), "total", strconv.Itoa(len(results)), "error", html.EscapeString(err.Error())), "HTML", nil)
					continue
				}
				delivered = []tgbotapi.Message{message}
				a.cacheDeliveredAudio(item.result, format, item.size, delivered[0])
				sent++
			}
			continue
		}
		for i, message := range delivered {
			if i < len(batch) {
				a.cacheDeliveredAudio(batch[i].result, format, batch[i].size, message)
				sent++
			}
		}
	}
	summary := tr("all_sent_summary", lang, "sent", strconv.Itoa(sent), "total", strconv.Itoa(len(results)))
	if sent < len(results) {
		summary += tr("some_failed_suffix", lang)
	}
	if showSummary {
		a.sendText(chatID, summary, "", nil)
	}
	return deliveryReport{Delivered: sent, Failed: len(results) - sent, TooLarge: tooLarge}
}

func (a *app) cacheDeliveredAudio(result downloadResult, format string, size int64, delivered tgbotapi.Message) {
	if a.store == nil || result.CacheKey == "" {
		return
	}
	entry := cachedAudio{Key: result.CacheKey, Title: result.Title, Artist: result.Artist, Duration: result.Duration, Format: format, Size: size}
	if delivered.Audio != nil {
		entry.FileID, entry.MediaType = delivered.Audio.FileID, "audio"
	} else if delivered.Document != nil {
		entry.FileID, entry.MediaType = delivered.Document.FileID, "document"
	}
	if entry.FileID != "" {
		_ = a.store.putCachedAudio(a.ctx, entry)
	}
}

func (a *app) sendResultsAsZIP(chatID int64, results []downloadResult, format, lang string) deliveryReport {
	report, _ := a.sendZIPArchives(chatID, results, format, lang, nil)
	return report
}

// playlistPart is one batch of a playlist delivered in batches: its first and last track and
// the last track of the whole selection.
type playlistPart struct {
	start, end, last int
}

// errDeliveryReported stops a batched delivery after an error the user has already been told about.
var errDeliveryReported = errors.New("delivery stopped")

// sendZIPArchives packs the downloaded results into archives under the upload limit and sends
// them. The archives of a playlist part are named after its tracks, and the progress and summary
// messages are then left to the caller; an error means the user got a ZIP error message.
func (a *app) sendZIPArchives(chatID int64, results []downloadResult, format, lang string, part *playlistPart) (deliveryReport, error) {
	defer a.downloader.clearSessions(results)

	failures := deliveryFailures{total: len(results)}
	defer func() { a.reportDeliveryFailures("zip", chatID, format, failures) }()
	valid := make([]downloadResult, 0, len(results))
	var tooLarge []downloadResult
	for i, result := range results {
		switch {
		case result.TooLarge:
			tooLarge = append(tooLarge, result)
		case result.Error != "":
			failures.add(i+1, result.Error)
		case regularFileExists(result.FilePath):
			// A file over the limit would make its archive part too large as well.
			if size := regularFileSize(result.FilePath); size > a.fileLimit() {
				result.TooLarge, result.Size = true, size
				tooLarge = append(tooLarge, result)
				continue
			}
			valid = append(valid, result)
		}
	}
	if len(valid) == 0 {
		if len(tooLarge) == 0 && part == nil {
			a.sendText(chatID, tr("no_files_for_zip", lang), "", nil)
		}
		return deliveryReport{Failed: len(results), TooLarge: tooLarge}, nil
	}
	if part == nil {
		a.sendText(chatID, tr("zipping", lang, "count", strconv.Itoa(len(valid))), "", nil)
	}
	chunks := splitResultsBySize(valid, a.fileLimit()*9/10)
	delivered := 0
	skipped := len(results) - len(valid)
	skippedLine := ""
	if skipped > 0 {
		skippedLine = tr("zip_caption_skipped", lang, "skipped", strconv.Itoa(skipped))
	}
	for index, chunk := range chunks {
		zipID, err := randomID()
		if err != nil {
			a.reportError(errorReport{Stage: "zip", ChatID: chatID, Format: format, Error: err.Error()})
			a.sendText(chatID, tr("zip_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
			return deliveryReport{Delivered: delivered, Failed: len(results) - delivered, TooLarge: tooLarge}, fmt.Errorf("%w: %v", errDeliveryReported, err)
		}
		zipPath := filepath.Join(filepath.Dir(valid[0].FilePath), zipName(part, index, len(chunks), zipID))
		// The tracks are removed as they are packed, so their sizes are kept for the notice about
		// an archive part over the limit.
		for i := range chunk {
			chunk[i].Size = regularFileSize(chunk[i].FilePath)
		}
		if err := createZIP(zipPath, chunk, true); err != nil {
			a.reportError(errorReport{Stage: "zip", ChatID: chatID, Format: format, Error: err.Error()})
			a.sendText(chatID, tr("zip_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
			return deliveryReport{Delivered: delivered, Failed: len(results) - delivered, TooLarge: tooLarge}, fmt.Errorf("%w: %v", errDeliveryReported, err)
		}
		info, err := os.Stat(zipPath)
		if err != nil {
			a.reportError(errorReport{Stage: "zip", ChatID: chatID, Format: format, Error: err.Error()})
			a.sendText(chatID, tr("zip_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
			return deliveryReport{Delivered: delivered, Failed: len(results) - delivered, TooLarge: tooLarge}, fmt.Errorf("%w: %v", errDeliveryReported, err)
		}
		if info.Size() > a.fileLimit() {
			// Only a part of one file just under the limit can outgrow it with the archive
			// overhead; its tracks are offered in a lighter format and the other parts still go.
			_ = os.Remove(zipPath)
			for _, result := range chunk {
				result.TooLarge = true
				tooLarge = append(tooLarge, result)
			}
			continue
		}
		document := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(zipPath))
		partLine := ""
		switch {
		case part != nil && len(chunks) > 1:
			partLine = tr("zip_tracks_part", lang, "start", strconv.Itoa(part.start), "end", strconv.Itoa(part.end), "part", strconv.Itoa(index+1), "total", strconv.Itoa(len(chunks)))
		case part != nil:
			partLine = tr("zip_tracks", lang, "start", strconv.Itoa(part.start), "end", strconv.Itoa(part.end))
		case len(chunks) > 1:
			partLine = tr("zip_part", lang, "part", strconv.Itoa(index+1), "total", strconv.Itoa(len(chunks)))
		}
		document.Caption = tr("zip_caption", lang, "count", strconv.Itoa(len(chunk)), "size", humanSize(info.Size(), lang), "fmt", strings.ToUpper(format), "skipped", skippedLine+partLine)
		document.ParseMode = "HTML"
		_, err = sendTelegram(a.bot, document)
		_ = os.Remove(zipPath)
		if err != nil {
			a.reportError(errorReport{Stage: "zip", ChatID: chatID, Format: format, Error: err.Error()})
			a.sendText(chatID, tr("zip_error", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
			return deliveryReport{Delivered: delivered, Failed: len(results) - delivered, TooLarge: tooLarge}, fmt.Errorf("%w: %v", errDeliveryReported, err)
		}
		delivered += len(chunk)
	}
	if delivered > 0 && part == nil {
		a.sendText(chatID, tr("zip_sent", lang), "", nil)
	}
	return deliveryReport{Delivered: delivered, Failed: len(results) - delivered, TooLarge: tooLarge}, nil
}

// zipName names archive index of total; the archives of a playlist part carry its tracks, padded
// to the width of the last track so that they sort in order.
func zipName(part *playlistPart, index, total int, id string) string {
	name := fmt.Sprintf("playlist_%02d_of_%02d", index+1, total)
	if part != nil {
		width := len(strconv.Itoa(part.last))
		name = fmt.Sprintf("playlist_%0*d-%0*d", width, part.start, width, part.end)
		if total > 1 {
			name += fmt.Sprintf("_%02d_of_%02d", index+1, total)
		}
	}
	return name + "_" + id + ".zip"
}

func splitResultsBySize(results []downloadResult, limit int64) [][]downloadResult {
	var chunks [][]downloadResult
	var current []downloadResult
	var size int64
	for _, result := range results {
		info, err := os.Stat(result.FilePath)
		if err != nil {
			continue
		}
		if len(current) > 0 && size+info.Size() > limit {
			chunks = append(chunks, current)
			current = nil
			size = 0
		}
		current = append(current, result)
		size += info.Size()
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// mediaGroupEnd returns where the media group starting at start ends: a group holds up to 10
// tracks and, behind a local Bot API server with its larger files, no more than mediaGroupMaxBytes
// unless a single track is larger, so one request stays bounded.
func mediaGroupEnd(sizes []int64, start int) int {
	end := start
	var total int64
	for end < len(sizes) && end-start < 10 && (end == start || total+sizes[end] <= mediaGroupMaxBytes) {
		total += sizes[end]
		end++
	}
	return end
}

// fileLimit is the upload limit: MAX_FILE_SIZE, capped by what the Bot API server accepts.
func (a *app) fileLimit() int64 {
	limit := maxFileSize
	if a.cfg.TelegramAPIURL != "" {
		limit = localMaxFileSize
	}
	if a.cfg.MaxFileSize > 0 && a.cfg.MaxFileSize <= limit {
		return a.cfg.MaxFileSize
	}
	return maxFileSize
}

func (a *app) guideText(key, lang string) string {
	username := "bot_username"
	if a.bot != nil && a.bot.Self.UserName != "" {
		username = a.bot.Self.UserName
	}
	text := tr(key, lang, "username", html.EscapeString(username))
	if key == "help" {
		if a.cfg.LastfmAPIKey != "" {
			text += "\n" + tr("lastfm_help", lang)
		}
		text += "\n" + tr("notify_help", lang) + "\n" + tr("id_help", lang)
	}
	return text
}

func (a *app) sendText(chatID int64, text, parseMode string, markup *tgbotapi.InlineKeyboardMarkup) *tgbotapi.Message {
	message := tgbotapi.NewMessage(chatID, text)
	message.ParseMode = parseMode
	if markup != nil {
		message.ReplyMarkup = *markup
	}
	sent, err := sendTelegram(a.bot, message)
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
		message, err := sendTelegram(a.bot, config)
		if err == nil {
			return &message
		}
		log.Printf("Не удалось изменить сообщение: %v", err)
	}
	return a.sendText(callback.From.ID, text, parseMode, markup)
}
