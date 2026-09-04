package main

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const telegramMaxAttempts = 3

var telegramSleep = time.Sleep

type deadlineTransport struct {
	base         http.RoundTripper
	requestLimit time.Duration
	uploadLimit  time.Duration
	pollLimit    time.Duration
}

type redactingHTTPClient struct {
	client  *http.Client
	secrets []string
}

func (c redactingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	response, err := c.client.Do(request)
	if err == nil {
		return response, nil
	}
	message := err.Error()
	for _, secret := range c.secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return nil, errors.New(message)
}

func (t deadlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	limit := t.requestLimit
	if strings.HasSuffix(request.URL.Path, "/getUpdates") {
		limit = t.pollLimit
	} else if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") || telegramMediaMethod(request.URL.Path) {
		limit = t.uploadLimit
	}
	ctx, cancel := context.WithTimeout(request.Context(), limit)
	response, err := t.base.RoundTrip(request.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &cancelOnClose{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

func telegramMediaMethod(path string) bool {
	for _, method := range []string{"/sendAudio", "/sendDocument", "/sendMediaGroup"} {
		if strings.HasSuffix(path, method) {
			return true
		}
	}
	return false
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *cancelOnClose) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func sendTelegram(bot *tgbotapi.BotAPI, config tgbotapi.Chattable) (tgbotapi.Message, error) {
	var message tgbotapi.Message
	var err error
	for attempt := 0; attempt < telegramMaxAttempts; attempt++ {
		message, err = bot.Send(config)
		if !retryableTelegramResponse(err, safeTelegramRetry(config)) || attempt+1 == telegramMaxAttempts {
			return message, err
		}
		telegramSleep(telegramRetryDelay(err, attempt))
	}
	return message, err
}

func requestTelegram(bot *tgbotapi.BotAPI, config tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	var response *tgbotapi.APIResponse
	var err error
	for attempt := 0; attempt < telegramMaxAttempts; attempt++ {
		response, err = bot.Request(config)
		if !retryableTelegramResponse(err, true) || attempt+1 == telegramMaxAttempts {
			return response, err
		}
		telegramSleep(telegramRetryDelay(err, attempt))
	}
	return response, err
}

func sendMediaGroupTelegram(bot *tgbotapi.BotAPI, config tgbotapi.MediaGroupConfig) ([]tgbotapi.Message, error) {
	var messages []tgbotapi.Message
	var err error
	for attempt := 0; attempt < telegramMaxAttempts; attempt++ {
		messages, err = bot.SendMediaGroup(config)
		// A 429 response means Telegram rejected the request, so retrying cannot
		// duplicate a delivered album. A transport/5xx failure is ambiguous.
		if !retryableTelegramResponse(err, false) || attempt+1 == telegramMaxAttempts {
			return messages, err
		}
		telegramSleep(telegramRetryDelay(err, attempt))
	}
	return messages, err
}

func canFallbackToIndividual(err error) bool {
	apiErr, ok := telegramAPIError(err)
	return ok && apiErr.Code >= 400 && apiErr.Code < 500 && apiErr.Code != http.StatusTooManyRequests
}

func retryableTelegramResponse(err error, retryTransient bool) bool {
	apiErr, ok := telegramAPIError(err)
	if !ok {
		return retryTransient && err != nil
	}
	return apiErr.RetryAfter > 0 || retryTransient && apiErr.Code >= 500
}

func telegramAPIError(err error) (tgbotapi.Error, bool) {
	var pointer *tgbotapi.Error
	if errors.As(err, &pointer) && pointer != nil {
		return *pointer, true
	}
	var value tgbotapi.Error
	if errors.As(err, &value) {
		return value, true
	}
	return tgbotapi.Error{}, false
}

func safeTelegramRetry(config tgbotapi.Chattable) bool {
	switch config.(type) {
	case tgbotapi.EditMessageTextConfig,
		tgbotapi.EditMessageCaptionConfig,
		tgbotapi.EditMessageMediaConfig,
		tgbotapi.CallbackConfig:
		return true
	default:
		// A lost response to sendMessage/sendAudio/sendDocument may still mean
		// that Telegram delivered it. Retrying could create a duplicate.
		return false
	}
}

func telegramRetryDelay(err error, attempt int) time.Duration {
	if apiErr, ok := telegramAPIError(err); ok && apiErr.RetryAfter > 0 {
		delay := time.Duration(apiErr.RetryAfter) * time.Second
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		return delay + time.Duration(rand.IntN(250))*time.Millisecond
	}
	return time.Duration(1<<attempt)*time.Second + time.Duration(rand.IntN(250))*time.Millisecond
}
