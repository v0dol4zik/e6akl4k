package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const testLastfmKey = "0123456789abcdef0123456789abcdef"

func TestParseLastfmUserAcceptsNamesAndProfileLinks(t *testing.T) {
	for input, want := range map[string]string{
		"rj":                                  "rj",
		" @RJ ":                               "RJ",
		"https://www.last.fm/user/Some_One":   "Some_One",
		"last.fm/user/dj-x/library?page=2":    "dj-x",
		"https://ru.last.fm/user/RJ/":         "RJ",
		"http://last.fm/user/abc":             "abc",
		"https://evil.test/last.fm/user/rj":   "",
		"https://last.fm.evil.test/user/rj":   "",
		"https://www.last.fm/music/Daft+Punk": "",
		"1abc":                                "",
		"a":                                   "",
		"../../2.0":                           "",
		"sixteen_chars_xx":                    "",
	} {
		got, ok := parseLastfmUser(input)
		if want == "" && ok || want != "" && (!ok || got != want) {
			t.Errorf("parseLastfmUser(%q) = %q, %v; want %q", input, got, ok, want)
		}
	}
}

// lastfmFake answers API methods by name and records every query.
type lastfmFake struct {
	mu      sync.Mutex
	queries []url.Values
	status  int
	bodies  map[string]string
	err     func(*http.Request) error
}

func (f *lastfmFake) Do(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.queries = append(f.queries, request.URL.Query())
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err(request)
	}
	if request.URL.Scheme+"://"+request.URL.Host+request.URL.Path != lastfmAPI {
		return nil, fmt.Errorf("unexpected endpoint %s", request.URL.Redacted())
	}
	recorder := httptest.NewRecorder()
	body, ok := f.bodies[request.URL.Query().Get("method")]
	switch {
	case f.status != 0:
		recorder.WriteHeader(f.status)
	case !ok:
		recorder.WriteHeader(http.StatusNotFound)
		body = `{"error":6,"message":"User not found"}`
	}
	fmt.Fprint(recorder, body)
	return recorder.Result(), nil
}

func (f *lastfmFake) install(t *testing.T) {
	t.Helper()
	old := makeResolverClient
	makeResolverClient = func() httpDoer { return f }
	t.Cleanup(func() { makeResolverClient = old })
}

func (f *lastfmFake) lastQuery() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		return nil
	}
	return f.queries[len(f.queries)-1]
}

const lastfmRecentBody = `{"recenttracks":{"track":[
{"artist":{"#text":"Daft Punk"},"name":"One More Time","@attr":{"nowplaying":"true"}},
{"artist":{"#text":"Daft Punk"},"name":"Aerodynamic","date":{"uts":"1"}},
{"artist":{"#text":"Justice"},"name":"D.A.N.C.E.","date":{"uts":"2"}},
{"artist":{"#text":""},"name":"","date":{"uts":"3"}},
{"artist":{"#text":"Air"},"name":"La Femme d'Argent","date":{"uts":"4"}}
],"@attr":{"user":"RJ"}}}`

func TestLastfmTracklistParsesListsAndTrimsNowPlaying(t *testing.T) {
	fake := &lastfmFake{bodies: map[string]string{
		"user.getrecenttracks": lastfmRecentBody,
		"user.getlovedtracks":  `{"lovedtracks":{"track":{"artist":{"name":"Air"},"name":"Playground Love"}}}`,
		"user.gettoptracks":    `{"toptracks":{"track":[{"artist":"Moderat","name":"A New Error","playcount":"42"}]}}`,
	}}
	fake.install(t)
	application := &app{cfg: config{LastfmAPIKey: testLastfmKey}}
	recent, _ := lastfmListFor("recent:10")
	recent.Limit = 3
	entries, err := application.lastfmTracklist(context.Background(), "RJ", recent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || !entries[0].NowPlaying || entries[1].NowPlaying || entries[2].Track.line() != "Justice - D.A.N.C.E." {
		t.Fatalf("recent entries = %#v", entries)
	}
	query := fake.lastQuery()
	if query.Get("method") != "user.getrecenttracks" || query.Get("user") != "RJ" || query.Get("limit") != "10" || query.Get("format") != "json" || query.Get("api_key") != testLastfmKey {
		t.Fatalf("query = %v", query)
	}

	loved, _ := lastfmListFor("loved")
	entries, err = application.lastfmTracklist(context.Background(), "RJ", loved)
	if err != nil || len(entries) != 1 || entries[0].Track.line() != "Air - Playground Love" {
		t.Fatalf("a single loved track must decode from an object: %#v, %v", entries, err)
	}
	top, _ := lastfmListFor("top:1month")
	entries, err = application.lastfmTracklist(context.Background(), "RJ", top)
	if err != nil || len(entries) != 1 || entries[0].Track.line() != "Moderat - A New Error" || entries[0].PlayCount != "42" {
		t.Fatalf("top entries = %#v, %v", entries, err)
	}
	if query := fake.lastQuery(); query.Get("period") != "1month" || query.Get("limit") != "25" {
		t.Fatalf("top query = %v", query)
	}
	if text := lastfmListText("RJ", top, entries, "en"); !strings.Contains(text, "1. Moderat — A New Error · 42×") || !strings.Contains(text, "🏆 <b>RJ</b> · top of the month") {
		t.Fatalf("list text = %q", text)
	}
}

func TestLastfmCallMapsErrorsAndNeverLeaksTheKey(t *testing.T) {
	application := &app{cfg: config{LastfmAPIKey: testLastfmKey}}
	call := func(fake *lastfmFake) error {
		fake.install(t)
		var target struct{}
		return application.lastfmCall(context.Background(), "user.getinfo", url.Values{"user": {"rj"}}, &target)
	}
	for code, want := range map[int]error{6: errLastfmNotFound, 17: errLastfmPrivate, 29: errLastfmRateLimited} {
		err := call(&lastfmFake{status: http.StatusBadRequest, bodies: map[string]string{"user.getinfo": fmt.Sprintf(`{"error":%d,"message":"x"}`, code)}})
		if !errors.Is(err, want) {
			t.Errorf("code %d: %v", code, err)
		}
	}
	failures := map[string]*lastfmFake{
		"bad key": {status: http.StatusForbidden, bodies: map[string]string{"user.getinfo": `{"error":10,"message":"Invalid API key ` + testLastfmKey + `"}`}},
		"http":    {status: http.StatusBadGateway, bodies: map[string]string{"user.getinfo": `<html>bad gateway</html>`}},
		"json":    {bodies: map[string]string{"user.getinfo": `{"user":`}},
		"transport": {err: func(request *http.Request) error {
			return &url.Error{Op: "Get", URL: request.URL.String(), Err: fmt.Errorf("proxy refused %s", request.URL.String())}
		}},
	}
	for name, fake := range failures {
		err := call(fake)
		if err == nil || strings.Contains(err.Error(), testLastfmKey) {
			t.Errorf("%s: the error must exist and hide the key: %v", name, err)
		}
	}
	if err := call(failures["bad key"]); !strings.Contains(err.Error(), "Invalid API key") || !strings.Contains(err.Error(), "код 10") {
		t.Errorf("bad key error = %v", err)
	}
	if err := call(failures["http"]); err.Error() != "last.fm ответил HTTP 502" {
		t.Errorf("http error = %v", err)
	}
}

func newLastfmTestApp(t *testing.T, telegram *exportTelegram) (*app, *store) {
	t.Helper()
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	application := newExportTestApp(t, telegram)
	application.store = state
	application.cfg.LastfmAPIKey = testLastfmKey
	return application, state
}

func lastfmCommand(userID int64, text string) *tgbotapi.Message {
	command, _, _ := strings.Cut(text, " ")
	return &tgbotapi.Message{
		Text:     text,
		Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: len(command)}},
		From:     &tgbotapi.User{ID: userID},
		Chat:     &tgbotapi.Chat{ID: userID, Type: "private"},
	}
}

func lastfmCallback(userID int64, data string) *tgbotapi.CallbackQuery {
	return &tgbotapi.CallbackQuery{ID: "cb", From: &tgbotapi.User{ID: userID}, Data: data,
		Message: &tgbotapi.Message{MessageID: 5, Chat: &tgbotapi.Chat{ID: userID, Type: "private"}}}
}

func TestLastfmCommandLinksProfileAndShowsLists(t *testing.T) {
	telegram := &exportTelegram{}
	application, state := newLastfmTestApp(t, telegram)
	fake := &lastfmFake{bodies: map[string]string{
		"user.getinfo":         `{"user":{"name":"RJ","playcount":"100"}}`,
		"user.getrecenttracks": lastfmRecentBody,
	}}
	fake.install(t)

	application.handleMessage(lastfmCommand(10, "/lastfm"))
	if calls := telegram.snapshot(); len(calls) != 1 || !strings.Contains(calls[0].text, "/lastfm name") {
		t.Fatalf("without a linked profile /lastfm must explain itself: %#v", calls)
	}
	application.handleMessage(lastfmCommand(10, "/lastfm not a name"))
	if calls := telegram.snapshot(); len(calls) != 1 || !strings.Contains(calls[0].text, "doesn't look like") || len(fake.queries) != 0 {
		t.Fatalf("an invalid name must be refused before calling last.fm: %#v", calls)
	}

	application.handleMessage(lastfmCommand(10, "/lastfm https://www.last.fm/user/rj"))
	calls := telegram.snapshot()
	if len(calls) != 2 || calls[1].method != "editMessageText" || !strings.Contains(calls[1].text, "profile <b>RJ</b> linked") || !strings.Contains(calls[1].markup, "lfm:10:recent:10") {
		t.Fatalf("link calls = %#v", calls)
	}
	if got := state.lastfmUser(context.Background(), 10); got != "RJ" {
		t.Fatalf("stored profile = %q, want the canonical name", got)
	}

	application.handleCallback(lastfmCallback(11, "lfm:10:recent:10"))
	if calls := telegram.snapshot(); len(calls) != 0 {
		t.Fatalf("another user must not drive the menu: %#v", calls)
	}

	application.handleCallback(lastfmCallback(10, "lfm:10:recent:10"))
	calls = telegram.snapshot()
	if len(calls) != 2 || !strings.Contains(calls[0].text, "loading") {
		t.Fatalf("list calls = %#v", calls)
	}
	list := calls[1]
	for _, want := range []string{"🕘 <b>RJ</b> · recent scrobbles", "▶️ 1. Daft Punk — One More Time", "3. Justice — D.A.N.C.E.", "4. Air — La Femme d&#39;Argent"} {
		if !strings.Contains(list.text, want) {
			t.Errorf("list text lacks %q: %q", want, list.text)
		}
	}
	key := between(list.markup, `"export:`, `"`)
	if key == "" || !strings.Contains(list.markup, `"lfs:`+key+`:3"`) || strings.Contains(list.markup, `"lfs:`+key+`:4"`) || !strings.Contains(list.markup, `"lfm:10:menu"`) {
		t.Fatalf("list keyboard = %s", list.markup)
	}

	application.handleCallback(lastfmCallback(10, "export:"+key))
	calls = telegram.snapshot()
	if len(calls) != 1 || calls[0].file != "Daft Punk - One More Time\nDaft Punk - Aerodynamic\nJustice - D.A.N.C.E.\nAir - La Femme d'Argent\n" || calls[0].fileName != "RJ - recent scrobbles.txt" {
		t.Fatalf("export of a last.fm list = %#v", calls)
	}
	application.handleCallback(lastfmCallback(10, "cover:"+key))
	if calls := telegram.snapshot(); len(calls) != 1 || calls[0].text != tr("action_unavailable", "en") {
		t.Fatalf("a last.fm list has no cover: %#v", calls)
	}

	application.handleMessage(lastfmCommand(10, "/lastfm off"))
	if got := state.lastfmUser(context.Background(), 10); got != "" {
		t.Fatalf("profile after unlink = %q", got)
	}
	telegram.snapshot()
	application.handleCallback(lastfmCallback(10, "lfm:10:recent:10"))
	if calls := telegram.snapshot(); len(calls) != 1 || !strings.Contains(calls[0].text, "/lastfm name") {
		t.Fatalf("an old menu of an unlinked profile must explain linking: %#v", calls)
	}
}

func between(text, start, end string) string {
	_, rest, ok := strings.Cut(text, start)
	if !ok {
		return ""
	}
	value, _, _ := strings.Cut(rest, end)
	return value
}

func TestLastfmPickSearchesTheTrackOnYouTube(t *testing.T) {
	telegram := &exportTelegram{}
	application, _ := newLastfmTestApp(t, telegram)
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "fake-yt-dlp")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" +
		`printf '%s' '{"entries":[{"id":"video-id","title":"Aerodynamic","uploader":"Daft Punk","duration":212,"url":"video-id"}]}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	application.downloader.bin = bin
	key, err := application.storeURL(pendingURL{ChatID: 10, UserID: 10, Preview: mediaPreview{Title: "RJ - loved tracks", IsPlaylist: true,
		Tracks: []exportTrack{{Artist: "Air", Title: "Playground Love"}, {Artist: "Daft Punk", Title: "Aerodynamic"}}}})
	if err != nil {
		t.Fatal(err)
	}

	application.handleCallback(lastfmCallback(11, "lfs:"+key+":1"))
	application.handleCallback(lastfmCallback(10, "lfs:"+key+":9"))
	if calls := telegram.snapshot(); len(calls) != 2 || calls[0].chatID != "11" || calls[1].chatID != "10" {
		t.Fatalf("foreign or out-of-range picks must be refused: %#v", calls)
	}
	application.handleCallback(lastfmCallback(10, "lfs:"+key+":1"))
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), ":Daft Punk - Aerodynamic\n") {
		t.Fatalf("search args = %q", args)
	}
	if calls := telegram.snapshot(); len(calls) != 2 || !strings.Contains(calls[1].markup, `"pick:`) {
		t.Fatalf("pick calls = %#v", calls)
	}
}

func TestLastfmIsHiddenWithoutAKey(t *testing.T) {
	telegram := &exportTelegram{}
	application, _ := newLastfmTestApp(t, telegram)
	for _, lang := range languageOrder {
		if help := application.guideText("help", lang); !strings.Contains(help, "/lastfm") || len([]rune(help)) > 4096 {
			t.Errorf("%s help with last.fm: %d runes", lang, len([]rune(help)))
		}
	}
	application.cfg.LastfmAPIKey = ""
	if help := application.guideText("help", "en"); strings.Contains(help, "/lastfm") {
		t.Error("help must not mention /lastfm without a key")
	}
	application.handleMessage(lastfmCommand(10, "/lastfm rj"))
	if calls := telegram.snapshot(); len(calls) != 1 || !strings.Contains(calls[0].text, "isn't configured") {
		t.Fatalf("calls = %#v", calls)
	}
}
