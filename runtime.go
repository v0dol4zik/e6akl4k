package main

import (
	"bufio"
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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
	if cacheChatIDText := strings.TrimSpace(os.Getenv("INLINE_CACHE_CHAT_ID")); cacheChatIDText != "" {
		cacheChatID, parseErr := strconv.ParseInt(cacheChatIDText, 10, 64)
		if parseErr != nil || cacheChatID == 0 {
			log.Fatalf("INLINE_CACHE_CHAT_ID должен быть числовым ID чата: %q", cacheChatIDText)
		}
		inline, inlineErr := newInlineService(ctx, bot, dl, cacheChatID)
		if inlineErr != nil {
			log.Fatalf("Не удалось запустить inline-режим: %v", inlineErr)
		}
		application.inline = inline
		log.Printf("Inline-режим включён, cache-чат: %d", cacheChatID)
	} else {
		log.Print("INLINE_CACHE_CHAT_ID не задан: inline-режим отключён")
	}
	updates := bot.GetUpdatesChan(tgbotapi.UpdateConfig{
		Timeout: 60,
		AllowedUpdates: []string{
			tgbotapi.UpdateTypeMessage,
			tgbotapi.UpdateTypeCallbackQuery,
			tgbotapi.UpdateTypeInlineQuery,
			tgbotapi.UpdateTypeChosenInlineResult,
		},
	})
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
