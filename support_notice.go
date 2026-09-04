package main

import (
	"log"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	supportNoticeKey      = "support_promotion_v1"
	supportNoticeCallback = "support_notice:hide:"
	supportNoticeInterval = 24 * time.Hour
)

func supportNoticeKeyboard(lang string, userID int64) *tgbotapi.InlineKeyboardMarkup {
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_hide_support", lang), supportNoticeCallback+strconv.FormatInt(userID, 10))),
	)
	return &markup
}

func (a *app) maybeSendSupportNotice(chatID, userID int64, lang string) {
	if a.store == nil || a.bot == nil {
		return
	}
	show, err := a.store.claimUserNotice(a.ctx, userID, supportNoticeKey, supportNoticeInterval)
	if err != nil {
		log.Printf("Проверить сообщение поддержки для пользователя %d: %v", userID, err)
		return
	}
	if show {
		a.sendText(chatID, tr("support_notice", lang), "", supportNoticeKeyboard(lang, userID))
	}
}

func (a *app) handleSupportNoticeDismiss(callback *tgbotapi.CallbackQuery) {
	if callback == nil || callback.From == nil {
		return
	}
	targetID, err := strconv.ParseInt(strings.TrimPrefix(callback.Data, supportNoticeCallback), 10, 64)
	if err != nil || targetID != callback.From.ID {
		return
	}
	if a.store != nil {
		if err = a.store.dismissUserNotice(a.ctx, callback.From.ID, supportNoticeKey); err != nil {
			log.Printf("Скрыть сообщение поддержки для пользователя %d: %v", callback.From.ID, err)
			return
		}
	}
	if callback.Message == nil || callback.Message.Chat == nil {
		return
	}
	if _, err := requestTelegram(a.bot, tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)); err != nil {
		log.Printf("Удалить скрытое сообщение поддержки: %v", err)
	}
}
