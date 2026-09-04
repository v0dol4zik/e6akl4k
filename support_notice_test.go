package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestSupportNoticeIsThrottledAndCanBeHiddenForever(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	var sends, deletes atomic.Int32
	var sentText, replyMarkup string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"test_bot"}}`)
		case "sendMessage":
			_ = r.ParseForm()
			sends.Add(1)
			sentText = r.FormValue("text")
			replyMarkup = r.FormValue("reply_markup")
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":9,"date":1,"chat":{"id":7,"type":"private"}}}`)
		case "deleteMessage":
			deletes.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	application := newAppWithServices(context.Background(), bot, nil, state, config{})
	application.maybeSendSupportNotice(7, 7, "ru")
	application.maybeSendSupportNotice(7, 7, "ru")
	if sends.Load() != 1 || !strings.Contains(sentText, "нравится бот?") || !strings.Contains(replyMarkup, "❌ скрыть это сообщение.") {
		t.Fatalf("sends=%d text=%q markup=%q", sends.Load(), sentText, replyMarkup)
	}
	application.handleSupportNoticeDismiss(&tgbotapi.CallbackQuery{
		Data:    supportNoticeCallback + "7",
		From:    &tgbotapi.User{ID: 8},
		Message: &tgbotapi.Message{MessageID: 9, Chat: &tgbotapi.Chat{ID: 7, Type: "private"}},
	})
	if deletes.Load() != 0 {
		t.Fatal("another user was able to hide the notice")
	}
	application.handleSupportNoticeDismiss(&tgbotapi.CallbackQuery{
		Data:    supportNoticeCallback + "7",
		From:    &tgbotapi.User{ID: 7},
		Message: &tgbotapi.Message{MessageID: 9, Chat: &tgbotapi.Chat{ID: 7, Type: "private"}},
	})
	if deletes.Load() != 1 {
		t.Fatalf("delete calls=%d", deletes.Load())
	}
	if _, err := state.db.Exec(`UPDATE user_notices SET last_shown_at=0 WHERE user_id=7 AND notice=?`, supportNoticeKey); err != nil {
		t.Fatal(err)
	}
	application.maybeSendSupportNotice(7, 7, "ru")
	if sends.Load() != 1 {
		t.Fatalf("dismissed notice was sent again: %d", sends.Load())
	}
}
