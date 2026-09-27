package main

import (
	"html"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// errorReportCallback prefixes the buttons under an error report: erp:<action>:<report id>.
const errorReportCallback = "erp:"

// errorReportRetryStages are the stages whose failed request can be repeated from the stored
// link and format.
var errorReportRetryStages = map[string]bool{
	"preview": true, "batch_preview": true, "download": true, "playlist": true,
	"cache_send": true, "inline": true, "inline_send": true,
}

func (r errorReportRecord) errorReport() errorReport {
	return errorReport{Stage: r.Stage, ChatID: r.ChatID, UserID: r.UserID, URL: r.URL, Query: r.Query, Format: r.Format, Error: r.Error}
}

// errorReportKeyboard offers retry and fix for repeatable failures and always the developer copy.
func errorReportKeyboard(id string, report errorReport, lang string) *tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton
	if report.URL != "" && errorReportRetryStages[report.Stage] {
		row := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_report_retry", lang), errorReportCallback+"retry:"+id))
		if report.UserID != 0 {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(tr("btn_report_fix", lang), errorReportCallback+"fix:"+id))
		}
		rows = append(rows, row)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_report_copy", lang), errorReportCallback+"copy:"+id)))
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

func errorReportConfirmKeyboard(id, lang string) *tgbotapi.InlineKeyboardMarkup {
	markup := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr("btn_report_fix_confirm", lang), errorReportCallback+"fixok:"+id),
		tgbotapi.NewInlineKeyboardButtonData(tr("btn_report_back", lang), errorReportCallback+"back:"+id),
	))
	return &markup
}

func errorReportDoneKeyboard(id, lang string) *tgbotapi.InlineKeyboardMarkup {
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_report_fixed", lang), errorReportCallback+"noop:"+id)),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_report_copy", lang), errorReportCallback+"copy:"+id)),
	)
	return &markup
}

// handleErrorReportCallback serves the report buttons. Only administrators may press them;
// answers that carry report details go to the admin's private chat, never to the channel.
func (a *app) handleErrorReportCallback(callback *tgbotapi.CallbackQuery) {
	if a.store == nil || !a.isAdmin(callback.From.ID) {
		return
	}
	action, id, ok := strings.Cut(strings.TrimPrefix(callback.Data, errorReportCallback), ":")
	if !ok || id == "" || action == "noop" {
		return
	}
	adminID := callback.From.ID
	lang := a.langOrDefault(adminID)
	record, err := a.store.errorReportByID(a.ctx, id)
	if err != nil {
		a.sendText(adminID, tr("report_unavailable", lang, "id", html.EscapeString(id)), "HTML", nil)
		return
	}
	switch action {
	case "copy":
		a.sendText(adminID, "<pre>"+html.EscapeString(developerReportText(record))+"</pre>", "HTML", nil)
	case "retry":
		if record.URL == "" || !errorReportRetryStages[record.Stage] {
			return
		}
		a.sendText(adminID, tr("report_retry_started", lang, "id", html.EscapeString(id)), "HTML", nil)
		go a.rerunErrorReport(adminID, adminID, record, lang)
	case "fix":
		if record.UserID == 0 || record.URL == "" {
			return
		}
		if !record.ResolvedAt.IsZero() {
			a.editErrorReportKeyboard(callback, errorReportDoneKeyboard(id, defaultLang))
			return
		}
		a.editErrorReportKeyboard(callback, errorReportConfirmKeyboard(id, defaultLang))
	case "back":
		a.editErrorReportKeyboard(callback, errorReportKeyboard(id, record.errorReport(), defaultLang))
	case "fixok":
		a.fixErrorReport(callback, record, lang)
	}
}

// fixErrorReport tells the user the failure is fixed and repeats their request. The report is
// claimed first so that two admins cannot notify the same user twice.
func (a *app) fixErrorReport(callback *tgbotapi.CallbackQuery, record errorReportRecord, lang string) {
	adminID := callback.From.ID
	if record.UserID == 0 || record.URL == "" {
		return
	}
	claimed, err := a.store.resolveErrorReport(a.ctx, record.ID, adminID)
	if err != nil {
		log.Printf("Отметить отчёт %s исправленным: %v", record.ID, err)
		return
	}
	if !claimed {
		a.sendText(adminID, tr("report_fix_already", lang, "id", html.EscapeString(record.ID)), "HTML", nil)
		a.editErrorReportKeyboard(callback, errorReportDoneKeyboard(record.ID, defaultLang))
		return
	}
	restore := func() {
		a.store.unresolveErrorReport(a.ctx, record.ID)
		a.editErrorReportKeyboard(callback, errorReportKeyboard(record.ID, record.errorReport(), defaultLang))
	}
	if _, banned := a.store.isBanned(a.ctx, record.UserID); banned {
		restore()
		a.sendText(adminID, tr("report_fix_banned", lang, "id", html.EscapeString(record.ID)), "HTML", nil)
		return
	}
	chatID := record.ChatID
	if chatID == 0 {
		chatID = record.UserID
	}
	userLang := a.langOrDefault(record.UserID)
	notice := tgbotapi.NewMessage(chatID, tr("report_fixed_notice", userLang))
	notice.ParseMode = "HTML"
	if _, err := sendTelegram(a.bot, notice); err != nil {
		restore()
		a.sendText(adminID, tr("report_fix_failed", lang, "id", html.EscapeString(record.ID), "error", html.EscapeString(err.Error())), "HTML", nil)
		return
	}
	a.store.audit(a.ctx, adminID, "report_fix_sent", record.UserID, record.ID)
	a.editErrorReportKeyboard(callback, errorReportDoneKeyboard(record.ID, defaultLang))
	go a.rerunErrorReport(record.UserID, chatID, record, userLang)
}

// rerunErrorReport repeats the failed request as if userID had sent the link to chatID again,
// with the reported format, so the result lands in that chat. The stored link is normalized
// like a sent one, so a report filed before a link rewrite (such as YouTube Samples) is retried
// with the rewritten link.
func (a *app) rerunErrorReport(userID, chatID int64, record errorReportRecord, lang string) {
	format, quality, _ := strings.Cut(record.Format, " ")
	message := &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: chatID}, From: &tgbotapi.User{ID: userID}}
	a.openURL(message, firstNonEmpty(normalizeDetectedURL(record.URL), record.URL), lang, format, quality)
}

func (a *app) editErrorReportKeyboard(callback *tgbotapi.CallbackQuery, markup *tgbotapi.InlineKeyboardMarkup) {
	if callback.Message == nil || callback.Message.Chat == nil || markup == nil {
		return
	}
	edit := tgbotapi.NewEditMessageReplyMarkup(callback.Message.Chat.ID, callback.Message.MessageID, *markup)
	if _, err := requestTelegram(a.bot, edit); err != nil && !strings.Contains(err.Error(), "message is not modified") {
		log.Printf("Обновить кнопки отчёта: %v", err)
	}
}

// developerReportText is a plain-text report without the user's ID or username, safe to paste
// into an issue. The URL keeps only track-identifying parameters.
func developerReportText(record errorReportRecord) string {
	lines := []string{
		"report: " + record.ID + " (" + record.Class + ")",
		"time: " + record.CreatedAt.UTC().Format(time.DateTime) + " UTC",
		"version: " + firstNonEmpty(record.Version, "unknown"),
		"stage: " + record.Stage,
	}
	if record.URL != "" {
		lines = append(lines, "source: "+sourceHost(record.URL), "url: "+safeTraceURL(record.URL))
	}
	if record.Query != "" {
		lines = append(lines, "query: "+shortenRunes(record.Query, 200))
	}
	if record.Format != "" {
		lines = append(lines, "format: "+record.Format)
	}
	lines = append(lines, "error:", shortenRunes(record.Error, maxErrorReportText))
	return strings.Join(lines, "\n")
}
