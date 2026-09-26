package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type noticeCall struct {
	method, chatID, text, entities, fromChat, messageID, markup string
}

// noticeTelegram records sends, copies and edits; chats listed in blocked answer 403 as a user
// who blocked the bot.
type noticeTelegram struct {
	mu      sync.Mutex
	blocked map[string]bool
	calls   []noticeCall
}

func (n *noticeTelegram) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseForm()
		method := filepath.Base(r.URL.Path)
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
			return
		case "sendMessage", "copyMessage", "editMessageText", "editMessageReplyMarkup":
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		chatID := r.FormValue("chat_id")
		n.mu.Lock()
		n.calls = append(n.calls, noticeCall{method, chatID, r.FormValue("text"), r.FormValue("entities"), r.FormValue("from_chat_id"), r.FormValue("message_id"), r.FormValue("reply_markup")})
		blocked := n.blocked[chatID]
		n.mu.Unlock()
		if blocked && (method == "sendMessage" || method == "copyMessage") {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":7,"date":1,"chat":{"id":%s,"type":"private"},"text":"x"}}`, chatID)
	})
}

func (n *noticeTelegram) snapshot() []noticeCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.calls
	n.calls = nil
	return out
}

// waitFor polls until a call matching the condition arrives and returns the calls up to it.
func (n *noticeTelegram) waitFor(t *testing.T, match func(noticeCall) bool) []noticeCall {
	t.Helper()
	var seen []noticeCall
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		seen = append(seen, n.snapshot()...)
		for _, call := range seen {
			if match(call) {
				return seen
			}
		}
	}
	t.Fatalf("the awaited call never came: %#v", seen)
	return nil
}

// newNoticeTestApp has owner 10, users 11, 14 (blocked the bot) and 15 (Russian), user 12 who
// muted notices and banned user 13.
func newNoticeTestApp(t *testing.T) (*app, *store, *noticeTelegram) {
	t.Helper()
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	telegram := &noticeTelegram{blocked: map[string]bool{"14": true}}
	application := newCookieTestApp(t, &cookieTestTelegram{}, state, dir)
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegram.handler()})
	if err != nil {
		t.Fatal(err)
	}
	application.bot = bot
	for _, userID := range []int64{12, 13, 14} {
		application.setLang(userID, "en")
	}
	application.setLang(15, "ru")
	ctx := context.Background()
	if err := state.setNotificationsOff(ctx, 12, true); err != nil {
		t.Fatal(err)
	}
	if _, err := state.banUser(ctx, 13, 10, "spam"); err != nil {
		t.Fatal(err)
	}
	return application, state, telegram
}

func TestNoticeCommandsAreAdminOnly(t *testing.T) {
	application, state, telegram := newNoticeTestApp(t)
	application.handleMessage(lastfmCommand(11, "/msgall hello everyone"))
	application.handleMessage(lastfmCommand(11, "/msg 15 hello"))
	calls := telegram.snapshot()
	if len(calls) != 2 {
		t.Fatalf("a regular user must only be refused: %#v", calls)
	}
	for _, call := range calls {
		if call.chatID != "11" || call.text != tr("admin_forbidden", "en") {
			t.Fatalf("a regular user must only be refused: %#v", calls)
		}
	}

	application.handleMessage(lastfmCommand(10, "/msgall hello everyone"))
	key := between(telegram.snapshot()[2].markup, `"`+noticeConfirmCallback, `:go"`)
	if key == "" {
		t.Fatal("the admin must get a confirmation")
	}
	application.handleCallback(lastfmCallback(11, noticeConfirmCallback+key+":go"))
	if calls := telegram.snapshot(); len(calls) != 0 {
		t.Fatalf("a regular user must not send someone's notice: %#v", calls)
	}
	// A second admin cannot send the notice of the first one either.
	if _, err := state.addAdmin(context.Background(), 16, 10); err != nil {
		t.Fatal(err)
	}
	application.setLang(16, "en")
	application.handleCallback(lastfmCallback(16, noticeConfirmCallback+key+":go"))
	if calls := telegram.snapshot(); len(calls) != 1 || calls[0].chatID != "16" || calls[0].text != tr("action_unavailable", "en") {
		t.Fatalf("another admin must be refused: %#v", calls)
	}
	application.handleCallback(lastfmCallback(10, noticeConfirmCallback+key+":no"))
	if calls := telegram.snapshot(); len(calls) != 1 || calls[0].method != "editMessageText" || calls[0].text != tr("notice_cancelled", "en") {
		t.Fatalf("the author must be able to cancel: %#v", calls)
	}
	application.handleCallback(lastfmCallback(10, noticeConfirmCallback+key+":go"))
	if calls := telegram.snapshot(); len(calls) != 1 || calls[0].text != tr("action_unavailable", "en") {
		t.Fatalf("a cancelled notice must not be sent: %#v", calls)
	}
}

func TestBroadcastPreviewsConfirmsAndReports(t *testing.T) {
	application, state, telegram := newNoticeTestApp(t)
	text := "/msgall 🎉 Новое: *жирный*"
	message := lastfmCommand(10, text)
	// "Новое" is bold: it starts after "/msgall 🎉 " — 8 + 3 UTF-16 units, the emoji takes two.
	message.Entities = append(message.Entities, tgbotapi.MessageEntity{Type: "bold", Offset: 11, Length: 5})
	application.handleMessage(message)
	calls := telegram.snapshot()
	if len(calls) != 3 || calls[0].text != tr("notice_preview", "en") || calls[1].text != "🎉 Новое: *жирный*" || calls[1].markup != "" {
		t.Fatalf("preview calls = %#v", calls)
	}
	var entities []tgbotapi.MessageEntity
	if err := json.Unmarshal([]byte(calls[1].entities), &entities); err != nil || !reflect.DeepEqual(entities, []tgbotapi.MessageEntity{{Type: "bold", Offset: 3, Length: 5}}) {
		t.Fatalf("preview entities = %s, %v", calls[1].entities, err)
	}
	// 11, 14 and 15 get it; the sender, muted 12 and banned 13 do not.
	if want := tr("notice_confirm", "en", "count", "3", "muted", "1"); calls[2].text != want {
		t.Fatalf("confirmation = %q, want %q", calls[2].text, want)
	}
	previewEntities := calls[1].entities
	key := between(calls[2].markup, `"`+noticeConfirmCallback, `:go"`)
	if key == "" || !strings.Contains(calls[2].markup, noticeConfirmCallback+key+":no") {
		t.Fatalf("confirmation keyboard = %s", calls[2].markup)
	}

	application.handleCallback(lastfmCallback(10, noticeConfirmCallback+key+":go"))
	report := tr("notice_report", "en", "delivered", "2", "total", "3", "unreachable", "1", "failed", "0")
	calls = telegram.waitFor(t, func(call noticeCall) bool { return strings.Contains(call.text, report) })
	delivered := map[string]noticeCall{}
	for _, call := range calls {
		if call.method == "sendMessage" && call.text == "🎉 Новое: *жирный*" {
			delivered[call.chatID] = call
		}
	}
	if len(delivered) != 3 {
		t.Fatalf("notice recipients = %#v", calls)
	}
	for chatID, lang := range map[string]string{"11": "en", "14": "en", "15": "ru"} {
		call, ok := delivered[chatID]
		if !ok || call.entities != previewEntities || !strings.Contains(call.markup, `"`+notifyCallback+chatID+`:off:n"`) || !strings.Contains(call.markup, tr("btn_notify_off", lang)) {
			t.Fatalf("notice for %s = %#v", chatID, call)
		}
	}
	last := calls[len(calls)-1]
	if last.method != "editMessageText" || last.chatID != "10" || !strings.HasPrefix(last.text, tr("notice_done_title", "en")) {
		t.Fatalf("final report = %#v", last)
	}
	records, err := state.auditLog(context.Background(), 5)
	if err != nil || len(records) == 0 || records[0].Action != "msgall" || records[0].Details != "3: 🎉 Новое: *жирный*" {
		t.Fatalf("audit = %#v, %v", records, err)
	}
	application.mu.Lock()
	busy := application.broadcasting
	application.mu.Unlock()
	if busy {
		t.Fatal("a finished broadcast must release the lock")
	}
}

func TestBroadcastCopiesARepliedMessage(t *testing.T) {
	application, _, telegram := newNoticeTestApp(t)
	message := lastfmCommand(10, "/msgall")
	message.ReplyToMessage = &tgbotapi.Message{MessageID: 42}
	application.handleMessage(message)
	calls := telegram.snapshot()
	if len(calls) != 3 || calls[1].method != "copyMessage" || calls[1].chatID != "10" || calls[1].fromChat != "10" || calls[1].messageID != "42" {
		t.Fatalf("a reply must be previewed as a copy: %#v", calls)
	}
	key := between(calls[2].markup, `"`+noticeConfirmCallback, `:go"`)
	application.handleCallback(lastfmCallback(10, noticeConfirmCallback+key+":go"))
	calls = telegram.waitFor(t, func(call noticeCall) bool { return strings.HasPrefix(call.text, tr("notice_done_title", "en")) })
	copies := 0
	for _, call := range calls {
		if call.method == "copyMessage" && call.messageID == "42" && strings.Contains(call.markup, notifyCallback+call.chatID+":off:n") {
			copies++
		}
	}
	if copies != 3 {
		t.Fatalf("copies = %d: %#v", copies, calls)
	}

	application.handleMessage(lastfmCommand(10, "/msgall"))
	if calls := telegram.snapshot(); len(calls) != 1 || !strings.Contains(calls[0].text, "/msgall &lt;текст&gt;") {
		t.Fatalf("without text or a reply /msgall must explain itself: %#v", calls)
	}
}

func TestDirectNoticeRespectsMuteAndReportsDelivery(t *testing.T) {
	application, state, telegram := newNoticeTestApp(t)
	application.handleMessage(lastfmCommand(10, "/msg 15 привет"))
	calls := telegram.snapshot()
	if len(calls) != 2 || calls[0].chatID != "15" || calls[0].text != "привет" || !strings.Contains(calls[0].markup, `"ntf:15:off:n"`) || !strings.Contains(calls[0].markup, tr("btn_notify_off", "ru")) {
		t.Fatalf("direct notice = %#v", calls)
	}
	if calls[1].chatID != "10" || calls[1].text != tr("notice_sent_one", "en", "id", "15") {
		t.Fatalf("the admin must learn it was sent: %#v", calls)
	}
	records, err := state.auditLog(context.Background(), 5)
	if err != nil || len(records) == 0 || records[0].Action != "msg" || records[0].TargetID != 15 || records[0].Details != "привет" {
		t.Fatalf("audit = %#v, %v", records, err)
	}

	for text, want := range map[string]string{
		"/msg 12 hi": tr("notice_muted_one", "en", "id", "12"),
		"/msg 14 hi": tr("notice_unreachable_one", "en", "id", "14"),
		"/msg 15":    tr("admin_usage", "en", "usage", "/msg &lt;tg_id&gt; &lt;текст&gt; — или ответом на сообщение"),
		"/msg x hi":  tr("admin_usage", "en", "usage", "/msg &lt;tg_id&gt; &lt;текст&gt; — или ответом на сообщение"),
	} {
		application.handleMessage(lastfmCommand(10, text))
		calls := telegram.snapshot()
		if len(calls) == 0 || calls[len(calls)-1].chatID != "10" || calls[len(calls)-1].text != want {
			t.Errorf("%s: %#v, want %q", text, calls, want)
		}
		for _, call := range calls {
			if call.chatID == "12" {
				t.Errorf("%s: a muted user got the notice", text)
			}
		}
	}
}

func TestNotifyCommandAndButtonToggleNotices(t *testing.T) {
	application, state, telegram := newNoticeTestApp(t)
	ctx := context.Background()
	application.handleMessage(lastfmCommand(11, "/notify"))
	calls := telegram.snapshot()
	if len(calls) != 1 || calls[0].text != tr("notify_status_on", "en") || !strings.Contains(calls[0].markup, `"ntf:11:off"`) {
		t.Fatalf("/notify = %#v", calls)
	}
	application.handleMessage(lastfmCommand(11, "/notify off"))
	if calls := telegram.snapshot(); len(calls) != 1 || calls[0].text != tr("notify_status_off", "en") || !state.notificationsOff(ctx, 11) {
		t.Fatalf("/notify off = %#v", calls)
	}
	application.handleMessage(lastfmCommand(11, "/notify вкл"))
	if telegram.snapshot(); state.notificationsOff(ctx, 11) {
		t.Fatal("/notify вкл must unmute")
	}

	application.handleCallback(lastfmCallback(12, "ntf:11:off:n"))
	if calls := telegram.snapshot(); len(calls) != 0 || state.notificationsOff(ctx, 11) {
		t.Fatalf("another user must not flip the switch: %#v", calls)
	}
	application.handleCallback(lastfmCallback(11, "ntf:11:off:n"))
	calls = telegram.snapshot()
	if len(calls) != 1 || calls[0].method != "editMessageReplyMarkup" || !strings.Contains(calls[0].markup, `"ntf:11:on:n"`) || !state.notificationsOff(ctx, 11) {
		t.Fatalf("the button under a notice must only swap itself: %#v", calls)
	}
	application.handleCallback(lastfmCallback(11, "ntf:11:on"))
	calls = telegram.snapshot()
	if len(calls) != 1 || calls[0].method != "editMessageText" || calls[0].text != tr("notify_status_on", "en") || state.notificationsOff(ctx, 11) {
		t.Fatalf("the menu button must rewrite the status: %#v", calls)
	}
}

func TestNoticeBodyKeepsItsFormatting(t *testing.T) {
	for _, test := range []struct {
		text string
		skip int
		want int
	}{
		{"/msgall hello", 0, 8},
		{"/msgall   hello world", 0, 10},
		{"/msgall@bot\nhello", 0, 12},
		{"/msg 42 hello", 1, 8},
		{"/msg  42\n\nhello", 1, 10},
		{"/msg 42", 1, 7},
		{"/msgall", 0, 7},
	} {
		if got := commandPrefixEnd(test.text, test.skip); got != test.want {
			t.Errorf("commandPrefixEnd(%q, %d) = %d, want %d", test.text, test.skip, got, test.want)
		}
	}
	if got := utf16Len("a🎉я"); got != 4 {
		t.Errorf("utf16Len = %d", got)
	}
	entities := []tgbotapi.MessageEntity{
		{Type: "bot_command", Offset: 0, Length: 7},
		{Type: "bold", Offset: 8, Length: 5},
		{Type: "italic", Offset: 5, Length: 6},
		{Type: "underline", Offset: 12, Length: 10},
		{Type: "custom_emoji", Offset: 9, Length: 2},
	}
	want := []tgbotapi.MessageEntity{{Type: "bold", Offset: 0, Length: 5}, {Type: "italic", Offset: 0, Length: 3}, {Type: "underline", Offset: 4, Length: 4}}
	if got := shiftEntities(entities, 8, 8); !reflect.DeepEqual(got, want) {
		t.Errorf("shiftEntities = %#v", got)
	}
}
