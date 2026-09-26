package main

import (
	"bufio"
	"context"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	telegramRequestTimeout = 30 * time.Second
	telegramUploadTimeout  = 30 * time.Minute
)

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
	if os.Getenv("LOG_FORMAT") == "json" {
		handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
		slog.SetDefault(slog.New(handler))
		log.SetFlags(0)
		log.SetOutput(slogWriter{})
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if cfg.YandexProxy != nil {
		yandexProxy.Store(cfg.YandexProxy)
		log.Printf("Яндекс Музыка читается через прокси %s://%s", cfg.YandexProxy.Scheme, cfg.YandexProxy.Host)
	}

	dl, err := newDownloader(cfg.DownloadDir, cfg.MaxFileSize)
	if err != nil {
		log.Fatal(err)
	}
	dl.maxPlaylistTracks = cfg.MaxPlaylistTracks
	dl.ytdlpSleepRequests = cfg.YTDLPSleepRequests
	dl.ytdlpFragments = cfg.YTDLPFragments
	state, err := openStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Не удалось открыть SQLite: %v", err)
	}
	defer state.Close()
	if err := state.cleanup(context.Background(), cfg.CacheTTL); err != nil {
		log.Printf("Очистить старый кэш: %v", err)
	}
	telegramHTTP := redactingHTTPClient{client: &http.Client{
		Transport: deadlineTransport{base: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 0,
			IdleConnTimeout:       90 * time.Second,
		}, requestLimit: telegramRequestTimeout, uploadLimit: telegramUploadTimeout, pollLimit: 75 * time.Second},
	}, secrets: []string{cfg.BotToken}}
	bot, err := connectTelegram(context.Background(), cfg, telegramHTTP, state)
	if err != nil {
		log.Fatalf("Не удалось подключиться к Telegram: %v", err)
	}
	if _, err := bot.Request(tgbotapi.DeleteWebhookConfig{DropPendingUpdates: cfg.DropPendingUpdates}); err != nil {
		log.Fatalf("Не удалось удалить webhook: %v", err)
	}
	log.Printf("Бот @%s запущен", bot.Self.UserName)
	adminIDs := make(map[int64]bool, len(cfg.AdminIDs))
	for id := range cfg.AdminIDs {
		adminIDs[id] = true
	}
	if records, adminErr := state.admins(context.Background()); adminErr == nil {
		for _, record := range records {
			adminIDs[record.UserID] = true
		}
	}
	registerBotCommands(bot, adminIDs, cfg.LastfmAPIKey != "")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startMediaMetricsRecorder(ctx, state)
	application := newAppWithServices(ctx, bot, dl, state, cfg)
	dl.onCookieRetry = application.reportCookieRetry
	application.startErrorReporter(ctx)
	_ = startHTTPServer(ctx, application, cfg.HTTPAddr)
	application.startDiskMonitor(ctx)
	if cfg.CacheChatID != 0 {
		if err := migrateLegacyInlineCache(ctx, state, inlineCachePath(dl.downloadDir), cfg.CacheTTL); err != nil {
			log.Printf("Мигрировать inline-кэш: %v", err)
		}
		application.inline = newInlineService(state, cfg.CacheTTL)
		log.Printf("Inline-режим включён, cache-чат: %d", cfg.CacheChatID)
	} else {
		log.Print("CACHE_CHAT_ID/INLINE_CACHE_CHAT_ID не задан: inline-режим отключён")
	}
	var handlers sync.WaitGroup
	dispatcher := newUpdateDispatcher(cfg.UpdateQueueSize)
	for i := 0; i < cfg.UpdateWorkers; i++ {
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			for {
				key, job, ok := dispatcher.take()
				if !ok {
					return
				}
				success := application.handleUpdate(job.update)
				if job.complete != nil {
					job.complete(success)
				}
				dispatcher.done(key)
			}
		}()
	}
	persistedComplete := func(updateID int) func(bool) {
		return func(success bool) {
			if err := state.finishUpdate(context.Background(), updateID, success); err != nil {
				log.Printf("Завершить сохранённый update %d: %v", updateID, err)
			}
		}
	}
	if cfg.DropPendingUpdates {
		if err := state.discardPendingUpdates(ctx); err != nil {
			log.Fatalf("Сбросить локальную очередь Telegram updates: %v", err)
		}
	}
	pendingUpdates, err := state.pendingUpdates(ctx)
	if err != nil {
		log.Fatalf("Прочитать незавершённые Telegram updates: %v", err)
	}
	for _, pending := range pendingUpdates {
		if !dispatcher.submit(ctx, pending, persistedComplete(pending.UpdateID)) {
			break
		}
	}
	if len(pendingUpdates) > 0 {
		log.Printf("Восстановлено незавершённых Telegram updates: %d", len(pendingUpdates))
	}
	updates := startUpdatePoller(ctx, bot, state)
	for {
		select {
		case <-ctx.Done():
			dispatcher.close()
			finished := make(chan struct{})
			go func() {
				handlers.Wait()
				close(finished)
			}()
			select {
			case <-finished:
			case <-time.After(cfg.ShutdownTimeout):
				log.Printf("Не все обработчики успели завершиться за %s", cfg.ShutdownTimeout)
			}
			log.Print("Бот остановлен")
			return
		case update, ok := <-updates:
			if !ok {
				dispatcher.close()
				handlers.Wait()
				return
			}
			_ = dispatcher.submit(ctx, update, persistedComplete(update.UpdateID))
		}
	}
}

func startUpdatePoller(ctx context.Context, bot *tgbotapi.BotAPI, state *store) <-chan tgbotapi.Update {
	result := make(chan tgbotapi.Update)
	config := tgbotapi.UpdateConfig{
		Timeout: 60,
		AllowedUpdates: []string{
			tgbotapi.UpdateTypeMessage,
			tgbotapi.UpdateTypeCallbackQuery,
			tgbotapi.UpdateTypeInlineQuery,
			tgbotapi.UpdateTypeChosenInlineResult,
		},
	}
	go func() {
		defer close(result)
		for ctx.Err() == nil {
			updates, err := bot.GetUpdates(config)
			if err != nil {
				log.Printf("Получить Telegram updates: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			for _, update := range updates {
				inserted, err := state.persistUpdate(ctx, update)
				if err != nil {
					log.Printf("Сохранить Telegram update %d: %v", update.UpdateID, err)
					break
				}
				config.Offset = update.UpdateID + 1
				if !inserted {
					continue
				}
				select {
				case result <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return result
}

type slogWriter struct{}

func (slogWriter) Write(p []byte) (int, error) {
	slog.Info(string(p))
	return len(p), nil
}

// registerBotCommands publishes the command menu. /lastfm is listed only when LASTFM_API_KEY is
// set; admin commands are never listed, but admins get the same per-chat menu so that a chat
// scope left over from an older release is overwritten.
func registerBotCommands(bot *tgbotapi.BotAPI, admins map[int64]bool, lastfm bool) {
	defaultScope := tgbotapi.NewBotCommandScopeDefault()
	if _, err := bot.Request(tgbotapi.NewSetMyCommands(botCommands(defaultLang, lastfm)...)); err != nil {
		log.Printf("Установить команды: %v", err)
	}
	for _, lang := range languageOrder {
		config := tgbotapi.NewSetMyCommandsWithScopeAndLanguage(defaultScope, lang, botCommands(lang, lastfm)...)
		if _, err := bot.Request(config); err != nil {
			log.Printf("Установить команды для языка %s: %v", lang, err)
		}
	}
	for adminID := range admins {
		registerChatCommands(bot, adminID, lastfm)
	}
}

func registerChatCommands(bot *tgbotapi.BotAPI, chatID int64, lastfm bool) {
	if bot == nil {
		return
	}
	scope := tgbotapi.NewBotCommandScopeChat(chatID)
	if _, err := bot.Request(tgbotapi.NewSetMyCommandsWithScope(scope, botCommands(defaultLang, lastfm)...)); err != nil {
		log.Printf("Установить команды %d: %v", chatID, err)
	}
	for _, lang := range languageOrder {
		config := tgbotapi.NewSetMyCommandsWithScopeAndLanguage(scope, lang, botCommands(lang, lastfm)...)
		if _, err := bot.Request(config); err != nil {
			log.Printf("Установить команды %d для языка %s: %v", chatID, lang, err)
		}
	}
}

func botCommands(lang string, lastfm bool) []tgbotapi.BotCommand {
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: tr("command_start", lang)},
		{Command: "help", Description: tr("command_help", lang)},
		{Command: "language", Description: tr("command_language", lang)},
		{Command: "settings", Description: tr("command_settings", lang)},
		{Command: "history", Description: tr("command_history", lang)},
		{Command: "export", Description: tr("command_export", lang)},
		{Command: "cover", Description: tr("command_cover", lang)},
	}
	if lastfm {
		commands = append(commands, tgbotapi.BotCommand{Command: "lastfm", Description: tr("command_lastfm", lang)})
	}
	return append(commands,
		tgbotapi.BotCommand{Command: "notify", Description: tr("command_notify", lang)},
		tgbotapi.BotCommand{Command: "id", Description: tr("command_id", lang)})
}
