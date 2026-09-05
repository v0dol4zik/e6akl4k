package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestPendingURLIsBoundToOwnerAndChat(t *testing.T) {
	a := newApp(context.Background(), nil, nil)
	want := pendingURL{URL: "https://youtu.be/example", UserID: 10, ChatID: -20}
	key, err := a.storeURL(want)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.popURL(key, 11, -20); ok {
		t.Fatal("another user consumed the URL")
	}
	if _, ok := a.popURL(key, 10, -21); ok {
		t.Fatal("URL was consumed from another chat")
	}
	if got, ok := a.popURL(key, 10, -20); !ok || got.URL != want.URL || got.UserID != want.UserID || got.ChatID != want.ChatID || got.ExpiresAt.IsZero() {
		t.Fatalf("owner could not consume URL: got %#v, ok %v", got, ok)
	}
	if _, ok := a.popURL(key, 10, -20); ok {
		t.Fatal("URL was consumed twice")
	}
}

func TestPendingURLExpires(t *testing.T) {
	a := newApp(context.Background(), nil, nil)
	a.urls["old"] = pendingURL{URL: "https://youtu.be/example", UserID: 10, ChatID: 20, ExpiresAt: time.Now().Add(-time.Second)}
	if _, ok := a.getURL("old", 10, 20); ok {
		t.Fatal("expired URL returned")
	}
	if _, exists := a.urls["old"]; exists {
		t.Fatal("expired URL retained")
	}
}

func TestActiveDownloadIsBoundToOwnerAndChat(t *testing.T) {
	a := newApp(context.Background(), nil, nil)
	a.active["download"] = activeDownload{userID: 10, chatID: -20}
	if _, ok := a.getActiveDownload("download", 11, -20); ok {
		t.Fatal("another user accessed the active download")
	}
	if _, ok := a.getActiveDownload("download", 10, -21); ok {
		t.Fatal("active download was accessed from another chat")
	}
	if _, ok := a.getActiveDownload("download", 10, -20); !ok {
		t.Fatal("owner could not access the active download")
	}
}

func TestPlaylistTrackLimit(t *testing.T) {
	if err := validatePlaylistSize(maxPlaylistTracks); err != nil {
		t.Fatalf("playlist at the limit was rejected: %v", err)
	}
	err := validatePlaylistSize(maxPlaylistTracks + 1)
	var tooLarge playlistTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("unexpected error type: %T (%v)", err, err)
	}
	if tooLarge.Count != 76 || tooLarge.Limit != 75 {
		t.Fatalf("unexpected limit error: %#v", tooLarge)
	}
}

func TestDetectURL(t *testing.T) {
	tests := map[string]string{
		"смотри youtu.be/dQw4w9WgXcQ":                    "https://youtu.be/dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=abc&list=xyz":   "https://www.youtube.com/watch?v=abc&list=xyz",
		"https://music.octavestreaming.com/album/3?t=11": "https://music.octavestreaming.com/album/3?t=11",
		"http://music.octavestreaming.com/album/3?t=11":  "",
		"http://youtube.com@127.0.0.1/private":           "",
		"не ссылка":                                      "",
	}
	for input, want := range tests {
		if got := detectURL(input); got != want {
			t.Errorf("detectURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTranslationFallbackAndValues(t *testing.T) {
	if got := tr("downloaded_count", "unknown", "count", "12"); got != "📦 скачано треков: <b>12</b>. как отправить?" {
		t.Fatalf("unexpected translation: %q", got)
	}
}

func TestFormatETA(t *testing.T) {
	if got := formatETA(30*time.Second, "ru"); got != "меньше минуты" {
		t.Fatalf("formatETA short = %q", got)
	}
	if got := formatETA(3*time.Minute+20*time.Second, "ru"); got != "около 3 мин." {
		t.Fatalf("formatETA minutes = %q", got)
	}
}

func TestBuildCaptionEscapesMetadata(t *testing.T) {
	result := downloadResult{FilePath: "track.mp3", Title: "A < B", Artist: "one & two", Duration: "3:14"}
	caption := buildCaption(result, 1024, "mp3", "ru", 1, 1)
	if strings.Contains(caption, "A < B") || strings.Contains(caption, "one & two") {
		t.Fatalf("caption is not escaped: %q", caption)
	}
	if !strings.Contains(caption, "A &lt; B") || !strings.Contains(caption, "one &amp; two") {
		t.Fatalf("escaped values are missing: %q", caption)
	}
}

func TestCreateZIPKeepsDuplicateTracks(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.mp3")
	second := filepath.Join(dir, "second.mp3")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "tracks.zip")
	results := []downloadResult{
		{FilePath: first, Title: "same", Artist: "artist"},
		{FilePath: second, Title: "same", Artist: "artist"},
	}
	if err := createZIP(archive, results); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) != 2 || reader.File[0].Name == reader.File[1].Name {
		t.Fatalf("duplicate files were not preserved: %#v", reader.File)
	}
}

func TestSplitResultsBySizeCreatesParts(t *testing.T) {
	dir := t.TempDir()
	var results []downloadResult
	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%d.mp3", i))
		if err := os.WriteFile(path, make([]byte, 6), 0o600); err != nil {
			t.Fatal(err)
		}
		results = append(results, downloadResult{FilePath: path})
	}
	parts := splitResultsBySize(results, 12)
	if len(parts) != 3 || len(parts[0]) != 2 || len(parts[2]) != 1 {
		t.Fatalf("parts=%#v", parts)
	}
}

func TestDownloadOptionAndTelegramMediaType(t *testing.T) {
	if !validDownloadOption("mp3", "320") || !validDownloadOption("flac", "best") || validDownloadOption("flac", "320") || validDownloadOption("exe", "best") {
		t.Fatal("invalid download option validation")
	}
	if !telegramAudioFormat("mp3") || !telegramAudioFormat("m4a") || telegramAudioFormat("flac") || telegramAudioFormat("ogg") {
		t.Fatal("invalid Telegram media classification")
	}
}

func TestDeliveryStatusAndError(t *testing.T) {
	if got := deliveryStatus(deliveryReport{Delivered: 2}); got != "delivered" {
		t.Fatalf("delivered status=%q", got)
	}
	partial := deliveryReport{Delivered: 1, Failed: 2}
	if got := deliveryStatus(partial); got != "partial" || !strings.Contains(deliveryError(partial), "2") {
		t.Fatalf("partial status=%q error=%q", got, deliveryError(partial))
	}
	if got := deliveryStatus(deliveryReport{Failed: 1}); got != "delivery_failed" {
		t.Fatalf("failed status=%q", got)
	}
}

func TestPreferenceCallbackStoresDefaultFormat(t *testing.T) {
	var edits []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
		case "editMessageText":
			_ = r.ParseForm()
			edits = append(edits, r.FormValue("text"))
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"},"text":"ok"}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	app := newAppWithServices(context.Background(), bot, nil, state, config{DownloadWorkers: 1, LookupWorkers: 1})
	app.setLang(10, "en")
	callback := func(data string) *tgbotapi.CallbackQuery {
		return &tgbotapi.CallbackQuery{ID: "cb", From: &tgbotapi.User{ID: 10}, Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 10, Type: "private"}}, Data: data}
	}
	app.handleCallback(callback("pref:mp3:320"))
	if format, quality, ok := app.getPreference(10); !ok || format != "mp3" || quality != "320" {
		t.Fatalf("cached preference=%q/%q ok=%v", format, quality, ok)
	}
	if format, quality, ok := state.userPreference(context.Background(), 10); !ok || format != "mp3" || quality != "320" {
		t.Fatalf("stored preference=%q/%q ok=%v", format, quality, ok)
	}
	if lang, ok := state.language(context.Background(), 10); !ok || lang != "en" {
		t.Fatalf("language must survive the preference update: %q ok=%v", lang, ok)
	}
	app.handleCallback(callback("pref:wav:best"))
	if format, _, ok := app.getPreference(10); !ok || format != "mp3" {
		t.Fatalf("invalid option must be ignored: format=%q ok=%v", format, ok)
	}
	app.handleCallback(callback("pref:ask"))
	if _, _, ok := app.getPreference(10); ok {
		t.Fatal("pref:ask must switch back to asking each time")
	}
	if _, _, ok := state.userPreference(context.Background(), 10); ok {
		t.Fatal("pref:ask must clear the stored preference")
	}
	if len(edits) != 2 || !strings.Contains(edits[0], "MP3 (320 kbps)") || !strings.Contains(edits[1], "ask each time") {
		t.Fatalf("unexpected confirmations: %q", edits)
	}
	// A fresh app instance must pick the value up from storage rather than the cache.
	if err := state.setUserPreference(context.Background(), 11, "flac", "best"); err != nil {
		t.Fatal(err)
	}
	if format, quality, ok := app.getPreference(11); !ok || format != "flac" || quality != "best" {
		t.Fatalf("preference from storage=%q/%q ok=%v", format, quality, ok)
	}
}

func TestSettingsCommandShowsCurrentDefault(t *testing.T) {
	var sentText, markup string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
		case "sendMessage":
			_ = r.ParseForm()
			sentText, markup = r.FormValue("text"), r.FormValue("reply_markup")
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"}}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(context.Background(), bot, nil)
	app.setLang(10, "ru")
	if err := app.setPreference(10, "flac", "best"); err != nil {
		t.Fatal(err)
	}
	app.handleMessage(&tgbotapi.Message{
		Text:     "/settings",
		Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 9}},
		From:     &tgbotapi.User{ID: 10},
		Chat:     &tgbotapi.Chat{ID: 10, Type: "private"},
	})
	if !strings.Contains(sentText, "FLAC") || !strings.Contains(sentText, "настройки") {
		t.Fatalf("unexpected settings text: %q", sentText)
	}
	for _, data := range []string{"pref:mp3:best", "pref:mp3:128", "pref:mp3:320", "pref:flac:best", "pref:m4a:best", "pref:ogg:best", "pref:ask"} {
		if !strings.Contains(markup, `"`+data+`"`) {
			t.Errorf("settings keyboard lacks %q: %s", data, markup)
		}
	}
	if strings.Contains(markup, `"dl:`) {
		t.Fatalf("settings keyboard must not start downloads: %s", markup)
	}
}

func TestIncomingURLWithDefaultPreferenceSkipsFormatKeyboard(t *testing.T) {
	var sendAudioCalls, keyboardCalls atomic.Int32
	var mu sync.Mutex
	var statusTexts []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseForm()
		if strings.Contains(r.FormValue("reply_markup"), `"dl:`) {
			keyboardCalls.Add(1)
		}
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
		case "sendMessage", "editMessageText":
			mu.Lock()
			statusTexts = append(statusTexts, r.FormValue("text"))
			mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"},"text":"status"}}`)
		case "sendAudio":
			sendAudioCalls.Add(1)
			if got := r.FormValue("audio"); got != "cached-file" {
				t.Errorf("audio=%q", got)
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"audio":{"file_id":"cached-file","file_unique_id":"u","duration":1}}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	dl := &downloader{downloadDir: t.TempDir(), maxFileSize: maxFileSize, maxPlaylistTracks: 75, octave: testOctaveClient(http.NotFoundHandler())}
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute, CacheTTL: time.Hour, MaxPlaylistTracks: 75, MaxFileSize: maxFileSize}
	app := newAppWithServices(context.Background(), bot, dl, state, cfg)
	app.setLang(10, "en")
	if err := state.putCachedAudio(context.Background(), cachedAudio{Key: sourceCacheKey("octave", "11", "mp3", "320"), FileID: "cached-file", Title: "Track", Format: "mp3"}); err != nil {
		t.Fatal(err)
	}
	message := &tgbotapi.Message{Text: "https://music.octavestreaming.com/track/11", From: &tgbotapi.User{ID: 10}, Chat: &tgbotapi.Chat{ID: 10, Type: "private"}}

	app.handleMessage(message)
	if sendAudioCalls.Load() != 0 || keyboardCalls.Load() != 1 {
		t.Fatalf("without a default the format keyboard must be shown: audio=%d keyboards=%d", sendAudioCalls.Load(), keyboardCalls.Load())
	}

	if err := app.setPreference(10, "mp3", "320"); err != nil {
		t.Fatal(err)
	}
	app.handleMessage(message)
	if sendAudioCalls.Load() != 1 || keyboardCalls.Load() != 1 {
		t.Fatalf("with a default the download must start immediately: audio=%d keyboards=%d", sendAudioCalls.Load(), keyboardCalls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	hinted := false
	for _, text := range statusTexts {
		if strings.Contains(text, "format: 🎵 MP3 (320 kbps)") && strings.Contains(text, "/settings") {
			hinted = true
		}
	}
	if !hinted {
		t.Fatalf("status must mention the applied default: %q", statusTexts)
	}
	var historyFormat string
	if err := state.db.QueryRow(`SELECT format FROM download_history ORDER BY id DESC LIMIT 1`).Scan(&historyFormat); err != nil || historyFormat != "mp3:320" {
		t.Fatalf("history format=%q err=%v", historyFormat, err)
	}
}

func TestHistoryCommandAndCachedRedelivery(t *testing.T) {
	type sent struct {
		method string
		text   string
		markup string
		audio  string
		chatID string
	}
	var mu sync.Mutex
	var calls []sent
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseForm()
		method := filepath.Base(r.URL.Path)
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
			return
		case "sendMessage", "editMessageText", "sendAudio", "sendDocument":
			mu.Lock()
			calls = append(calls, sent{method: method, text: r.FormValue("text"), markup: r.FormValue("reply_markup"), audio: r.FormValue("audio"), chatID: r.FormValue("chat_id")})
			mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"text":"x"}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute, CacheTTL: time.Hour}
	app := newAppWithServices(ctx, bot, &downloader{downloadDir: t.TempDir()}, state, cfg)
	app.setLang(10, "en")
	app.setLang(11, "en")
	snapshot := func() []sent {
		mu.Lock()
		defer mu.Unlock()
		out := append([]sent(nil), calls...)
		calls = nil
		return out
	}
	command := func(userID int64) *tgbotapi.Message {
		return &tgbotapi.Message{Text: "/history", Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 8}}, From: &tgbotapi.User{ID: userID}, Chat: &tgbotapi.Chat{ID: userID, Type: "private"}}
	}
	callback := func(userID int64, data string) *tgbotapi.CallbackQuery {
		return &tgbotapi.CallbackQuery{ID: "cb", From: &tgbotapi.User{ID: userID}, Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: userID, Type: "private"}}, Data: data}
	}

	app.handleMessage(command(10))
	got := snapshot()
	if len(got) != 1 || got[0].method != "sendMessage" || got[0].text != tr("history_empty", "en") || got[0].markup != "" {
		t.Fatalf("empty history: %#v", got)
	}

	state.recordDownload(ctx, 10, "youtube.com", "mp3:320", "delivered", time.Second, "", "youtube:gone:mp3:320", "Gone", "Nobody")
	state.recordDownload(ctx, 10, "youtube.com", "mp3:320", "delivered", time.Second, "", "octave:11:mp3:320", "Track", "Artist")
	if err := state.putCachedAudio(ctx, cachedAudio{Key: "octave:11:mp3:320", FileID: "cached-file", Title: "Track", Artist: "Artist", Format: "mp3", Quality: "320"}); err != nil {
		t.Fatal(err)
	}
	items, err := state.recentDownloads(ctx, 10, historyLimit)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	cachedID, expiredID := items[0].ID, items[1].ID

	app.handleMessage(command(10))
	got = snapshot()
	if len(got) != 1 || got[0].method != "sendMessage" || !strings.Contains(got[0].text, "Artist — Track") || !strings.Contains(got[0].text, "Nobody — Gone") {
		t.Fatalf("history listing: %#v", got)
	}
	for _, want := range []string{fmt.Sprintf(`"hist:%d"`, cachedID), fmt.Sprintf(`"hist:%d"`, expiredID), `"hist:clear"`} {
		if !strings.Contains(got[0].markup, want) {
			t.Fatalf("history keyboard must contain %s: %s", want, got[0].markup)
		}
	}

	app.handleCallback(callback(11, fmt.Sprintf("hist:%d", cachedID)))
	if got = snapshot(); len(got) != 0 {
		t.Fatalf("another user's history button must be ignored: %#v", got)
	}

	app.handleCallback(callback(10, fmt.Sprintf("hist:%d", cachedID)))
	got = snapshot()
	if len(got) != 1 || got[0].method != "sendAudio" || got[0].audio != "cached-file" || got[0].chatID != "10" {
		t.Fatalf("cached history entry must be re-sent by file_id: %#v", got)
	}
	stats, err := state.stats(ctx)
	if err != nil || stats.CacheHits != 1 {
		t.Fatalf("cache hits=%d err=%v", stats.CacheHits, err)
	}

	app.handleCallback(callback(10, fmt.Sprintf("hist:%d", expiredID)))
	got = snapshot()
	if len(got) != 1 || got[0].method != "sendMessage" || got[0].text != tr("history_expired", "en") {
		t.Fatalf("expired history entry: %#v", got)
	}

	app.handleCallback(callback(10, "hist:clear"))
	got = snapshot()
	if len(got) != 1 || got[0].method != "editMessageText" || got[0].text != tr("history_cleared", "en") {
		t.Fatalf("clear history: %#v", got)
	}
	if items, err := state.recentDownloads(ctx, 10, historyLimit); err != nil || len(items) != 0 {
		t.Fatalf("history must be empty after clearing: %#v err=%v", items, err)
	}
}

func TestAudioSearchQuery(t *testing.T) {
	tests := []struct {
		name  string
		audio *tgbotapi.Audio
		want  string
	}{
		{name: "nil", audio: nil, want: ""},
		{name: "performer and title", audio: &tgbotapi.Audio{Performer: "Daft Punk", Title: "Get Lucky"}, want: "Daft Punk Get Lucky"},
		{name: "title only", audio: &tgbotapi.Audio{Title: " Get Lucky "}, want: "Get Lucky"},
		{name: "performer only", audio: &tgbotapi.Audio{Performer: "Daft Punk"}, want: "Daft Punk"},
		{name: "file name without extension", audio: &tgbotapi.Audio{FileName: "Daft Punk - Get Lucky.mp3"}, want: "Daft Punk - Get Lucky"},
		{name: "tags win over file name", audio: &tgbotapi.Audio{Title: "Get Lucky", FileName: "other.flac"}, want: "Get Lucky"},
		{name: "extension only", audio: &tgbotapi.Audio{FileName: ".mp3"}, want: ""},
		{name: "nothing", audio: &tgbotapi.Audio{Duration: 200}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := audioSearchQuery(tt.audio); got != tt.want {
				t.Fatalf("audioSearchQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestForwardedAudioTriggersSearch(t *testing.T) {
	tests := []struct {
		name      string
		message   *tgbotapi.Message
		wantFirst string
		wantCalls bool
	}{
		{
			name: "private chat with tags searches",
			message: &tgbotapi.Message{
				Audio: &tgbotapi.Audio{Performer: "Daft Punk", Title: "Get Lucky", Duration: 248},
				From:  &tgbotapi.User{ID: 10}, Chat: &tgbotapi.Chat{ID: 10, Type: "private"},
			},
			wantFirst: tr("searching_by_audio", "en"),
			wantCalls: true,
		},
		{
			name: "private chat without metadata explains",
			message: &tgbotapi.Message{
				Audio: &tgbotapi.Audio{Duration: 248},
				From:  &tgbotapi.User{ID: 10}, Chat: &tgbotapi.Chat{ID: 10, Type: "private"},
			},
			wantFirst: tr("audio_no_metadata", "en"),
			wantCalls: true,
		},
		{
			name: "group chat is ignored",
			message: &tgbotapi.Message{
				Audio: &tgbotapi.Audio{Performer: "Daft Punk", Title: "Get Lucky", Duration: 248},
				From:  &tgbotapi.User{ID: 10}, Chat: &tgbotapi.Chat{ID: -20, Type: "group"},
			},
			wantCalls: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var texts []string
			var calls atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = r.ParseForm()
				switch filepath.Base(r.URL.Path) {
				case "getMe":
					fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
				case "sendMessage", "editMessageText":
					calls.Add(1)
					mu.Lock()
					texts = append(texts, r.FormValue("text"))
					mu.Unlock()
					fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"},"text":"status"}}`)
				default:
					calls.Add(1)
					fmt.Fprint(w, `{"ok":true,"result":true}`)
				}
			})
			bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
			if err != nil {
				t.Fatal(err)
			}
			state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			// os.Args[0] is the test binary: yt-dlp lookups exit without producing JSON, so the search ends without network access.
			dl := &downloader{downloadDir: t.TempDir(), bin: os.Args[0], maxFileSize: maxFileSize, maxPlaylistTracks: 75, octave: testOctaveClient(http.NotFoundHandler())}
			cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute, CacheTTL: time.Hour, MaxPlaylistTracks: 75, MaxFileSize: maxFileSize}
			app := newAppWithServices(context.Background(), bot, dl, state, cfg)
			app.setLang(10, "en")

			app.handleMessage(tt.message)

			mu.Lock()
			defer mu.Unlock()
			if !tt.wantCalls {
				if calls.Load() != 0 {
					t.Fatalf("expected no Telegram calls, got %d: %q", calls.Load(), texts)
				}
				return
			}
			if len(texts) == 0 || texts[0] != tt.wantFirst {
				t.Fatalf("first message = %q, want %q", texts, tt.wantFirst)
			}
			if tt.wantFirst == tr("audio_no_metadata", "en") && len(texts) != 1 {
				t.Fatalf("no search must run without metadata: %q", texts)
			}
		})
	}
}
