package main

import (
	"context"
	"errors"
	"html"
	"log"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// errInlineCollection marks a playlist, album or channel link: inline mode sends one track, so
// such links are left to the bot chat.
var errInlineCollection = errors.New("inline-режим не скачивает плейлисты")

// inlineArticle is an InlineQueryResultArticle with the field names of the current Bot API: the
// vendored library still sends thumb_url, which Bot API 6.6 renamed to thumbnail_url.
type inlineArticle struct {
	Type                string                           `json:"type"`
	ID                  string                           `json:"id"`
	Title               string                           `json:"title"`
	Description         string                           `json:"description,omitempty"`
	ThumbnailURL        string                           `json:"thumbnail_url,omitempty"`
	InputMessageContent tgbotapi.InputTextMessageContent `json:"input_message_content"`
	ReplyMarkup         *tgbotapi.InlineKeyboardMarkup   `json:"reply_markup,omitempty"`
}

func (a *app) handleInlineQuery(query *tgbotapi.InlineQuery) {
	if query == nil || query.From == nil {
		return
	}
	lang := a.inlineLang(query.From)
	if a.inline == nil {
		a.answerInline(query.ID, nil, tr("inline_switch_pm", lang))
		return
	}
	text := strings.TrimSpace(query.Query)
	if text == "" {
		a.inline.cancelQuery(query.From.ID)
		a.answerInline(query.ID, nil, tr("inline_hint_empty", lang))
		return
	}

	ctx, queryID := a.inline.beginQuery(a.ctx, query.From.ID)
	defer a.inline.finishQuery(query.From.ID, queryID)
	directURL := detectURL(text)
	if directURL == "" {
		if utf8.RuneCountInString(text) < inlineMinQueryLen {
			a.answerInline(query.ID, nil, tr("inline_hint_short", lang))
			return
		}
		// A query arrives on every keystroke: the next one cancels this wait.
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
		a.answerInline(query.ID, nil, tr("inline_hint_limited", lang))
		return
	}
	candidates, hint := a.inlineLookup(ctx, text, directURL)
	if errors.Is(ctx.Err(), context.Canceled) {
		// A newer query replaced this one, and Telegram no longer shows its answer.
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
		results = append(results, inlineResult(resultID, candidate, a.inline.cachedFileID(candidate.CacheKey), lang))
	}
	a.answerInline(query.ID, results, tr(hint, lang))
}

// inlineLookup finds the results of an inline query. hint is the locale key of the button above
// them, which also explains an empty list: a playlist link, nothing found, a failed search.
func (a *app) inlineLookup(ctx context.Context, text, directURL string) ([]inlineCandidate, string) {
	_, release, err := a.lookups.acquire(ctx)
	if err != nil {
		if errors.Is(err, errQueueFull) {
			if a.store != nil {
				a.store.increment(a.ctx, "queue_rejected")
			}
			return nil, "inline_hint_busy"
		}
		return nil, "inline_hint_error"
	}
	defer release()
	var candidates []inlineCandidate
	if directURL != "" {
		candidates, err = a.inlineLinkCandidates(ctx, directURL)
	} else {
		candidates, err = a.downloader.searchLookup(ctx, text, 0)
	}
	switch {
	case err == nil && len(candidates) > 0:
		return candidates, "inline_switch_pm"
	case err == nil || errors.Is(err, errNothingFound):
		return nil, "inline_hint_nothing"
	case errors.Is(err, errInlineCollection):
		return nil, "inline_hint_playlist"
	case errors.Is(err, errMusicServiceBlocked):
		return nil, "inline_hint_blocked"
	case errors.Is(err, errGenericLinkPage):
		return nil, "inline_hint_link"
	}
	if !errors.Is(err, context.Canceled) {
		log.Printf("Inline-поиск %q: %v", text, err)
	}
	return nil, "inline_hint_error"
}

// inlineLinkCandidates reads a pasted link the way a private chat does. A YouTube video is named
// through oEmbed, because a full yt-dlp probe takes most of the time an inline answer may wait;
// a music-service track is searched on YouTube by its title.
func (a *app) inlineLinkCandidates(ctx context.Context, rawURL string) ([]inlineCandidate, error) {
	if requiresMusicResolution(rawURL) {
		rawURL = expandMusicShortLink(ctx, rawURL)
		if musicCollectionKind(rawURL) != "" {
			return nil, errInlineCollection
		}
		preview, err := a.inspectURL(ctx, rawURL)
		if err != nil {
			return nil, err
		}
		return a.downloader.searchLookup(ctx, musicSearchQuery(preview), preview.DurationSeconds)
	}
	id, playlist := youtubeLinkTarget(rawURL)
	if playlist {
		return nil, errInlineCollection
	}
	if id != "" {
		candidate := youtubeCandidate(id)
		title, author, err := fetchOEmbed(ctx, "https://www.youtube.com/oembed?format=json&url="+url.QueryEscape(candidate.URL))
		if err == nil {
			candidate.Title, candidate.Artist = title, author
			return []inlineCandidate{candidate}, nil
		}
		// An age-restricted or embed-disabled video has no oEmbed; the probe may still read it.
		rawURL = candidate.URL
	}
	preview, err := a.downloader.preview(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if preview.IsPlaylist {
		return nil, errInlineCollection
	}
	thumbnail := youtubeThumbnail(id)
	if thumbnail == "" && strings.HasPrefix(preview.Thumbnail, "https://") {
		thumbnail = preview.Thumbnail
	}
	return []inlineCandidate{{
		URL: rawURL, CacheKey: inlineCacheKey(rawURL, preview.Extractor, preview.SourceID),
		Title: preview.Title, Artist: preview.Artist, Duration: preview.Duration,
		SourceID: preview.SourceID, Extractor: preview.Extractor, Thumbnail: thumbnail,
	}}, nil
}

// inlineResult shows a cached track as ready audio. Any other track is an article: its message
// names the track, and the download turns it into the audio once it is sent.
func inlineResult(resultID string, candidate inlineCandidate, fileID, lang string) interface{} {
	if fileID != "" {
		return tgbotapi.NewInlineQueryResultCachedAudio(resultID, fileID)
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr("inline_cancel", lang), "inline_cancel:"+resultID),
	))
	return inlineArticle{
		Type:         "article",
		ID:           resultID,
		Title:        shortenRunes(firstNonEmpty(candidate.Title, "Unknown"), maxTitleLength),
		Description:  inlineDetails(candidate),
		ThumbnailURL: candidate.Thumbnail,
		InputMessageContent: tgbotapi.InputTextMessageContent{
			Text:      inlineMessageText(tr("inline_loading", lang), candidate),
			ParseMode: "HTML",
		},
		ReplyMarkup: &markup,
	}
}

// answerInline answers an inline query. The button above the results opens the bot chat, and its
// text is also the hint of why the list is empty.
func (a *app) answerInline(queryID string, results []interface{}, button string) {
	if results == nil {
		results = make([]interface{}, 0)
	}
	config := tgbotapi.InlineConfig{
		InlineQueryID:     queryID,
		Results:           results,
		CacheTime:         1,
		IsPersonal:        true,
		SwitchPMText:      button,
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
	lang := a.inlineLang(chosen.From)
	candidate, ok := a.inline.takeCandidate(chosen.ResultID, chosen.From.ID)
	if !ok {
		// The results were shown before a restart or too long ago; the loading text must not stay.
		if chosen.InlineMessageID != "" {
			a.editInlineText(chosen.InlineMessageID, tr("inline_expired", lang))
		}
		return
	}
	fileID := a.inline.cachedFileID(candidate.CacheKey)
	if chosen.InlineMessageID == "" {
		// A cached track went out as audio right away. A loading message always has a cancel
		// button, so it arrives without an ID only when inline feedback is off in BotFather.
		if fileID == "" {
			log.Printf("Inline-результат %s пришёл без inline_message_id: включи inline feedback в BotFather", chosen.ResultID)
		} else if a.store != nil {
			a.store.increment(a.ctx, "cache_hits")
		}
		return
	}
	if fileID != "" {
		// Another request cached the track after the results were shown.
		if a.store != nil {
			a.store.increment(a.ctx, "cache_hits")
		}
		if err := a.editInlineAudio(chosen.InlineMessageID, fileID); err != nil {
			log.Printf("Заменить inline-сообщение на аудио: %v", err)
			a.editInlineText(chosen.InlineMessageID, inlineMessageText(tr("inline_error", lang, "error", html.EscapeString(err.Error())), candidate))
		}
		return
	}
	a.downloadInline(chosen.From.ID, chosen.ResultID, chosen.InlineMessageID, candidate, lang)
}

// downloadInline caches a chosen track through the cache channel and turns its loading message
// into the audio. Only this function edits the message after the choice, so a cancel tapped at
// the last moment cannot overwrite a delivered track.
func (a *app) downloadInline(userID int64, resultID, messageID string, candidate inlineCandidate, lang string) {
	if !a.beginUserDownload(userID) {
		a.editInlineText(messageID, inlineMessageText(tr("user_download_active", lang), candidate))
		return
	}
	defer a.finishUserDownload(userID)
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	a.inline.setActive(resultID, inlineActiveDownload{cancel: cancel, userID: userID, inlineMessageID: messageID})
	defer a.inline.clearActive(resultID)

	pending := pendingURL{URL: candidate.URL, Preview: mediaPreview{SourceID: candidate.SourceID, Extractor: candidate.Extractor}}
	entry, err := a.ensureCachedAudio(ctx, pending, inlineFormat, inlineQuality, nil)
	if err == nil {
		// A cancel tapped while the upload finished still wins.
		err = ctx.Err()
	}
	var oversized fileTooLargeError
	switch {
	case err == nil:
	case a.ctx.Err() != nil:
		a.editInlineText(messageID, inlineMessageText(tr("inline_interrupted", lang), candidate))
		return
	case errors.Is(err, context.Canceled):
		if a.store != nil {
			a.store.increment(a.ctx, "downloads_cancelled")
		}
		a.editInlineText(messageID, inlineMessageText(tr("inline_cancelled", lang), candidate))
		return
	case errors.As(err, &oversized):
		// Too long for the limit is not a bot failure: it is counted, not reported.
		if a.store != nil {
			a.store.increment(a.ctx, "downloads_too_large")
			a.store.increment(a.ctx, "downloads_too_large_mp3")
		}
		a.editInlineText(messageID, inlineMessageText(tr("inline_too_large", lang, "limit", humanSize(a.fileLimit(), lang)), candidate))
		return
	default:
		a.reportDownloadFailure(err.Error(), sourceHost(candidate.URL))
		a.reportError(errorReport{Stage: "inline", UserID: userID, URL: candidate.URL, Format: "mp3 320", Error: err.Error()})
		a.editInlineText(messageID, inlineMessageText(tr("inline_error", lang, "error", html.EscapeString(err.Error())), candidate))
		return
	}
	if entry.Key != candidate.CacheKey && candidate.CacheKey != "" {
		alias := entry
		alias.Key = candidate.CacheKey
		if err := a.store.putCachedAudio(a.ctx, alias); err != nil {
			log.Printf("Сохранить inline file_id: %v", err)
		}
	}
	if err := a.editInlineAudio(messageID, entry.FileID); err != nil {
		log.Printf("Заменить inline-сообщение на аудио: %v", err)
		a.reportError(errorReport{Stage: "inline_send", UserID: userID, URL: candidate.URL, Format: "mp3 320", Error: err.Error()})
		a.editInlineText(messageID, inlineMessageText(tr("inline_error", lang, "error", html.EscapeString(err.Error())), candidate))
		if a.store != nil {
			a.store.increment(a.ctx, "downloads_failed")
		}
		return
	}
	if a.store != nil {
		a.store.increment(a.ctx, "downloads_ok")
	}
}

// editInlineAudio turns an inline message into a cached audio. The file keeps the title,
// performer and duration it was uploaded with, so no caption repeats them.
func (a *app) editInlineAudio(inlineMessageID, fileID string) error {
	edit := tgbotapi.EditMessageMediaConfig{
		BaseEdit: tgbotapi.BaseEdit{InlineMessageID: inlineMessageID, ReplyMarkup: emptyInlineKeyboard()},
		Media:    tgbotapi.NewInputMediaAudio(tgbotapi.FileID(fileID)),
	}
	_, err := requestTelegram(a.bot, edit)
	return err
}

// editInlineText replaces the text of an inline message that is not audio yet and removes its
// cancel button.
func (a *app) editInlineText(inlineMessageID, text string) {
	edit := tgbotapi.EditMessageTextConfig{
		BaseEdit:  tgbotapi.BaseEdit{InlineMessageID: inlineMessageID, ReplyMarkup: emptyInlineKeyboard()},
		Text:      text,
		ParseMode: "HTML",
	}
	if _, err := requestTelegram(a.bot, edit); err != nil {
		log.Printf("Обновить inline-сообщение: %v", err)
	}
}

func emptyInlineKeyboard() *tgbotapi.InlineKeyboardMarkup {
	return &tgbotapi.InlineKeyboardMarkup{InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{}}
}

// handleInlineCancel only stops the download; the download itself reports the cancellation.
func (a *app) handleInlineCancel(callback *tgbotapi.CallbackQuery) {
	if a.inline == nil || callback.From == nil || callback.InlineMessageID == "" {
		return
	}
	a.inline.cancelDownload(strings.TrimPrefix(callback.Data, "inline_cancel:"), callback.From.ID, callback.InlineMessageID)
}
