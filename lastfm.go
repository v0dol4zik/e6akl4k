package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	lastfmAPI = "https://ws.audioscrobbler.com/2.0/"
	// lastfmTimeout bounds one /lastfm request; the API is only read, never written.
	lastfmTimeout     = 15 * time.Second
	maxLastfmResponse = 4 << 20
	// lastfmLineLimit keeps a numbered list readable on a phone screen.
	lastfmLineLimit = 70
)

var (
	errLastfmNotFound    = errors.New("lastfm user not found")
	errLastfmPrivate     = errors.New("lastfm profile is private")
	errLastfmRateLimited = errors.New("lastfm rate limited")
	// lastfmName is the last.fm username rule: 2-15 characters starting with a letter.
	lastfmName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,14}$`)
)

// lastfmList is one list the /lastfm menu offers. ID is the callback suffix.
type lastfmList struct {
	ID      string
	Method  string
	Params  url.Values
	Limit   int
	Emoji   string
	NameKey string
}

func lastfmListFor(id string) (lastfmList, bool) {
	switch id {
	case "recent:10", "recent:25", "recent:50":
		limit, _ := strconv.Atoi(strings.TrimPrefix(id, "recent:"))
		return lastfmList{ID: id, Method: "user.getrecenttracks", Params: url.Values{"limit": {strconv.Itoa(limit)}}, Limit: limit, Emoji: "🕘", NameKey: "lastfm_name_recent"}, true
	case "loved":
		return lastfmList{ID: id, Method: "user.getlovedtracks", Params: url.Values{"limit": {"25"}}, Limit: 25, Emoji: "❤️", NameKey: "lastfm_name_loved"}, true
	case "top:7day", "top:1month":
		period, emoji := strings.TrimPrefix(id, "top:"), "🔥"
		if period == "1month" {
			emoji = "🏆"
		}
		return lastfmList{ID: id, Method: "user.gettoptracks", Params: url.Values{"period": {period}, "limit": {"25"}}, Limit: 25, Emoji: emoji, NameKey: "lastfm_name_top_" + period}, true
	}
	return lastfmList{}, false
}

// lastfmTrack covers the track objects of the recent, loved and top lists.
type lastfmTrack struct {
	Name      string       `json:"name"`
	Artist    lastfmArtist `json:"artist"`
	PlayCount string       `json:"playcount"`
	Attr      struct {
		NowPlaying string `json:"nowplaying"`
	} `json:"@attr"`
}

// lastfmArtist is {"#text": ...} in recent tracks, {"name": ...} elsewhere and a plain string
// in some methods.
type lastfmArtist struct {
	Name string `json:"name"`
	Text string `json:"#text"`
}

func (a *lastfmArtist) UnmarshalJSON(data []byte) error {
	if data = bytes.TrimSpace(data); len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &a.Name)
	}
	type plain lastfmArtist
	return json.Unmarshal(data, (*plain)(a))
}

// lastfmTracks accepts both an array and the single object the API returns for one track.
type lastfmTracks []lastfmTrack

func (t *lastfmTracks) UnmarshalJSON(data []byte) error {
	if data = bytes.TrimSpace(data); len(data) > 0 && data[0] == '{' {
		var one lastfmTrack
		if err := json.Unmarshal(data, &one); err != nil {
			return err
		}
		*t = lastfmTracks{one}
		return nil
	}
	var many []lastfmTrack
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*t = many
	return nil
}

type lastfmTrackPage struct {
	Track lastfmTracks `json:"track"`
}

// lastfmEntry is one shown line; the search query is Track.line().
type lastfmEntry struct {
	Track      exportTrack
	NowPlaying bool
	PlayCount  string
}

// parseLastfmUser accepts "name", "@name" or a last.fm/user/name link.
func parseLastfmUser(text string) (string, bool) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "@")
	if strings.Contains(strings.ToLower(text), "last.fm/") {
		if !strings.Contains(text, "://") {
			text = "https://" + text
		}
		parsed, err := url.Parse(text)
		if err != nil {
			return "", false
		}
		host := strings.ToLower(parsed.Hostname())
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if (host != "last.fm" && !strings.HasSuffix(host, ".last.fm")) || len(segments) < 2 || segments[0] != "user" {
			return "", false
		}
		text = segments[1]
	}
	return text, lastfmName.MatchString(text)
}

// lastfmCall runs one read-only API method. Transport errors carry the request URL and with it
// the API key, so they are unwrapped and the key is redacted from every returned error.
func (a *app) lastfmCall(ctx context.Context, method string, params url.Values, target any) error {
	key := a.cfg.LastfmAPIKey
	query := url.Values{"method": {method}, "api_key": {key}, "format": {"json"}}
	for name, values := range params {
		query[name] = values
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, lastfmAPI+"?"+query.Encode(), nil)
	if err != nil {
		return redactLastfmKey(err, key)
	}
	request.Header.Set("Accept", "application/json")
	response, err := makeResolverClient().Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return redactLastfmKey(err, key)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxLastfmResponse))
	if err != nil {
		return redactLastfmKey(err, key)
	}
	var apiErr struct {
		Code    int    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Code != 0 {
		switch apiErr.Code {
		case 6:
			return errLastfmNotFound
		case 17:
			return errLastfmPrivate
		case 29:
			return errLastfmRateLimited
		}
		return redactLastfmKey(fmt.Errorf("last.fm: %s (код %d)", firstNonEmpty(apiErr.Message, "ошибка API"), apiErr.Code), key)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("last.fm ответил HTTP %d", response.StatusCode)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return redactLastfmKey(fmt.Errorf("last.fm вернул неожиданный ответ: %w", err), key)
	}
	return nil
}

// isTimeoutError recognizes the client timeout, whose error is not context.DeadlineExceeded.
func isTimeoutError(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func redactLastfmKey(err error, key string) error {
	if err == nil || key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[api_key]"))
}

// lastfmProfile checks that a profile exists and returns its canonical spelling.
func (a *app) lastfmProfile(ctx context.Context, name string) (string, error) {
	var response struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := a.lastfmCall(ctx, "user.getinfo", url.Values{"user": {name}}, &response); err != nil {
		return "", err
	}
	if !lastfmName.MatchString(response.User.Name) {
		return "", errLastfmNotFound
	}
	return response.User.Name, nil
}

// lastfmTracklist fetches one list. A track playing right now comes on top of the recent
// scrobbles, so the result is trimmed back to the requested size.
func (a *app) lastfmTracklist(ctx context.Context, name string, list lastfmList) ([]lastfmEntry, error) {
	params := url.Values{"user": {name}}
	for key, values := range list.Params {
		params[key] = values
	}
	var response struct {
		Recent lastfmTrackPage `json:"recenttracks"`
		Loved  lastfmTrackPage `json:"lovedtracks"`
		Top    lastfmTrackPage `json:"toptracks"`
	}
	if err := a.lastfmCall(ctx, list.Method, params, &response); err != nil {
		return nil, err
	}
	var entries []lastfmEntry
	for _, page := range []lastfmTrackPage{response.Recent, response.Loved, response.Top} {
		for _, track := range page.Track {
			entry := lastfmEntry{
				Track:      exportTrack{Artist: strings.TrimSpace(firstNonEmpty(track.Artist.Name, track.Artist.Text)), Title: strings.TrimSpace(track.Name)},
				NowPlaying: track.Attr.NowPlaying == "true",
				PlayCount:  track.PlayCount,
			}
			if entry.Track.line() != "" && len(entries) < list.Limit {
				entries = append(entries, entry)
			}
		}
	}
	return entries, nil
}

func (a *app) lastfmEnabled() bool {
	return a.cfg.LastfmAPIKey != "" && a.store != nil
}

func lastfmMenuText(name, lang string) string {
	return tr("lastfm_menu", lang, "user", html.EscapeString(name))
}

// lastfmMenuKeyboard carries the owner's ID in every button, so that a menu posted in a group
// is not driven by other members.
func lastfmMenuKeyboard(userID int64, lang string) *tgbotapi.InlineKeyboardMarkup {
	prefix := "lfm:" + strconv.FormatInt(userID, 10) + ":"
	button := func(label, action string) tgbotapi.InlineKeyboardButton {
		return tgbotapi.NewInlineKeyboardButtonData(label, prefix+action)
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(button(tr("btn_lastfm_recent", lang, "count", "10"), "recent:10"), button("🕘 25", "recent:25"), button("🕘 50", "recent:50")),
		tgbotapi.NewInlineKeyboardRow(button(tr("btn_lastfm_loved", lang), "loved")),
		tgbotapi.NewInlineKeyboardRow(button(tr("btn_lastfm_top_7day", lang), "top:7day"), button(tr("btn_lastfm_top_1month", lang), "top:1month")),
		tgbotapi.NewInlineKeyboardRow(button(tr("btn_lastfm_unlink", lang), "unlink")),
	)
	return &markup
}

func lastfmListText(name string, list lastfmList, entries []lastfmEntry, lang string) string {
	lines := []string{list.Emoji + " <b>" + html.EscapeString(name) + "</b> · " + tr(list.NameKey, lang)}
	for i, entry := range entries {
		line := strconv.Itoa(i+1) + ". " + html.EscapeString(shortenRunes(strings.ReplaceAll(entry.Track.line(), " - ", " — "), lastfmLineLimit))
		if entry.NowPlaying {
			line = "▶️ " + line
		}
		if entry.PlayCount != "" && entry.PlayCount != "0" {
			line += " · " + html.EscapeString(entry.PlayCount) + "×"
		}
		lines = append(lines, line)
	}
	return strings.Join(append(lines, "", tr("lastfm_pick_hint", lang)), "\n")
}

// lastfmListKeyboard offers one search button per track, the list as .txt through the export
// flow, and a way back to the menu.
func lastfmListKeyboard(key string, userID int64, count int, lang string) *tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, count/5+2)
	var row []tgbotapi.InlineKeyboardButton
	for i := 0; i < count; i++ {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(strconv.Itoa(i+1), "lfs:"+key+":"+strconv.Itoa(i)))
		if len(row) == 5 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr("btn_lastfm_txt", lang), "export:"+key),
		tgbotapi.NewInlineKeyboardButtonData(tr("btn_lastfm_menu", lang), "lfm:"+strconv.FormatInt(userID, 10)+":menu"),
	))
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

// handleLastfmCommand serves /lastfm: without arguments it opens the menu of the linked
// profile, "off" unlinks it, and a name or profile link links a new one.
func (a *app) handleLastfmCommand(message *tgbotapi.Message, lang string) {
	chatID, userID := message.Chat.ID, message.From.ID
	if !a.lastfmEnabled() {
		a.sendText(chatID, tr("lastfm_disabled", lang), "", nil)
		return
	}
	args := strings.TrimSpace(message.CommandArguments())
	switch strings.ToLower(args) {
	case "":
		if name := a.store.lastfmUser(a.ctx, userID); name != "" {
			a.sendText(chatID, lastfmMenuText(name, lang), "HTML", lastfmMenuKeyboard(userID, lang))
		} else {
			a.sendText(chatID, tr("lastfm_usage", lang), "HTML", nil)
		}
		return
	case "off", "unlink", "выкл", "отвязать":
		a.unlinkLastfm(chatID, userID, lang, nil)
		return
	}
	name, ok := parseLastfmUser(args)
	if !ok {
		a.sendText(chatID, tr("lastfm_bad_name", lang), "HTML", nil)
		return
	}
	if !a.allowUserRequest(chatID, userID, lang) {
		return
	}
	status := a.sendText(chatID, tr("lastfm_checking", lang), "HTML", nil)
	ctx, cancel := context.WithTimeout(a.ctx, lastfmTimeout)
	defer cancel()
	canonical, err := a.lastfmProfile(ctx, name)
	if err == nil {
		err = a.store.setLastfmUser(a.ctx, userID, canonical)
	}
	if err != nil {
		a.lastfmFailed(chatID, userID, name, "user.getinfo", lang, status, err)
		return
	}
	a.store.increment(a.ctx, "lastfm_links")
	text := tr("lastfm_linked", lang, "user", html.EscapeString(canonical)) + "\n\n" + lastfmMenuText(canonical, lang)
	if status != nil {
		edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, text, *lastfmMenuKeyboard(userID, lang))
		edit.ParseMode = "HTML"
		if _, err := sendTelegram(a.bot, edit); err == nil {
			return
		}
	}
	a.sendText(chatID, text, "HTML", lastfmMenuKeyboard(userID, lang))
}

func (a *app) unlinkLastfm(chatID, userID int64, lang string, callback *tgbotapi.CallbackQuery) {
	text := tr("lastfm_unlinked", lang)
	if err := a.store.setLastfmUser(a.ctx, userID, ""); err != nil {
		text = tr("lastfm_error", lang, "error", html.EscapeString(err.Error()))
	}
	if callback != nil {
		a.safeEdit(callback, text, "HTML", nil)
		return
	}
	a.sendText(chatID, text, "HTML", nil)
}

// handleLastfmCallback serves "lfm:<owner>:<action>" menu buttons; other users' presses are
// ignored.
func (a *app) handleLastfmCallback(callback *tgbotapi.CallbackQuery) {
	userID, chatID := callback.From.ID, callbackChatID(callback)
	owner, action, ok := strings.Cut(strings.TrimPrefix(callback.Data, "lfm:"), ":")
	if !ok || owner != strconv.FormatInt(userID, 10) {
		return
	}
	lang := a.langOrDefault(userID)
	if !a.lastfmEnabled() {
		a.safeEdit(callback, tr("lastfm_disabled", lang), "", nil)
		return
	}
	name := a.store.lastfmUser(a.ctx, userID)
	if name == "" {
		a.safeEdit(callback, tr("lastfm_usage", lang), "HTML", nil)
		return
	}
	switch action {
	case "menu":
		a.safeEdit(callback, lastfmMenuText(name, lang), "HTML", lastfmMenuKeyboard(userID, lang))
		return
	case "unlink":
		a.unlinkLastfm(chatID, userID, lang, callback)
		return
	}
	list, ok := lastfmListFor(action)
	if !ok || !a.allowUserRequest(chatID, userID, lang) {
		return
	}
	status := a.safeEdit(callback, tr("lastfm_loading", lang), "HTML", nil)
	ctx, cancel := context.WithTimeout(a.ctx, lastfmTimeout)
	defer cancel()
	entries, err := a.lastfmTracklist(ctx, name, list)
	if err != nil {
		a.lastfmFailed(chatID, userID, name, list.Method, lang, status, err)
		return
	}
	if len(entries) == 0 {
		a.replaceStatusText(chatID, status, tr("lastfm_empty", lang))
		return
	}
	tracks := make([]exportTrack, 0, len(entries))
	for _, entry := range entries {
		tracks = append(tracks, entry.Track)
	}
	preview := mediaPreview{Title: name + " - " + tr(list.NameKey, lang), IsPlaylist: true, TrackCount: len(tracks), Tracks: tracks}
	key, err := a.storeURL(pendingURL{ChatID: chatID, UserID: userID, Preview: preview})
	if err != nil {
		a.lastfmFailed(chatID, userID, name, list.Method, lang, status, err)
		return
	}
	a.store.increment(a.ctx, "lastfm_lists")
	text, keyboard := lastfmListText(name, list, entries, lang), lastfmListKeyboard(key, userID, len(entries), lang)
	if status != nil {
		edit := tgbotapi.NewEditMessageTextAndMarkup(status.Chat.ID, status.MessageID, text, *keyboard)
		edit.ParseMode = "HTML"
		if _, err := sendTelegram(a.bot, edit); err == nil {
			return
		}
	}
	a.sendText(chatID, text, "HTML", keyboard)
}

// handleLastfmPick searches YouTube for one track of a shown list ("lfs:<key>:<index>").
func (a *app) handleLastfmPick(callback *tgbotapi.CallbackQuery) {
	userID, chatID := callback.From.ID, callbackChatID(callback)
	lang := a.langOrDefault(userID)
	key, indexText, _ := strings.Cut(strings.TrimPrefix(callback.Data, "lfs:"), ":")
	pending, ok := a.getURL(key, userID, chatID)
	index, err := strconv.Atoi(indexText)
	if !ok || err != nil || index < 0 || index >= len(pending.Preview.Tracks) {
		a.sendText(userID, tr("action_unavailable", lang), "", nil)
		return
	}
	if !a.allowUserRequest(chatID, userID, lang) {
		return
	}
	status := a.sendText(chatID, tr("searching", lang), "HTML", nil)
	a.presentSearchResults(chatID, userID, pending.Preview.Tracks[index].line(), lang, status, false, 0)
}

// lastfmFailed explains a failed last.fm request. A missing or private profile and the API
// rate limit are the user's case; other failures also reach the operator chat.
func (a *app) lastfmFailed(chatID, userID int64, name, method, lang string, status *tgbotapi.Message, err error) {
	var text string
	switch {
	case errors.Is(err, errLastfmNotFound):
		text = tr("lastfm_not_found", lang, "user", html.EscapeString(name))
	case errors.Is(err, errLastfmPrivate):
		text = tr("lastfm_private", lang, "user", html.EscapeString(name))
	case errors.Is(err, errLastfmRateLimited):
		text = tr("lastfm_rate_limited", lang)
	case errors.Is(err, context.DeadlineExceeded) || isTimeoutError(err):
		text = tr("lastfm_timeout", lang)
	default:
		a.reportError(errorReport{Stage: "lastfm", ChatID: chatID, UserID: userID, Query: method + " " + name, Error: err.Error()})
		text = tr("lastfm_error", lang, "error", html.EscapeString(err.Error()))
	}
	a.replaceStatusText(chatID, status, text)
}
