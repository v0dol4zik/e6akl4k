package main

import (
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const maxTooLargeLines = 10

// fileTooLargeError reports a track that does not fit the Telegram upload limit in the chosen format.
type fileTooLargeError struct {
	Result downloadResult
	Limit  int64
}

func (e fileTooLargeError) Error() string {
	return fmt.Sprintf("«%s» — %s: больше лимита Telegram %s", e.Result.Title, humanSize(e.Result.Size, defaultLang), humanSize(e.Limit, defaultLang))
}

// lighterOptions lists the download options lighter than format:quality whose estimated size for a
// track of the given length stays safely under limit. Without a known length every lighter option is
// offered.
func lighterOptions(seconds int, limit int64, format, quality string) []downloadOption {
	current := audioRate(format, quality)
	var options []downloadOption
	for _, option := range downloadOptions {
		if audioRate(option.format, option.quality) >= current {
			continue
		}
		if seconds > 0 && float64(estimateAudioSize(seconds, option.format, option.quality)) > float64(limit)*sizeEstimateMargin {
			continue
		}
		options = append(options, option)
	}
	return options
}

func tooLargeResults(results []downloadResult) []downloadResult {
	var tooLarge []downloadResult
	for _, result := range results {
		if result.TooLarge {
			tooLarge = append(tooLarge, result)
		}
	}
	return tooLarge
}

// formatName renders a format for messages: "FLAC", "MP3 320 kbps".
func formatName(format, quality string) string {
	name := strings.ToUpper(format)
	if quality != "" && quality != "best" {
		name += " " + quality + " kbps"
	}
	return name
}

// tooLargeSize renders the file size, or the estimate of a track skipped before download.
func tooLargeSize(result downloadResult, lang string) string {
	if result.FilePath == "" {
		return "≈ " + humanSize(result.Size, lang)
	}
	return humanSize(result.Size, lang)
}

// offerLighterFormats is the answer to tracks that do not fit the Telegram upload limit in the chosen
// format: it is not an error, so nothing goes to the error channel. It counts them, lists them, and
// offers the lighter formats that fit as buttons on a new pending entry: single is the original
// one-track request, reused as is; otherwise the tracks' own links become a batch with the same
// delivery mode.
func (a *app) offerLighterFormats(chatID, userID int64, tracks []downloadResult, format, quality, delivery, lang string, single *pendingURL) {
	if len(tracks) == 0 {
		return
	}
	if a.store != nil {
		a.store.incrementBy(a.ctx, "downloads_too_large", int64(len(tracks)))
		a.store.incrementBy(a.ctx, "downloads_too_large_"+strings.ToLower(format), int64(len(tracks)))
	}
	limit := a.fileLimit()
	longest := 0
	for _, track := range tracks {
		longest = max(longest, track.DurationSeconds)
	}
	log.Printf("Не влезло в лимит Telegram: user_id=%d format=%s:%s tracks=%d longest=%ds limit=%d", userID, format, quality, len(tracks), longest, limit)

	var text string
	if len(tracks) == 1 {
		track := tracks[0]
		text = tr("too_large_track", lang,
			"title", html.EscapeString(shortenRunes(firstNonEmpty(track.Title, "Unknown"), maxTitleLength)),
			"duration", html.EscapeString(track.Duration),
			"format", formatName(format, quality),
			"size", tooLargeSize(track, lang),
			"limit", humanSize(limit, lang))
	} else {
		lines := make([]string, 0, min(len(tracks), maxTooLargeLines)+1)
		for i, track := range tracks {
			if i == maxTooLargeLines {
				lines = append(lines, "… +"+strconv.Itoa(len(tracks)-i))
				break
			}
			label := html.EscapeString(shortenRunes(firstNonEmpty(track.Title, "Unknown"), 80))
			if track.Artist != "" {
				label = html.EscapeString(shortenRunes(track.Artist, 60)) + " — " + label
			}
			if track.Duration != "" {
				label += " · " + html.EscapeString(track.Duration)
			}
			lines = append(lines, strconv.Itoa(i+1)+". "+label+" · "+tooLargeSize(track, lang))
		}
		text = tr("too_large_tracks", lang,
			"count", strconv.Itoa(len(tracks)),
			"format", formatName(format, quality),
			"limit", humanSize(limit, lang),
			"list", strings.Join(lines, "\n"))
	}

	options := lighterOptions(longest, limit, format, quality)
	retry, ok := a.lighterFormatRequest(chatID, userID, tracks, delivery, single)
	if len(options) == 0 || !ok {
		if len(options) == 0 {
			text += "\n" + tr("too_large_no_option", lang)
		}
		a.sendText(chatID, text, "HTML", nil)
		return
	}
	key, err := a.storeURL(retry)
	if err != nil {
		log.Printf("save lighter-format request: %v", err)
		a.sendText(chatID, text, "HTML", nil)
		return
	}
	a.sendText(chatID, text+"\n"+tr("too_large_choose", lang), "HTML", lighterFormatKeyboard(key, options, lang))
}

// lighterFormatRequest builds the pending entry the lighter-format buttons download. Tracks without a
// page link of an allowed host are left out; ok is false when none is left.
func (a *app) lighterFormatRequest(chatID, userID int64, tracks []downloadResult, delivery string, single *pendingURL) (pendingURL, bool) {
	if single != nil && len(tracks) == 1 {
		request := *single
		request.ChatID, request.UserID, request.ExpiresAt = chatID, userID, time.Time{}
		return request, request.URL != ""
	}
	request := pendingURL{ChatID: chatID, UserID: userID, Delivery: "individual"}
	if delivery == "zip" {
		request.Delivery = "zip"
	}
	for _, track := range tracks {
		link := normalizeDetectedURL(track.URL)
		if link == "" {
			continue
		}
		request.Batch = append(request.Batch, link)
		request.BatchPreviews = append(request.BatchPreviews, mediaPreview{
			URL: link, Title: track.Title, Artist: track.Artist, Duration: track.Duration,
			DurationSeconds: track.DurationSeconds, TrackCount: 1,
		})
	}
	switch len(request.Batch) {
	case 0:
		return pendingURL{}, false
	case 1:
		// One link is an ordinary track request: it goes through the file_id cache channel.
		request.URL, request.Preview, request.Delivery = request.Batch[0], request.BatchPreviews[0], ""
		request.Batch, request.BatchPreviews = nil, nil
		return request, true
	}
	request.URL = request.Batch[0]
	request.Preview = mediaPreview{URL: request.URL, TrackCount: len(request.Batch), Extractor: "batch"}
	for _, preview := range request.BatchPreviews {
		request.Preview.DurationSeconds += preview.DurationSeconds
	}
	return request, true
}

func lighterFormatKeyboard(key string, options []downloadOption, lang string) *tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(options)+1)
	for _, option := range options {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(option.labelKey, lang), "dl:"+option.format+":"+option.quality+":"+key)))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_cancel", lang), "cancel:"+key)))
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}
