package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestSendTelegramRetriesSafeEdits(t *testing.T) {
	bot, calls := retryTestBot(t, "editMessageText", 2)
	previousSleep := telegramSleep
	telegramSleep = func(time.Duration) {}
	t.Cleanup(func() { telegramSleep = previousSleep })

	if _, err := sendTelegram(bot, tgbotapi.NewEditMessageText(10, 20, "updated")); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("edit attempts=%d, want 3", got)
	}
}

func TestSendTelegramDoesNotDuplicateUnsafeSend(t *testing.T) {
	bot, calls := retryTestBot(t, "sendMessage", 3)
	previousSleep := telegramSleep
	telegramSleep = func(time.Duration) {}
	t.Cleanup(func() { telegramSleep = previousSleep })

	if _, err := sendTelegram(bot, tgbotapi.NewMessage(10, "hello")); err == nil {
		t.Fatal("transient sendMessage error was hidden")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("sendMessage attempts=%d, want 1 to avoid duplicates", got)
	}
}

func TestTelegramRetryAfterSupportsPointerError(t *testing.T) {
	err := &tgbotapi.Error{Code: http.StatusTooManyRequests, ResponseParameters: tgbotapi.ResponseParameters{RetryAfter: 2}}
	if !retryableTelegramResponse(err, false) {
		t.Fatal("429 response was not retryable")
	}
	delay := telegramRetryDelay(err, 0)
	if delay < 2*time.Second || delay >= 2250*time.Millisecond {
		t.Fatalf("retry delay=%s", delay)
	}
}

func TestMediaGroupFallbackOnlyForDefinitiveClientErrors(t *testing.T) {
	if canFallbackToIndividual(fmt.Errorf("connection reset")) {
		t.Fatal("ambiguous transport failure could duplicate an album")
	}
	if !canFallbackToIndividual(&tgbotapi.Error{Code: http.StatusBadRequest}) {
		t.Fatal("definitive bad request should allow individual fallback")
	}
	if canFallbackToIndividual(&tgbotapi.Error{Code: http.StatusTooManyRequests}) {
		t.Fatal("rate limit should not immediately fan out to individual sends")
	}
}

func TestTelegramMediaMethodsUseUploadTimeout(t *testing.T) {
	for _, path := range []string{"/bottoken/sendAudio", "/bottoken/sendDocument", "/bottoken/sendMediaGroup"} {
		if !telegramMediaMethod(path) {
			t.Errorf("media method not recognized: %s", path)
		}
	}
	for _, path := range []string{"/bottoken/getUpdates", "/bottoken/sendMessage"} {
		if telegramMediaMethod(path) {
			t.Errorf("non-media method recognized: %s", path)
		}
	}
}

func TestTelegramHTTPClientRedactsSecrets(t *testing.T) {
	const secret = "123456:super-secret-token"
	client := redactingHTTPClient{
		client:  &http.Client{Transport: failingRoundTripper{secret: secret}},
		secrets: []string{secret},
	}
	request, err := http.NewRequest(http.MethodPost, "https://telegram.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Do(request); err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("unredacted error=%v", err)
	}
}

type failingRoundTripper struct{ secret string }

func (t failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("request https://api.telegram.org/bot%s/getUpdates failed", t.secret)
}

func retryTestBot(t *testing.T, method string, failures int32) (*tgbotapi.BotAPI, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if filepath.Base(r.URL.Path) == "getMe" {
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
			return
		}
		if filepath.Base(r.URL.Path) != method {
			t.Errorf("method=%q, want %q", filepath.Base(r.URL.Path), method)
		}
		if calls.Add(1) <= failures {
			fmt.Fprint(w, `{"ok":false,"error_code":500,"description":"temporary"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"}}}`)
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	return bot, &calls
}
