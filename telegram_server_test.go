package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBotAPIServers answers the cloud Bot API and a local server; logOut and getMe answer with the
// given status, and a negative status is a refused connection.
type fakeBotAPIServers struct {
	mu         sync.Mutex
	calls      []string
	logOutCode int
	localDown  int
	localGetMe int
	cloudGetMe int
}

func (f *fakeBotAPIServers) Do(request *http.Request) (*http.Response, error) {
	method := filepath.Base(request.URL.Path)
	call := request.URL.Host + " " + method
	f.mu.Lock()
	f.calls = append(f.calls, call)
	down := request.URL.Host == "telegram-bot-api:8081" && f.localDown > 0
	if down {
		f.localDown--
	}
	f.mu.Unlock()
	if down {
		return nil, errors.New("dial tcp: connection refused")
	}
	code := http.StatusOK
	switch {
	case method == "logOut":
		code = f.logOutCode
	case request.URL.Host == "telegram-bot-api:8081":
		code = f.localGetMe
	default:
		code = f.cloudGetMe
	}
	return handlerClient{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case code != http.StatusOK && code != 0:
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"error %d"}`, code, code)
		case method == "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})}.Do(request)
}

func (f *fakeBotAPIServers) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	f.calls = nil
	return calls
}

func TestConnectTelegramLocalServerLogsOutOfCloudOnce(t *testing.T) {
	localTelegramRetryDelay = time.Millisecond
	t.Cleanup(func() { localTelegramRetryDelay = 2 * time.Second })
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	ctx := context.Background()
	local := config{BotToken: "1:secret", TelegramAPIURL: "http://telegram-bot-api:8081"}
	servers := &fakeBotAPIServers{localDown: 2}

	bot, err := connectTelegram(ctx, local, servers, state)
	if err != nil || bot.Self.UserName != "testbot" {
		t.Fatalf("bot=%v err=%v", bot, err)
	}
	want := "api.telegram.org logOut,telegram-bot-api:8081 getMe,telegram-bot-api:8081 getMe,telegram-bot-api:8081 getMe"
	if got := strings.Join(servers.take(), ","); got != want {
		t.Fatalf("calls=%s want %s", got, want)
	}
	if got := state.metadata(ctx, telegramLocalMetadataKey); got != "1" {
		t.Fatalf("marker=%q, want the bot ID only", got)
	}
	if _, err := connectTelegram(ctx, local, servers, state); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(servers.take(), ","); got != "telegram-bot-api:8081 getMe" {
		t.Fatalf("a restart must not log out again: %s", got)
	}

	if _, err := connectTelegram(ctx, config{BotToken: "1:secret"}, servers, state); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(servers.take(), ","); got != "api.telegram.org getMe" {
		t.Fatalf("cloud calls=%s", got)
	}
	if got := state.metadata(ctx, telegramLocalMetadataKey); got != "" {
		t.Fatalf("the marker must be reset on the cloud: %q", got)
	}
	if _, err := connectTelegram(ctx, local, servers, state); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(servers.take(), ","); got != "api.telegram.org logOut,telegram-bot-api:8081 getMe" {
		t.Fatalf("switching back to the local server must log out again: %s", got)
	}
}

func TestConnectTelegramLocalServerFailures(t *testing.T) {
	localTelegramRetryDelay = time.Millisecond
	t.Cleanup(func() { localTelegramRetryDelay = 2 * time.Second })
	ctx := context.Background()
	local := config{BotToken: "1:secret", TelegramAPIURL: "http://telegram-bot-api:8081"}
	open := func(t *testing.T) *store {
		state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { state.Close() })
		return state
	}

	t.Run("already logged out", func(t *testing.T) {
		state := open(t)
		servers := &fakeBotAPIServers{logOutCode: http.StatusUnauthorized}
		if _, err := connectTelegram(ctx, local, servers, state); err != nil {
			t.Fatal(err)
		}
		if got := state.metadata(ctx, telegramLocalMetadataKey); got != "1" {
			t.Fatalf("marker=%q", got)
		}
	})
	t.Run("logOut fails", func(t *testing.T) {
		state := open(t)
		servers := &fakeBotAPIServers{logOutCode: http.StatusInternalServerError}
		if _, err := connectTelegram(ctx, local, servers, state); err == nil {
			t.Fatal("a failed logOut must stop the start")
		}
		if got := strings.Join(servers.take(), ","); got != "api.telegram.org logOut" {
			t.Fatalf("the local server must not be used before the logOut: %s", got)
		}
		if got := state.metadata(ctx, telegramLocalMetadataKey); got != "" {
			t.Fatalf("marker=%q", got)
		}
	})
	t.Run("wrong token on the local server", func(t *testing.T) {
		state := open(t)
		servers := &fakeBotAPIServers{localGetMe: http.StatusUnauthorized}
		_, err := connectTelegram(ctx, local, servers, state)
		if err == nil {
			t.Fatal("a Bot API error must not be retried")
		}
		if got := len(servers.take()); got != 2 {
			t.Fatalf("calls=%d, want logOut and one getMe", got)
		}
		if got := state.metadata(ctx, telegramLocalMetadataKey); got != "" {
			t.Fatalf("the marker is set only after a working local connection: %q", got)
		}
	})
	t.Run("local server never comes up", func(t *testing.T) {
		state := open(t)
		servers := &fakeBotAPIServers{localDown: localTelegramAttempts}
		if _, err := connectTelegram(ctx, local, servers, state); err == nil {
			t.Fatal("the start must fail once the retries run out")
		}
		if got := len(servers.take()); got != 1+localTelegramAttempts {
			t.Fatalf("calls=%d, want logOut and %d getMe", got, localTelegramAttempts)
		}
	})
	t.Run("cloud refuses after a switch", func(t *testing.T) {
		state := open(t)
		if err := state.setMetadata(ctx, telegramLocalMetadataKey, "1"); err != nil {
			t.Fatal(err)
		}
		servers := &fakeBotAPIServers{cloudGetMe: http.StatusUnauthorized}
		_, err := connectTelegram(ctx, config{BotToken: "1:secret"}, servers, state)
		if err == nil || !strings.Contains(err.Error(), "10 минут") {
			t.Fatalf("err=%v", err)
		}
		if got := state.metadata(ctx, telegramLocalMetadataKey); got != "1" {
			t.Fatalf("the marker must stay until the cloud accepts the bot: %q", got)
		}
	})
}
