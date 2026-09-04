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

func TestStorePersistsAdminsBansAndAudit(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if changed, err := state.addAdmin(ctx, 20, 10); err != nil || !changed {
		t.Fatalf("add admin changed=%v err=%v", changed, err)
	}
	if changed, err := state.addAdmin(ctx, 20, 10); err != nil || changed {
		t.Fatalf("duplicate admin changed=%v err=%v", changed, err)
	}
	if !state.isDynamicAdmin(ctx, 20) {
		t.Fatal("dynamic admin was not persisted")
	}
	if changed, err := state.banUser(ctx, 30, 20, "spam"); err != nil || !changed {
		t.Fatalf("ban changed=%v err=%v", changed, err)
	}
	if ban, ok := state.isBanned(ctx, 30); !ok || ban.Reason != "spam" || ban.BannedBy != 20 {
		t.Fatalf("ban=%#v ok=%v", ban, ok)
	}
	state.audit(ctx, 20, "ban", 30, "spam")
	log, err := state.auditLog(ctx, 10)
	if err != nil || len(log) != 1 || log[0].Action != "ban" {
		t.Fatalf("audit=%#v err=%v", log, err)
	}
	if changed, err := state.pardonUser(ctx, 30); err != nil || !changed {
		t.Fatalf("pardon changed=%v err=%v", changed, err)
	}
	if changed, err := state.deleteAdmin(ctx, 20); err != nil || !changed {
		t.Fatalf("delete admin changed=%v err=%v", changed, err)
	}
}

func TestBannedUpdateIsRejectedBeforeHandlers(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.banUser(context.Background(), 30, 10, "spam"); err != nil {
		t.Fatal(err)
	}
	var sent atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"test_bot"}}`)
		case "sendMessage":
			sent.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":30,"type":"private"}}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	application := newAppWithServices(context.Background(), bot, nil, state, config{AdminIDs: map[int64]bool{10: true}})
	update := tgbotapi.Update{UpdateID: 1, Message: &tgbotapi.Message{Text: "/start", Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 6}}, From: &tgbotapi.User{ID: 30}, Chat: &tgbotapi.Chat{ID: 30, Type: "private"}}}
	success := application.handleUpdate(update)
	if !success || sent.Load() != 1 {
		t.Fatalf("banned update success=%v messages=%d", success, sent.Load())
	}
}

func TestOwnerAndDynamicAdminRoles(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.addAdmin(context.Background(), 20, 10); err != nil {
		t.Fatal(err)
	}
	application := newAppWithServices(context.Background(), nil, nil, state, config{AdminIDs: map[int64]bool{10: true}})
	if !application.isOwner(10) || !application.isAdmin(10) || !application.isAdmin(20) || application.isOwner(20) || application.isAdmin(30) {
		t.Fatalf("unexpected roles owner10=%v admin10=%v admin20=%v owner20=%v admin30=%v", application.isOwner(10), application.isAdmin(10), application.isAdmin(20), application.isOwner(20), application.isAdmin(30))
	}
}

func TestIDCommandResolvesObservedUsername(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.observeTelegramUser(context.Background(), &tgbotapi.User{ID: 4242, UserName: "Known_User"}); err != nil {
		t.Fatal(err)
	}
	var reply string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"test_bot"}}`)
		case "sendMessage":
			_ = r.ParseForm()
			reply = r.FormValue("text")
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":7,"type":"private"}}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	application := newAppWithServices(context.Background(), bot, nil, state, config{})
	message := &tgbotapi.Message{
		Text:     "/id @KNOWN_user",
		Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 3}},
		From:     &tgbotapi.User{ID: 7, UserName: "Requester"},
		Chat:     &tgbotapi.Chat{ID: 7, Type: "private"},
	}
	if !application.handleAdminCommand(message) || !strings.Contains(reply, "4242") || !strings.Contains(reply, "@known_user") {
		t.Fatalf("unexpected /id reply: %q", reply)
	}
}
