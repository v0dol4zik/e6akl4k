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

// downloadOptions lists the selectable format/quality pairs in keyboard order.
var downloadOptions = []struct {
	labelKey, format, quality string
}{
	{"btn_mp3_best", "mp3", "best"},
	{"btn_mp3_128", "mp3", "128"},
	{"btn_mp3_320", "mp3", "320"},
	{"btn_flac", "flac", "best"},
	{"btn_m4a", "m4a", "best"},
	{"btn_ogg", "ogg", "best"},
}

func formatKeyboard(key, lang string) *tgbotapi.InlineKeyboardMarkup {
	buttons := make([][2]string, 0, len(downloadOptions)+1)
	for _, option := range downloadOptions {
		buttons = append(buttons, [2]string{tr(option.labelKey, lang), "dl:" + option.format + ":" + option.quality + ":" + key})
	}
	buttons = append(buttons, [2]string{tr("btn_cancel", lang), "cancel:" + key})
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(buttons))
	for _, button := range buttons {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(button[0], button[1])))
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

// settingsKeyboard offers the formatKeyboard options as defaults plus "ask each time".
func settingsKeyboard(lang string) *tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(downloadOptions)+1)
	for _, option := range downloadOptions {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(option.labelKey, lang), "pref:"+option.format+":"+option.quality)))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("settings_ask_each_time", lang), "pref:ask")))
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

// historyLabel renders one /history entry as "artist — title" or a fallback on the cache key.
func historyLabel(item historyItem) string {
	title := strings.TrimSpace(item.Title)
	artist := strings.TrimSpace(item.Artist)
	switch {
	case title != "" && artist != "":
		return artist + " — " + title
	case title != "":
		return title
	case artist != "":
		return artist
	default:
		return item.CacheKey
	}
}

func historyText(items []historyItem, lang string) string {
	lines := make([]string, 0, len(items)+1)
	lines = append(lines, tr("history_title", lang))
	for index, item := range items {
		format := strings.ToUpper(strings.SplitN(item.Format, ":", 2)[0])
		lines = append(lines, fmt.Sprintf("%d. %s · %s", index+1, html.EscapeString(historyLabel(item)), html.EscapeString(format)))
	}
	return strings.Join(lines, "\n")
}

// historyKeyboard offers one "hist:<id>" button per entry plus a "hist:clear" button.
func historyKeyboard(items []historyItem, lang string) *tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(items)+1)
	for index, item := range items {
		label := shortenRunes(fmt.Sprintf("%d. %s", index+1, historyLabel(item)), 40)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(label, "hist:"+strconv.FormatInt(item.ID, 10))))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_history_clear", lang), "hist:clear")))
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

// preferenceLabel renders a stored default; an empty format means "ask each time".
func preferenceLabel(format, quality, lang string) string {
	if format == "" {
		return tr("settings_ask_each_time", lang)
	}
	for _, option := range downloadOptions {
		if option.format == format && option.quality == quality {
			return tr(option.labelKey, lang)
		}
	}
	return strings.ToUpper(format) + " " + quality
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

// detectURL returns the first valid media link found in text, or an empty string.
func detectURL(text string) string {
	urls := detectURLs(text, 1)
	if len(urls) == 0 {
		return ""
	}
	return urls[0]
}

// detectURLs returns every valid media link in text in order of appearance,
// without duplicates. A max of zero or less means no cap.
func detectURLs(text string, max int) []string {
	var urls []string
	seen := make(map[string]struct{})
	for _, match := range urlPattern.FindAllString(text, -1) {
		normalized := normalizeDetectedURL(match)
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		urls = append(urls, normalized)
		if max > 0 && len(urls) >= max {
			break
		}
	}
	return urls
}

// normalizeDetectedURL adds a scheme when missing and rejects links with user info or unknown hosts.
func normalizeDetectedURL(rawURL string) string {
	if !strings.HasPrefix(strings.ToLower(rawURL), "http://") && !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		rawURL = "https://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || !allowedHost(parsed.Hostname()) {
		return ""
	}
	return rawURL
}

func allowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
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
