package main

import (
	"context"
	"errors"
	"html"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (a *app) handleInlineQuery(query *tgbotapi.InlineQuery) {
	if query == nil || query.From == nil {
		return
	}
	lang := a.inlineLang(query.From)
	if a.inline == nil {
		a.answerInline(query.ID, nil, lang)
		return
	}
	text := strings.TrimSpace(query.Query)
	if text == "" {
		a.inline.cancelQuery(query.From.ID)
		a.answerInline(query.ID, nil, lang)
		return
	}

	ctx, queryID := a.inline.beginQuery(a.ctx, query.From.ID)
	defer a.inline.finishQuery(query.From.ID, queryID)
	directURL := detectURL(text)
	if directURL == "" && utf8.RuneCountInString(text) < inlineMinQueryLen {
		a.answerInline(query.ID, nil, lang)
		return
	}
	if directURL == "" {
		timer := time.NewTimer(inlineDebounce)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
	}
	if allowed, _ := a.inlineLimiter.allow(query.From.ID); !allowed {
		if a.store != nil {
			a.store.increment(a.ctx, "rate_limited")
		}
		a.answerInline(query.ID, nil, lang)
		return
	}
	candidates, err := a.runInlineLookup(ctx, text)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Printf("Inline-поиск %q: %v", text, err)
		a.answerInline(query.ID, nil, lang)
		return
	}
	if ctx.Err() != nil {
		return
	}

	results := make([]interface{}, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.UserID = query.From.ID
		resultID, err := a.inline.storeCandidate(candidate)
		if err != nil {
			log.Printf("Сохранить inline-результат: %v", err)
			continue
		}
		fileID := a.inline.cachedFileID(candidate.CacheKey)
		loading := fileID == ""
		if loading {
			fileID = a.inline.placeholderID
		}
		result := tgbotapi.NewInlineQueryResultCachedAudio(resultID, fileID)
		result.Caption = inlineResultCaption(candidate, lang, loading)
		result.ParseMode = "HTML"
		if loading {
			markup := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("inline_cancel", lang), "inline_cancel:"+resultID)),
			)
			result.ReplyMarkup = &markup
		}
		results = append(results, result)
	}
	a.answerInline(query.ID, results, lang)
}

func (a *app) answerInline(queryID string, results []interface{}, lang string) {
	if results == nil {
		results = make([]interface{}, 0)
	}
	config := tgbotapi.InlineConfig{
		InlineQueryID:     queryID,
		Results:           results,
		CacheTime:         1,
		IsPersonal:        true,
		SwitchPMText:      tr("inline_switch_pm", lang),
		SwitchPMParameter: "inline_help",
	}
	if _, err := requestTelegram(a.bot, config); err != nil {
		log.Printf("Ответить на inline query: %v", err)
	}
}

func (a *app) inlineLang(user *tgbotapi.User) string {
	if lang, ok := a.getLang(user.ID); ok {
		return lang
	}
	if strings.HasPrefix(strings.ToLower(user.LanguageCode), "en") {
		return "en"
	}
	return defaultLang
}

func (a *app) handleChosenInlineResult(chosen *tgbotapi.ChosenInlineResult) {
	if chosen == nil || chosen.From == nil || a.inline == nil {
		return
	}
	candidate, ok := a.inline.takeCandidate(chosen.ResultID, chosen.From.ID)
	if !ok {
		return
	}
	if chosen.InlineMessageID == "" {
		if a.inline.cachedFileID(candidate.CacheKey) == "" {
			log.Printf("Inline-результ %s не содержит inline_message_id; включи inline feedback в BotFather", chosen.ResultID)
		}
		return
	}
	lang := a.inlineLang(chosen.From)
	if fileID := a.inline.cachedFileID(candidate.CacheKey); fileID != "" {
		if a.store != nil {
			a.store.increment(a.ctx, "cache_hits")
		}
		if err := a.editInlineAudio(chosen.InlineMessageID, fileID, candidate, lang); err != nil {
			log.Printf("Заменить inline placeholder на аудио: %v", err)
		}
		return
	}
	if !a.beginUserDownload(chosen.From.ID) {
		a.editInlineError(chosen.InlineMessageID, tr("user_download_active", lang))
		return
	}
	defer a.finishUserDownload(chosen.From.ID)
	inlineOK := false
	inlineCancelled := false
	inlineFailure := "inline download failed"
	defer func() {
		if inlineOK && a.store != nil {
			a.store.increment(a.ctx, "downloads_ok")
		} else if inlineCancelled && a.store != nil {
			a.store.increment(a.ctx, "downloads_cancelled")
		} else if !inlineOK {
			a.reportDownloadFailure(inlineFailure)
		}
	}()

	downloadCtx, cancel := context.WithCancel(a.ctx)
	if !a.inline.setActive(chosen.ResultID, inlineActiveDownload{
		cancel:          cancel,
		userID:          chosen.From.ID,
		inlineMessageID: chosen.InlineMessageID,
	}) {
		cancel()
		a.editInlineError(chosen.InlineMessageID, tr("inline_already_active", lang))
		return
	}
	defer func() {
		cancel()
		a.inline.clearActive(chosen.ResultID)
	}()

	sourceID, extractor := candidate.SourceID, candidate.Extractor
	if sourceID == "" || extractor == "" {
		parts := strings.Split(candidate.CacheKey, ":")
		if len(parts) >= 2 {
			extractor, sourceID = parts[0], parts[1]
		}
	}
	pending := pendingURL{URL: candidate.URL, Preview: mediaPreview{SourceID: sourceID, Extractor: extractor}}
	entry, err := a.ensureCachedAudio(downloadCtx, pending, "mp3", "320", nil)
	if err != nil {
		inlineFailure = err.Error()
		inlineCancelled = errors.Is(err, context.Canceled)
		if !errors.Is(err, context.Canceled) {
			a.editInlineError(chosen.InlineMessageID, tr("inline_error", lang, "error", html.EscapeString(err.Error())))
		}
		return
	}
	alias := entry
	alias.Key = candidate.CacheKey
	if err := a.store.putCachedAudio(a.ctx, alias); err != nil {
		log.Printf("Сохранить inline file_id: %v", err)
	}
	candidate.Title = firstNonEmpty(entry.Title, candidate.Title)
	candidate.Artist = firstNonEmpty(entry.Artist, candidate.Artist)
	candidate.Duration = firstNonEmpty(entry.Duration, candidate.Duration)
	if err := a.editInlineAudio(chosen.InlineMessageID, entry.FileID, candidate, lang); err != nil {
		inlineFailure = err.Error()
		log.Printf("Заменить inline placeholder на аудио: %v", err)
		return
	}
	inlineOK = true
}

func (a *app) editInlineAudio(inlineMessageID, fileID string, candidate inlineCandidate, lang string) error {
	media := tgbotapi.NewInputMediaAudio(tgbotapi.FileID(fileID))
	media.Title = candidate.Title
	media.Performer = candidate.Artist
	media.Duration = inlineDurationSeconds(candidate.Duration)
	media.Caption = inlineResultCaption(candidate, lang, false)
	media.ParseMode = "HTML"
	edit := tgbotapi.EditMessageMediaConfig{
		BaseEdit: tgbotapi.BaseEdit{InlineMessageID: inlineMessageID, ReplyMarkup: emptyInlineKeyboard()},
		Media:    media,
	}
	_, err := requestTelegram(a.bot, edit)
	return err
}

func (a *app) editInlineError(inlineMessageID, text string) {
	edit := tgbotapi.EditMessageCaptionConfig{
		BaseEdit:  tgbotapi.BaseEdit{InlineMessageID: inlineMessageID, ReplyMarkup: emptyInlineKeyboard()},
		Caption:   text,
		ParseMode: "HTML",
	}
	if _, err := requestTelegram(a.bot, edit); err != nil {
		log.Printf("Обновить ошибку inline-загрузки: %v", err)
	}
}

func emptyInlineKeyboard() *tgbotapi.InlineKeyboardMarkup {
	return &tgbotapi.InlineKeyboardMarkup{InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{}}
}

func (a *app) handleInlineCancel(callback *tgbotapi.CallbackQuery) {
	if a.inline == nil || callback.From == nil || callback.InlineMessageID == "" {
		return
	}
	id := strings.TrimPrefix(callback.Data, "inline_cancel:")
	if !a.inline.cancelDownload(id, callback.From.ID, callback.InlineMessageID) {
		return
	}
	lang := a.inlineLang(callback.From)
	a.editInlineError(callback.InlineMessageID, tr("inline_cancelled", lang))
}
