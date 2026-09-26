package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// telegramLocalMetadataKey holds the ID of the bot that has logged out of the cloud Bot API and
// works through the local server, so the logOut, which locks the cloud out for 10 minutes, runs once.
const telegramLocalMetadataKey = "telegram_local_bot_id"

const localTelegramAttempts = 30

var localTelegramRetryDelay = 2 * time.Second

// connectTelegram opens the Bot API client: the cloud one by default, or the local server at
// TELEGRAM_API_URL. A local server serves a bot only after it has logged out of the cloud.
func connectTelegram(ctx context.Context, cfg config, client tgbotapi.HTTPClient, state *store) (*tgbotapi.BotAPI, error) {
	botID, _, found := strings.Cut(cfg.BotToken, ":")
	if !found {
		// Never store a malformed token itself; Telegram rejects it anyway.
		botID = "unknown"
	}
	switched := state.metadata(ctx, telegramLocalMetadataKey)
	if cfg.TelegramAPIURL == "" {
		bot, err := tgbotapi.NewBotAPIWithClient(cfg.BotToken, tgbotapi.APIEndpoint, client)
		if err != nil {
			if switched != "" {
				err = fmt.Errorf("%w (облачный Bot API не пускает бота 10 минут после перехода на локальный сервер)", err)
			}
			return nil, err
		}
		if switched != "" {
			// Back on the cloud: a later switch to a local server has to log out again.
			if err := state.setMetadata(ctx, telegramLocalMetadataKey, ""); err != nil {
				return nil, fmt.Errorf("сбросить отметку локального Bot API: %w", err)
			}
		}
		return bot, nil
	}
	if switched != botID {
		if err := logOutCloudTelegram(cfg.BotToken, client); err != nil {
			return nil, fmt.Errorf("выйти из облачного Bot API: %w", err)
		}
		log.Print("Бот вышел из облачного Bot API: вернуться в облако можно не раньше чем через 10 минут")
	}
	bot, err := connectLocalTelegram(ctx, cfg, client)
	if err != nil {
		return nil, err
	}
	if switched != botID {
		if err := state.setMetadata(ctx, telegramLocalMetadataKey, botID); err != nil {
			return nil, fmt.Errorf("сохранить отметку локального Bot API: %w", err)
		}
	}
	return bot, nil
}

// logOutCloudTelegram logs the bot out of the cloud Bot API. A bot that has already logged out is
// refused with 401 there, which counts as done.
func logOutCloudTelegram(token string, client tgbotapi.HTTPClient) error {
	cloud := &tgbotapi.BotAPI{Token: token, Client: client, Buffer: 100}
	cloud.SetAPIEndpoint(tgbotapi.APIEndpoint)
	_, err := cloud.Request(tgbotapi.LogOutConfig{})
	if apiErr, ok := telegramAPIError(err); ok && apiErr.Code == http.StatusUnauthorized {
		log.Print("Облачный Bot API уже не принимает бота: считаю, что logOut уже был")
		return nil
	}
	return err
}

// connectLocalTelegram waits for the local server, which may start after the bot; a Bot API error
// such as a wrong token is final.
func connectLocalTelegram(ctx context.Context, cfg config, client tgbotapi.HTTPClient) (*tgbotapi.BotAPI, error) {
	endpoint := cfg.TelegramAPIURL + "/bot%s/%s"
	for attempt := 1; ; attempt++ {
		bot, err := tgbotapi.NewBotAPIWithClient(cfg.BotToken, endpoint, client)
		if err == nil {
			log.Printf("Telegram Bot API: локальный сервер %s", cfg.TelegramAPIURL)
			return bot, nil
		}
		if _, apiErr := telegramAPIError(err); apiErr || attempt == localTelegramAttempts {
			return nil, fmt.Errorf("локальный Bot API %s: %w", cfg.TelegramAPIURL, err)
		}
		log.Printf("Локальный Bot API %s пока недоступен (попытка %d): %v", cfg.TelegramAPIURL, attempt, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(localTelegramRetryDelay):
		}
	}
}
