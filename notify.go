package main

import (
	"html"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	notifyCallback        = "ntf:"
	noticeConfirmCallback = "ntc:"
	// noticeInterval paces a broadcast at 20 messages a second, below Telegram's bulk limit.
	noticeInterval = 50 * time.Millisecond
	// noticeProgressEvery is how often the admin's broadcast status is refreshed.
	noticeProgressEvery = 5 * time.Second
	pendingNoticeTTL    = 15 * time.Minute
	maxPendingNotices   = 32
)

// notice is what an admin posts: text with its formatting entities, or a message to copy.
type notice struct {
	Text      string
	Entities  []tgbotapi.MessageEntity
	FromChat  int64
	MessageID int
}

// pendingNotice is a /msgall waiting for the admin's confirmation.
type pendingNotice struct {
	notice
	AdminID   int64
	ChatID    int64
	ExpiresAt time.Time
}

type noticeReport struct {
	Total, Delivered, Unreachable, Failed int
}

func (r *noticeReport) add(userID int64, err error) {
	switch {
	case err == nil:
		r.Delivered++
	case noticeUnreachable(err):
		r.Unreachable++
	default:
		r.Failed++
		log.Printf("Не удалось доставить оповещение %d: %v", userID, err)
	}
}

// noticeUnreachable recognizes users who blocked the bot, deleted their account or never
// opened a chat with it.
func noticeUnreachable(err error) bool {
	apiErr, ok := telegramAPIError(err)
	return ok && (apiErr.Code == 403 || apiErr.Code == 400 && strings.Contains(strings.ToLower(apiErr.Message), "chat not found"))
}

// noticeFromCommand takes the notice out of an admin command. The text after the command and
// skip more words (the target ID of /msg) keeps its formatting; without text, the replied-to
// message is copied as is, which also covers photos, videos and files.
func noticeFromCommand(message *tgbotapi.Message, skip int) (notice, bool) {
	cut := commandPrefixEnd(message.Text, skip)
	if body := strings.TrimRightFunc(message.Text[cut:], unicode.IsSpace); body != "" {
		return notice{Text: body, Entities: shiftEntities(message.Entities, utf16Len(message.Text[:cut]), utf16Len(body))}, true
	}
	if message.ReplyToMessage != nil && message.Chat != nil {
		return notice{FromChat: message.Chat.ID, MessageID: message.ReplyToMessage.MessageID}, true
	}
	return notice{}, false
}

// commandPrefixEnd returns the byte offset where the command, the next skip words and the
// spaces after them end.
func commandPrefixEnd(text string, skip int) int {
	offset := 0
	for word := 0; word <= skip; word++ {
		offset = len(text) - len(strings.TrimLeftFunc(text[offset:], unicode.IsSpace))
		end := strings.IndexFunc(text[offset:], unicode.IsSpace)
		if end < 0 {
			return len(text)
		}
		offset += end
	}
	return len(text) - len(strings.TrimLeftFunc(text[offset:], unicode.IsSpace))
}

// utf16Len measures text the way Telegram counts entity offsets.
func utf16Len(text string) int {
	length := 0
	for _, r := range text {
		length++
		if r > 0xFFFF {
			length++
		}
	}
	return length
}

// shiftEntities moves formatting entities from the command message onto the notice body,
// which starts shift UTF-16 units later and is limit units long. Custom emoji are dropped: the
// library does not carry their IDs, and Telegram refuses such an entity without one.
func shiftEntities(entities []tgbotapi.MessageEntity, shift, limit int) []tgbotapi.MessageEntity {
	var shifted []tgbotapi.MessageEntity
	for _, entity := range entities {
		start, end := max(entity.Offset-shift, 0), min(entity.Offset+entity.Length-shift, limit)
		if end <= start || entity.Type == "custom_emoji" {
			continue
		}
		entity.Offset, entity.Length = start, end-start
		shifted = append(shifted, entity)
	}
	return shifted
}

func noticeExcerpt(n notice) string {
	if n.Text == "" {
		return "копия сообщения " + strconv.Itoa(n.MessageID)
	}
	return shortenRunes(n.Text, 200)
}

// deliverNotice sends one notice, with the mute switch when markup is given.
func (a *app) deliverNotice(n notice, chatID int64, markup *tgbotapi.InlineKeyboardMarkup) error {
	var config tgbotapi.Chattable
	if n.Text != "" {
		message := tgbotapi.NewMessage(chatID, n.Text)
		message.Entities = n.Entities
		if markup != nil {
			message.ReplyMarkup = *markup
		}
		config = message
	} else {
		copied := tgbotapi.NewCopyMessage(chatID, n.FromChat, n.MessageID)
		if markup != nil {
			copied.ReplyMarkup = *markup
		}
		config = copied
	}
	_, err := sendTelegram(a.bot, config)
	return err
}

// notifyKeyboard is the switch that mutes or unmutes notices. Under a notice (onNotice) it only
// swaps the button; in the /notify menu it also rewrites the status text.
func notifyKeyboard(userID int64, off, onNotice bool, lang string) *tgbotapi.InlineKeyboardMarkup {
	label, action := tr("btn_notify_off", lang), "off"
	if off {
		label, action = tr("btn_notify_on", lang), "on"
	}
	data := notifyCallback + strconv.FormatInt(userID, 10) + ":" + action
	if onNotice {
		data += ":n"
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(label, data)))
	return &markup
}

func notifyStatusText(off bool, lang string) string {
	if off {
		return tr("notify_status_off", lang)
	}
	return tr("notify_status_on", lang)
}

// handleNotifyCommand serves /notify: without arguments it shows the current state, "on" and
// "off" change it.
func (a *app) handleNotifyCommand(message *tgbotapi.Message, lang string) {
	chatID, userID := message.Chat.ID, message.From.ID
	if a.store == nil {
		a.sendText(chatID, tr("action_unavailable", lang), "", nil)
		return
	}
	off := a.store.notificationsOff(a.ctx, userID)
	switch strings.ToLower(strings.TrimSpace(message.CommandArguments())) {
	case "on", "вкл", "включить":
		off = false
	case "off", "выкл", "выключить":
		off = true
	default:
		a.sendText(chatID, notifyStatusText(off, lang), "HTML", notifyKeyboard(userID, off, false, lang))
		return
	}
	if err := a.store.setNotificationsOff(a.ctx, userID, off); err != nil {
		log.Printf("Сохранить настройку оповещений %d: %v", userID, err)
		a.sendText(chatID, tr("action_unavailable", lang), "", nil)
		return
	}
	a.sendText(chatID, notifyStatusText(off, lang), "HTML", notifyKeyboard(userID, off, false, lang))
}

// handleNotifyCallback serves "ntf:<user>:<on|off>[:n]"; presses by anyone but the owner of the
// switch are ignored.
func (a *app) handleNotifyCallback(callback *tgbotapi.CallbackQuery) {
	userID := callback.From.ID
	parts := strings.Split(strings.TrimPrefix(callback.Data, notifyCallback), ":")
	if a.store == nil || len(parts) < 2 || parts[0] != strconv.FormatInt(userID, 10) || parts[1] != "on" && parts[1] != "off" {
		return
	}
	lang, off := a.langOrDefault(userID), parts[1] == "off"
	if err := a.store.setNotificationsOff(a.ctx, userID, off); err != nil {
		log.Printf("Сохранить настройку оповещений %d: %v", userID, err)
		a.sendText(userID, tr("action_unavailable", lang), "", nil)
		return
	}
	if len(parts) == 3 && parts[2] == "n" && callback.Message != nil && callback.Message.Chat != nil {
		edit := tgbotapi.NewEditMessageReplyMarkup(callback.Message.Chat.ID, callback.Message.MessageID, *notifyKeyboard(userID, off, true, lang))
		if _, err := sendTelegram(a.bot, edit); err == nil {
			return
		}
		a.sendText(userID, notifyStatusText(off, lang), "HTML", notifyKeyboard(userID, off, false, lang))
		return
	}
	a.safeEdit(callback, notifyStatusText(off, lang), "HTML", notifyKeyboard(userID, off, false, lang))
}

// handleBroadcastCommand serves the admin-only /msgall: it shows the notice exactly as users
// will get it and asks to confirm before anything is sent.
func (a *app) handleBroadcastCommand(message *tgbotapi.Message, lang string) {
	chatID, adminID := message.Chat.ID, message.From.ID
	n, ok := noticeFromCommand(message, 0)
	if !ok {
		a.sendText(chatID, adminUsage(lang, "/msgall <текст> — или ответом на сообщение"), "HTML", nil)
		return
	}
	recipients, muted, err := a.store.noticeRecipients(a.ctx, adminID)
	if err != nil {
		a.adminStoreError(message, err)
		return
	}
	if len(recipients) == 0 {
		a.sendText(chatID, tr("notice_no_recipients", lang), "", nil)
		return
	}
	key, err := a.storeNotice(pendingNotice{notice: n, AdminID: adminID, ChatID: chatID})
	if err != nil {
		a.adminStoreError(message, err)
		return
	}
	a.sendText(chatID, tr("notice_preview", lang), "", nil)
	if err := a.deliverNotice(n, chatID, nil); err != nil {
		a.takeNotice(key, adminID)
		a.sendText(chatID, tr("notice_failed", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr("btn_notice_send", lang), noticeConfirmCallback+key+":go"),
		tgbotapi.NewInlineKeyboardButtonData(tr("btn_notice_cancel", lang), noticeConfirmCallback+key+":no"),
	))
	a.sendText(chatID, tr("notice_confirm", lang, "count", strconv.Itoa(len(recipients)), "muted", strconv.Itoa(muted)), "HTML", &markup)
}

// handleNoticeConfirm serves "ntc:<key>:<go|no>". Only the admin who wrote the notice can send
// it, and only while still an admin. The broadcast runs in the background, so the update is
// finished at once and a restart never replays it.
func (a *app) handleNoticeConfirm(callback *tgbotapi.CallbackQuery) {
	adminID := callback.From.ID
	if a.store == nil || !a.isAdmin(adminID) {
		return
	}
	lang := a.langOrDefault(adminID)
	key, action, _ := strings.Cut(strings.TrimPrefix(callback.Data, noticeConfirmCallback), ":")
	pending, ok := a.takeNotice(key, adminID)
	if !ok {
		a.sendText(adminID, tr("action_unavailable", lang), "", nil)
		return
	}
	if action != "go" {
		a.safeEdit(callback, tr("notice_cancelled", lang), "", nil)
		return
	}
	if !a.beginBroadcast() {
		a.restoreNotice(key, pending)
		a.sendText(adminID, tr("notice_busy", lang), "", nil)
		return
	}
	recipients, _, err := a.store.noticeRecipients(a.ctx, adminID)
	if err != nil || len(recipients) == 0 {
		a.endBroadcast()
		text := tr("notice_no_recipients", lang)
		if err != nil {
			text = tr("notice_failed", lang, "error", html.EscapeString(err.Error()))
		}
		a.safeEdit(callback, text, "HTML", nil)
		return
	}
	status := a.safeEdit(callback, tr("notice_progress", lang, "done", "0", "total", strconv.Itoa(len(recipients))), "HTML", nil)
	a.store.audit(a.ctx, adminID, "msgall", 0, strconv.Itoa(len(recipients))+": "+noticeExcerpt(pending.notice))
	go a.runBroadcast(pending.notice, pending.ChatID, status, recipients, lang)
}

func (a *app) runBroadcast(n notice, chatID int64, status *tgbotapi.Message, recipients []noticeRecipient, lang string) {
	defer a.endBroadcast()
	report := noticeReport{Total: len(recipients)}
	pace := time.NewTicker(noticeInterval)
	defer pace.Stop()
	progressAt := time.Now().Add(noticeProgressEvery)
	stopped := false
	for i, recipient := range recipients {
		if i > 0 {
			select {
			case <-a.ctx.Done():
				stopped = true
			case <-pace.C:
			}
		}
		if stopped {
			break
		}
		report.add(recipient.UserID, a.deliverNotice(n, recipient.UserID, notifyKeyboard(recipient.UserID, false, true, recipient.Lang)))
		if status != nil && i+1 < len(recipients) && time.Now().After(progressAt) {
			progressAt = time.Now().Add(noticeProgressEvery)
			edit := tgbotapi.NewEditMessageText(status.Chat.ID, status.MessageID, tr("notice_progress", lang, "done", strconv.Itoa(i+1), "total", strconv.Itoa(report.Total)))
			edit.ParseMode = "HTML"
			_, _ = sendTelegram(a.bot, edit)
		}
	}
	title := "notice_done_title"
	if stopped {
		title = "notice_stopped_title"
	}
	log.Printf("Рассылка: доставлено %d из %d, недоступны %d, ошибок %d", report.Delivered, report.Total, report.Unreachable, report.Failed)
	a.replaceStatusText(chatID, status, tr(title, lang)+"\n"+tr("notice_report", lang,
		"delivered", strconv.Itoa(report.Delivered), "total", strconv.Itoa(report.Total),
		"unreachable", strconv.Itoa(report.Unreachable), "failed", strconv.Itoa(report.Failed)))
}

// handleDirectNoticeCommand serves the admin-only /msg <tg_id>, sent at once. A user who muted
// notices does not get it, and the admin is told so.
func (a *app) handleDirectNoticeCommand(message *tgbotapi.Message, lang string) {
	chatID := message.Chat.ID
	target, _, ok := parseAdminTarget(message.CommandArguments())
	n, hasNotice := noticeFromCommand(message, 1)
	if !ok || !hasNotice {
		a.sendText(chatID, adminUsage(lang, "/msg <tg_id> <текст> — или ответом на сообщение"), "HTML", nil)
		return
	}
	id := strconv.FormatInt(target, 10)
	if a.store.notificationsOff(a.ctx, target) {
		a.sendText(chatID, tr("notice_muted_one", lang, "id", id), "HTML", nil)
		return
	}
	err := a.deliverNotice(n, target, notifyKeyboard(target, false, true, a.langOrDefault(target)))
	switch {
	case err == nil:
		a.store.audit(a.ctx, message.From.ID, "msg", target, noticeExcerpt(n))
		a.sendText(chatID, tr("notice_sent_one", lang, "id", id), "HTML", nil)
	case noticeUnreachable(err):
		a.sendText(chatID, tr("notice_unreachable_one", lang, "id", id), "HTML", nil)
	default:
		a.sendText(chatID, tr("notice_failed", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
	}
}

func (a *app) storeNotice(pending pendingNotice) (string, error) {
	key, err := randomID()
	if err != nil {
		return "", err
	}
	a.restoreNotice(key, pending)
	return key, nil
}

func (a *app) restoreNotice(key string, pending pendingNotice) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.notices == nil {
		a.notices = make(map[string]pendingNotice)
	}
	now := time.Now()
	for other, stored := range a.notices {
		if now.After(stored.ExpiresAt) || len(a.notices) >= maxPendingNotices {
			delete(a.notices, other)
		}
	}
	if pending.ExpiresAt.IsZero() {
		pending.ExpiresAt = now.Add(pendingNoticeTTL)
	}
	a.notices[key] = pending
}

// takeNotice consumes a pending notice of this admin; other admins' notices are left alone.
func (a *app) takeNotice(key string, adminID int64) (pendingNotice, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending, ok := a.notices[key]
	if !ok || pending.AdminID != adminID {
		return pendingNotice{}, false
	}
	delete(a.notices, key)
	return pending, time.Now().Before(pending.ExpiresAt)
}

// beginBroadcast lets one broadcast run at a time.
func (a *app) beginBroadcast() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.broadcasting {
		return false
	}
	a.broadcasting = true
	return true
}

func (a *app) endBroadcast() {
	a.mu.Lock()
	a.broadcasting = false
	a.mu.Unlock()
}
