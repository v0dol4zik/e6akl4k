package main

import (
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func languageKeyboard() *tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(languageOrder))
	for _, code := range languageOrder {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(languages[code], "setlang:"+code)))
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

func formatKeyboard(key, lang string) *tgbotapi.InlineKeyboardMarkup {
	buttons := [][2]string{
		{tr("btn_mp3_best", lang), "dl:mp3:best:" + key},
		{tr("btn_mp3_128", lang), "dl:mp3:128:" + key},
		{tr("btn_mp3_320", lang), "dl:mp3:320:" + key},
		{tr("btn_flac", lang), "dl:flac:best:" + key},
		{tr("btn_m4a", lang), "dl:m4a:best:" + key},
		{tr("btn_ogg", lang), "dl:ogg:best:" + key},
		{tr("btn_cancel", lang), "cancel:" + key},
	}
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(buttons))
	for _, button := range buttons {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(button[0], button[1])))
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

func deliveryKeyboard(key, format, quality, lang string) *tgbotapi.InlineKeyboardMarkup {
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_zip_yes", lang), "delivery:zip:"+format+":"+quality+":"+key)),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_zip_no", lang), "delivery:individual:"+format+":"+quality+":"+key)),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_cancel", lang), "cancel:"+key)),
	)
	return &markup
}

func downloadCancelKeyboard(key, lang string) *tgbotapi.InlineKeyboardMarkup {
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_cancel", lang), "cancel_download:"+key)),
	)
	return &markup
}

func rangeKeyboard(key string, count, limit int, lang string) *tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, 10)
	effective := min(count, limit)
	if count <= limit {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_range_all", lang), "range:all:"+key)))
	}
	if effective >= 10 {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_range_10", lang), "range:10:"+key)))
	}
	if effective >= 25 {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_range_25", lang), "range:25:"+key)))
	}
	if count > limit {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_range_limit", lang, "limit", strconv.Itoa(limit)), "range:limit:"+key)))
	}
	var rangeRow []tgbotapi.InlineKeyboardButton
	for start := 11; start <= effective; start += 10 {
		end := min(start+9, effective)
		label := tr("btn_range_custom", lang, "start", strconv.Itoa(start), "end", strconv.Itoa(end))
		rangeRow = append(rangeRow, tgbotapi.NewInlineKeyboardButtonData(label, "range:"+strconv.Itoa(start)+"-"+strconv.Itoa(end)+":"+key))
		if len(rangeRow) == 2 {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(rangeRow...))
			rangeRow = nil
		}
	}
	if len(rangeRow) > 0 {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(rangeRow...))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_cancel", lang), "cancel:"+key)))
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

func validDownloadOption(format, quality string) bool {
	switch strings.ToLower(format) {
	case "mp3":
		return quality == "best" || quality == "128" || quality == "320"
	case "flac", "m4a", "ogg":
		return quality == "best"
	default:
		return false
	}
}

func telegramAudioFormat(format string) bool {
	format = strings.ToLower(format)
	return format == "mp3" || format == "m4a"
}

func searchKeyboard(keys []string, candidates []inlineCandidate, lang string) *tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(keys))
	for i, key := range keys {
		marker := "≈"
		switch candidates[i].Match {
		case "exact":
			marker = "✅"
		case "variant":
			marker = "⚠️"
		}
		source := "YT"
		if candidates[i].Extractor == "octave" {
			source = "Octave"
		}
		label := marker + " " + candidates[i].Title
		if candidates[i].Artist != "" {
			label = candidates[i].Artist + " — " + label
		}
		if candidates[i].Duration != "" {
			label += " · " + candidates[i].Duration
		}
		label += " · " + source
		label = shortenRunes(label, 58)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(label, "pick:"+key)))
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

func formatETA(duration time.Duration, lang string) string {
	if duration < time.Minute {
		return tr("eta_less_minute", lang)
	}
	minutes := int(duration / time.Minute)
	if minutes < 60 {
		return tr("eta_minutes", lang, "count", strconv.Itoa(minutes))
	}
	return tr("eta_hours_minutes", lang, "hours", strconv.Itoa(minutes/60), "minutes", strconv.Itoa(minutes%60))
}

func detectURL(text string) string {
	rawURL := urlPattern.FindString(text)
	if rawURL == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(rawURL), "http://") && !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		rawURL = "https://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || !allowedHost(parsed.Hostname()) {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if (host == "music.octavestreaming.com" || host == "api.octavestreaming.com") && parsed.Scheme != "https" {
		return ""
	}
	return rawURL
}

func allowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "music.octavestreaming.com" || host == "api.octavestreaming.com" {
		return true
	}
	for _, domain := range []string{"youtube.com", "youtu.be", "spotify.com", "soundcloud.com", "music.apple.com", "deezer.com", "tidal.com", "bandcamp.com", "vk.com", "ok.ru", "mixcloud.com", "audiomack.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	parts := strings.Split(host, ".")
	if len(parts) != 3 || parts[0] != "music" || parts[1] != "yandex" {
		return false
	}
	for _, tld := range []string{"ru", "by", "kz", "uz", "com"} {
		if parts[2] == tld {
			return true
		}
	}
	return false
}

func humanSize(size int64, lang string) string {
	units := sizeUnits[lang]
	if len(units) == 0 {
		units = sizeUnits[defaultLang]
	}
	value := float64(size)
	for _, unit := range units[:len(units)-1] {
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%.1f %s", value, units[len(units)-1])
}

func buildCaption(result downloadResult, size int64, format, lang string, index, total int) string {
	title := shortenRunes(firstNonEmpty(result.Title, strings.TrimSuffix(filepath.Base(result.FilePath), filepath.Ext(result.FilePath))), maxTitleLength)
	lines := []string{"<b>" + html.EscapeString(title) + "</b>"}
	if result.Artist != "" {
		lines = append(lines, "👤 "+html.EscapeString(shortenRunes(result.Artist, maxTitleLength)))
	}
	if result.Duration != "" {
		lines = append(lines, "⏱ "+html.EscapeString(result.Duration))
	}
	lines = append(lines, "📦 "+humanSize(size, lang)+" | "+strings.ToUpper(format))
	if total > 1 {
		lines = append(lines, fmt.Sprintf("[%d/%d]", index, total))
	}
	return strings.Join(lines, "\n")
}

func shortenRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func allFailed(results []downloadResult) bool {
	for _, result := range results {
		if result.Error == "" {
			return false
		}
	}
	return true
}
