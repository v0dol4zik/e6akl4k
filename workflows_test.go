package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestCachedDownloadUsesTelegramFileID(t *testing.T) {
	var sendAudioCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := filepath.Base(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
		case "sendAudio":
			sendAudioCalls.Add(1)
			_ = r.ParseMultipartForm(1 << 20)
			_ = r.ParseForm()
			if !strings.Contains(r.FormValue("audio"), "cached-file") {
				t.Errorf("audio=%q", r.FormValue("audio"))
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
	pending := pendingURL{URL: "https://youtu.be/id", Preview: mediaPreview{SourceID: "id", Extractor: "youtube"}}
	key := sourceCacheKey("youtube", "id", "mp3", "320")
	if err := state.putCachedAudio(context.Background(), cachedAudio{Key: key, FileID: "cached-file", Title: "Track", Format: "mp3"}); err != nil {
		t.Fatal(err)
	}
	dl := &downloader{downloadDir: t.TempDir(), bin: os.Args[0]}
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute, CacheTTL: time.Hour, MaxPlaylistTracks: 75}
	app := newAppWithServices(context.Background(), bot, dl, state, cfg)
	handled, succeeded := app.tryCachedDownload(context.Background(), 10, pending, "mp3", "320", "en", nil)
	if !handled || !succeeded || sendAudioCalls.Load() != 1 {
		t.Fatalf("handled=%v succeeded=%v calls=%d", handled, succeeded, sendAudioCalls.Load())
	}
}

func TestOctaveRemoteMP3IsCachedWithoutLocalMediaDownload(t *testing.T) {
	var sendAudioCalls atomic.Int32
	var mediaDownloadCalls atomic.Int32
	octaveHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/album/3":
			fmt.Fprint(w, `{"album":{"id":"3","title":"Album","artist":{"id":"2","name":"Artist"},"tracks":[{"id":"11","title":"Track","artist":{"id":"2","name":"Artist"},"duration":180}]}}`)
		case "/api/playback-token":
			fmt.Fprint(w, `{"token":"octk_test_token","expiresIn":7200}`)
		case "/audio/320":
			mediaDownloadCalls.Add(1)
			http.Error(w, "must not download through the bot", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	})
	telegramHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
		case "sendAudio":
			call := sendAudioCalls.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			audio := r.FormValue("audio")
			if call == 1 {
				if !strings.Contains(audio, "/audio/320?track=11") || !strings.Contains(audio, "k=octk_test_token") {
					t.Errorf("remote audio=%q", audio)
				}
			} else if audio != "remote-file" {
				t.Errorf("cached audio=%q", audio)
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"audio":{"file_id":"remote-file","file_unique_id":"u","duration":180,"file_size":7549747}}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegramHandler})
	if err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	dl := &downloader{
		downloadDir: t.TempDir(), maxFileSize: maxFileSize, maxPlaylistTracks: 75,
		octave: testOctaveClient(octaveHandler),
	}
	cfg := config{
		CacheChatID: -1001, DownloadWorkers: 1, DownloadQueueSize: 1,
		LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute,
		CacheTTL: time.Hour, MaxFileSize: maxFileSize, MaxPlaylistTracks: 75,
	}
	app := newAppWithServices(context.Background(), bot, dl, state, cfg)
	pending := pendingURL{
		URL:     "https://music.octavestreaming.com/album/3?t=11",
		Preview: mediaPreview{SourceID: "11", Extractor: "octave", Title: "Track", Artist: "Artist", DurationSeconds: 180},
	}
	handled, succeeded := app.tryCachedDownload(context.Background(), 10, pending, "mp3", "320", "en", nil)
	if !handled || !succeeded || sendAudioCalls.Load() != 2 || mediaDownloadCalls.Load() != 0 {
		t.Fatalf("handled=%v succeeded=%v sends=%d media_downloads=%d", handled, succeeded, sendAudioCalls.Load(), mediaDownloadCalls.Load())
	}
	if entry, ok := state.cachedAudio(context.Background(), "octave:11:mp3:320", time.Hour); !ok || entry.FileID != "remote-file" || entry.Size != 7549747 {
		t.Fatalf("cached entry=%#v ok=%v", entry, ok)
	}
}

func TestOctaveRemoteMP3FallsBackToLocalUpload(t *testing.T) {
	var sendAudioCalls atomic.Int32
	var mediaDownloadCalls atomic.Int32
	octaveHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/album/3":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"album":{"id":"3","title":"Album","artist":{"id":"2","name":"Artist"},"tracks":[{"id":"11","title":"Track","artist":{"id":"2","name":"Artist"},"duration":180}]}}`)
		case "/api/playback-token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"octk_test_token","expiresIn":7200}`)
		case "/audio/320":
			mediaDownloadCalls.Add(1)
			w.Header().Set("Content-Type", "audio/mpeg")
			fmt.Fprint(w, "audio-data")
		default:
			http.NotFound(w, r)
		}
	})
	telegramHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
		case "sendAudio":
			call := sendAudioCalls.Add(1)
			if call == 1 {
				fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"failed to get HTTP URL content"}`)
				return
			}
			fileID := "local-file"
			if call == 3 {
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse cached form: %v", err)
				}
				if got := r.FormValue("audio"); got != fileID {
					t.Errorf("cached audio=%q", got)
				}
			}
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"audio":{"file_id":%q,"file_unique_id":"u","duration":180,"file_size":10}}}`, fileID)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegramHandler})
	if err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	dl := &downloader{
		downloadDir: t.TempDir(), maxFileSize: maxFileSize, maxPlaylistTracks: 75,
		octave: testOctaveClient(octaveHandler),
	}
	cfg := config{
		CacheChatID: -1001, DownloadWorkers: 1, DownloadQueueSize: 1,
		LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute,
		CacheTTL: time.Hour, MaxFileSize: maxFileSize, MaxPlaylistTracks: 75,
	}
	app := newAppWithServices(context.Background(), bot, dl, state, cfg)
	pending := pendingURL{
		URL:     "https://music.octavestreaming.com/album/3?t=11",
		Preview: mediaPreview{SourceID: "11", Extractor: "octave", Title: "Track", Artist: "Artist", DurationSeconds: 180},
	}
	handled, succeeded := app.tryCachedDownload(context.Background(), 10, pending, "mp3", "320", "en", nil)
	if !handled || !succeeded || sendAudioCalls.Load() != 3 || mediaDownloadCalls.Load() != 1 {
		t.Fatalf("handled=%v succeeded=%v sends=%d media_downloads=%d", handled, succeeded, sendAudioCalls.Load(), mediaDownloadCalls.Load())
	}
}

func TestCachedFLACUsesTelegramDocument(t *testing.T) {
	var documentCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
		case "sendDocument":
			documentCalls.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"document":{"file_id":"flac-file","file_unique_id":"u"}}}`)
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
	pending := pendingURL{URL: "https://youtu.be/id", Preview: mediaPreview{SourceID: "id", Extractor: "youtube"}}
	key := sourceCacheKey("youtube", "id", "flac", "best")
	if err := state.putCachedAudio(context.Background(), cachedAudio{Key: key, FileID: "flac-file", Title: "Track", Format: "flac", MediaType: "document"}); err != nil {
		t.Fatal(err)
	}
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute, CacheTTL: time.Hour}
	app := newAppWithServices(context.Background(), bot, &downloader{downloadDir: t.TempDir()}, state, cfg)
	handled, succeeded := app.tryCachedDownload(context.Background(), 10, pending, "flac", "best", "en", nil)
	if !handled || !succeeded || documentCalls.Load() != 1 {
		t.Fatalf("handled=%v succeeded=%v document calls=%d", handled, succeeded, documentCalls.Load())
	}
}

func TestCachedCallbackFlow(t *testing.T) {
	var sendAudioCalls atomic.Int32
	var deleteMessageCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
		case "sendAudio":
			sendAudioCalls.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"audio":{"file_id":"cached-file","file_unique_id":"u","duration":1}}}`)
		case "editMessageText":
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"},"text":"status"}}`)
		case "deleteMessage":
			deleteMessageCalls.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":true}`)
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
	dlDir := t.TempDir()
	dl := &downloader{downloadDir: dlDir, bin: os.Args[0], maxFileSize: maxFileSize, maxPlaylistTracks: 75}
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute, CacheTTL: time.Hour, MaxPlaylistTracks: 75, MaxFileSize: maxFileSize}
	app := newAppWithServices(context.Background(), bot, dl, state, cfg)
	pending := pendingURL{URL: "https://youtu.be/id", ChatID: 10, UserID: 10, Preview: mediaPreview{SourceID: "id", Extractor: "youtube"}}
	key, err := app.storeURL(pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.putCachedAudio(context.Background(), cachedAudio{Key: sourceCacheKey("youtube", "id", "mp3", "320"), FileID: "cached-file", Title: "Track", Format: "mp3"}); err != nil {
		t.Fatal(err)
	}
	app.handleCallback(&tgbotapi.CallbackQuery{ID: "callback", From: &tgbotapi.User{ID: 10}, Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 10, Type: "private"}}, Data: "dl:mp3:320:" + key})
	if sendAudioCalls.Load() != 1 {
		t.Fatalf("sendAudio calls=%d", sendAudioCalls.Load())
	}
	if deleteMessageCalls.Load() != 1 {
		t.Fatalf("deleteMessage calls=%d", deleteMessageCalls.Load())
	}
	var historyStatus string
	if err := state.db.QueryRow(`SELECT status FROM download_history ORDER BY id DESC LIMIT 1`).Scan(&historyStatus); err != nil || historyStatus != "delivered" {
		t.Fatalf("history=%q err=%v", historyStatus, err)
	}
}

func TestOpenGraphResolverIsBounded(t *testing.T) {
	oldClient := makeResolverClient
	makeResolverClient = func() httpDoer {
		return handlerClient{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `<html><head><meta property="og:title" content="Track &amp; Remix"><meta property="music:musician" content="Artist"></head></html>`)
		})}
	}
	defer func() { makeResolverClient = oldClient }()
	title, artist, err := fetchOpenGraph(context.Background(), "https://music.apple.com/test")
	if err != nil {
		t.Fatal(err)
	}
	if title != "Track & Remix" || artist != "Artist" {
		t.Fatalf("title=%q artist=%q", title, artist)
	}
}

type handlerClient struct{ handler http.Handler }

func (c handlerClient) Do(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func TestCacheKeysNormalizeFragments(t *testing.T) {
	first := generalCacheKey("https://YouTube.com/watch?v=x#one", "MP3", "320")
	second := generalCacheKey("https://youtube.com/watch?v=x#two", "mp3", "320")
	if first != second {
		t.Fatalf("keys differ: %q %q", first, second)
	}
	parsed, _ := url.Parse("https://youtube.com/watch?v=x")
	if sourceHost(parsed.String()) != "youtube.com" {
		t.Fatal("unexpected source host")
	}
}

func TestCookieFailureClassification(t *testing.T) {
	if !isCookieFailure("YouTube требует подтверждения, что запрос не от бота. Нужен свежий cookies.txt") {
		t.Fatal("cookie failure not detected")
	}
	if isCookieFailure("Видео приватное") {
		t.Fatal("unrelated error classified as cookie failure")
	}
}

func TestPresentSearchResultsAnnouncesYouTubeFallback(t *testing.T) {
	youtubeBin := filepath.Join(t.TempDir(), "fake-yt-dlp")
	script := `#!/bin/sh
printf '%s' '{"entries":[{"id":"youtube-id","title":"Track","uploader":"Artist","duration":185,"url":"youtube-id"}]}'
`
	if err := os.WriteFile(youtubeBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	octaveJSON := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		})
	}
	tests := []struct {
		name         string
		octave       http.Handler
		resolved     bool
		wantPrefix   string
		wantNotice   string
		wantOctave   int64
		wantFallback int64
	}{
		{
			name:       "octave results",
			octave:     octaveJSON(`{"results":[{"id":"11","title":"Track","artist":{"id":"2","name":"Artist"},"album":{"id":"3"},"duration":185}]}`),
			wantPrefix: tr("search_results", "en"), wantOctave: 1,
		},
		{
			name:       "octave empty",
			octave:     octaveJSON(`{"results":[]}`),
			wantPrefix: tr("search_results", "en"), wantNotice: tr("search_fallback_no_results", "en"), wantFallback: 1,
		},
		{
			name: "octave unavailable",
			octave: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusBadGateway)
			}),
			wantPrefix: tr("search_results", "en"), wantNotice: tr("search_fallback_unavailable", "en"), wantFallback: 1,
		},
		{
			name:       "resolved link keeps its prefix",
			octave:     octaveJSON(`{"results":[]}`),
			resolved:   true,
			wantPrefix: tr("resolved_results", "en"), wantNotice: tr("search_fallback_no_results", "en"), wantFallback: 1,
		},
		{
			name:       "octave disabled",
			wantPrefix: tr("search_results", "en"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var texts []string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = r.ParseForm()
				switch filepath.Base(r.URL.Path) {
				case "getMe":
					fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
				case "sendMessage", "editMessageText":
					mu.Lock()
					texts = append(texts, r.FormValue("text"))
					mu.Unlock()
					fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"text":"x"}}`)
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
			dl := &downloader{downloadDir: t.TempDir(), bin: youtubeBin}
			if tc.octave != nil {
				dl.octave = testOctaveClient(tc.octave)
			}
			cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute, CacheTTL: time.Hour}
			app := newAppWithServices(context.Background(), bot, dl, state, cfg)
			status := &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 10, Type: "private"}}
			app.presentSearchResults(10, 10, "Artist Track", "en", status, tc.resolved, 185)

			mu.Lock()
			got := append([]string(nil), texts...)
			mu.Unlock()
			if len(got) != 1 {
				t.Fatalf("messages=%#v", got)
			}
			want := tc.wantPrefix
			if tc.wantNotice != "" {
				want += "\n\n" + tc.wantNotice
			}
			if got[0] != want {
				t.Fatalf("header=%q, want %q", got[0], want)
			}
			stats, err := state.stats(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if stats.Searches != 1 || stats.SearchOctave != tc.wantOctave || stats.SearchYouTubeFallback != tc.wantFallback {
				t.Fatalf("stats=%+v, want octave=%d fallback=%d", stats, tc.wantOctave, tc.wantFallback)
			}
		})
	}
}

type telegramCall struct {
	method string
	text   string
	markup string
	audio  string
}

type batchHarness struct {
	app   *app
	state *store
	mu    sync.Mutex
	calls []telegramCall
}

func (h *batchHarness) snapshot() []telegramCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]telegramCall(nil), h.calls...)
	h.calls = nil
	return out
}

func (h *batchHarness) count(method string, calls []telegramCall) int {
	n := 0
	for _, call := range calls {
		if call.method == method {
			n++
		}
	}
	return n
}

func (h *batchHarness) hasText(calls []telegramCall, text string) bool {
	for _, call := range calls {
		if call.method == "sendMessage" && call.text == text {
			return true
		}
	}
	return false
}

func (h *batchHarness) pendingKey(t *testing.T) string {
	t.Helper()
	h.app.mu.Lock()
	defer h.app.mu.Unlock()
	if len(h.app.urls) != 1 {
		t.Fatalf("expected exactly one pending entry, got %d", len(h.app.urls))
	}
	for key := range h.app.urls {
		return key
	}
	return ""
}

// newBatchHarness wires a fake Telegram and a fake Octave API where album 3 holds tracks 11 and 12
// (downloadable as MP3 320 without conversion) and every other album or track is a 404.
func newBatchHarness(t *testing.T) *batchHarness {
	t.Helper()
	h := &batchHarness{}
	telegramHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseForm()
		method := filepath.Base(r.URL.Path)
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
			return
		case "sendMessage", "editMessageText", "sendAudio", "sendDocument":
			h.mu.Lock()
			h.calls = append(h.calls, telegramCall{method: method, text: r.FormValue("text"), markup: r.FormValue("reply_markup"), audio: r.FormValue("audio")})
			h.mu.Unlock()
			if method == "sendAudio" {
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"audio":{"file_id":"cached-file","file_unique_id":"u","duration":1}}}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"},"text":"status"}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	octaveHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/album/3":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"album":{"id":"3","title":"Album","artist":{"id":"2","name":"Artist"},"tracks":[{"id":"11","title":"First","artist":{"id":"2","name":"Artist"},"duration":180},{"id":"12","title":"Second","artist":{"id":"2","name":"Artist"},"duration":200}]}}`)
		case "/api/playback-token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"octk_test_token","expiresIn":7200}`)
		case "/audio/320":
			if track := r.URL.Query().Get("track"); track != "11" && track != "12" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			fmt.Fprint(w, "audio")
		default:
			http.NotFound(w, r)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegramHandler})
	if err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	previousSleep := octaveRetrySleep
	octaveRetrySleep = func(context.Context, int) error { return nil }
	t.Cleanup(func() { octaveRetrySleep = previousSleep })
	dl := &downloader{downloadDir: t.TempDir(), maxFileSize: maxFileSize, maxPlaylistTracks: 75, octave: testOctaveClient(octaveHandler)}
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 20, RateWindow: time.Minute, CacheTTL: time.Hour, MaxPlaylistTracks: 75, MaxFileSize: maxFileSize}
	h.app = newAppWithServices(context.Background(), bot, dl, state, cfg)
	h.state = state
	h.app.setLang(10, "en")
	return h
}

func batchMessage(text string) *tgbotapi.Message {
	return &tgbotapi.Message{Text: text, From: &tgbotapi.User{ID: 10}, Chat: &tgbotapi.Chat{ID: 10, Type: "private"}}
}

func batchCallback(data string) *tgbotapi.CallbackQuery {
	return &tgbotapi.CallbackQuery{ID: "cb", From: &tgbotapi.User{ID: 10}, Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 10, Type: "private"}}, Data: data}
}

func cacheOctaveTrack(t *testing.T, state *store, trackID, title string) {
	t.Helper()
	if err := state.putCachedAudio(context.Background(), cachedAudio{Key: sourceCacheKey("octave", trackID, "mp3", "320"), FileID: "cached-file", Title: title, Format: "mp3", Quality: "320"}); err != nil {
		t.Fatal(err)
	}
}

func TestBatchOfTwoLinksShowsPreviewAndDeliversBoth(t *testing.T) {
	h := newBatchHarness(t)
	cacheOctaveTrack(t, h.state, "11", "First")
	cacheOctaveTrack(t, h.state, "12", "Second")

	h.app.handleMessage(batchMessage("https://music.octavestreaming.com/album/3?t=11 and https://music.octavestreaming.com/album/3?t=12"))
	calls := h.snapshot()
	var preview telegramCall
	for _, call := range calls {
		if strings.Contains(call.markup, `"dl:mp3:320:`) {
			preview = call
		}
	}
	if preview.method == "" {
		t.Fatalf("batch preview with a format keyboard was not shown: %#v", calls)
	}
	if !strings.Contains(preview.text, "links in the message: 2") || !strings.Contains(preview.text, "1. Artist — First") || !strings.Contains(preview.text, "2. Artist — Second") {
		t.Fatalf("unexpected preview text: %q", preview.text)
	}
	if strings.Contains(preview.markup, `"delivery:`) {
		t.Fatalf("small batches must not ask for a delivery mode: %s", preview.markup)
	}
	pending, ok := h.app.getURL(h.pendingKey(t), 10, 10)
	if !ok || len(pending.Batch) != 2 || pending.Preview.IsPlaylist || pending.Preview.Extractor != "batch" || pending.Preview.TrackCount != 2 || pending.Preview.Title != "2 tracks" {
		t.Fatalf("unexpected pending batch: %#v", pending)
	}

	key := h.pendingKey(t)
	h.app.handleCallback(batchCallback("dl:mp3:320:" + key))
	calls = h.snapshot()
	choice := h.deliveryPrompt(t, calls)
	if choice.text != tr("choose_delivery_batch", "en", "count", "2") {
		t.Fatalf("unexpected delivery prompt: %q", choice.text)
	}
	if h.count("sendAudio", calls) != 0 {
		t.Fatalf("nothing must be sent before the delivery mode is chosen: %#v", calls)
	}
	h.app.handleCallback(batchCallback("delivery:individual:mp3:320:" + key))
	calls = h.snapshot()
	if got := h.count("sendAudio", calls); got != 2 {
		t.Fatalf("sendAudio calls=%d, calls=%#v", got, calls)
	}
	if !h.hasText(calls, tr("all_sent_summary", "en", "sent", "2", "total", "2")) {
		t.Fatalf("summary is missing: %#v", calls)
	}
	var status, cacheKey string
	if err := h.state.db.QueryRow(`SELECT status, cache_key FROM download_history ORDER BY id DESC LIMIT 1`).Scan(&status, &cacheKey); err != nil || status != "delivered" || cacheKey != "" {
		t.Fatalf("history status=%q cache_key=%q err=%v", status, cacheKey, err)
	}
	if _, ok := h.app.getURL(key, 10, 10); ok {
		t.Fatal("pending batch must be consumed")
	}
}

func TestBatchSkipsLinkWithFailedPreview(t *testing.T) {
	h := newBatchHarness(t)
	cacheOctaveTrack(t, h.state, "11", "First")

	h.app.handleMessage(batchMessage("https://music.octavestreaming.com/album/3?t=11 https://music.octavestreaming.com/album/99?t=5"))
	calls := h.snapshot()
	var preview telegramCall
	for _, call := range calls {
		if strings.Contains(call.markup, `"dl:mp3:320:`) {
			preview = call
		}
	}
	if preview.method == "" {
		t.Fatalf("preview was not shown: %#v", calls)
	}
	if !strings.Contains(preview.text, "links in the message: 1") || !strings.Contains(preview.text, "album/99?t=5</code> — skipped: Octave API") {
		t.Fatalf("failed link must be listed with its error: %q", preview.text)
	}
	h.app.handleCallback(batchCallback("dl:mp3:320:" + h.pendingKey(t)))
	calls = h.snapshot()
	for _, call := range calls {
		if strings.Contains(call.markup, `"delivery:`) {
			t.Fatalf("a batch with one remaining link must not ask for a delivery mode: %#v", call)
		}
	}
	if got := h.count("sendAudio", calls); got != 1 {
		t.Fatalf("sendAudio calls=%d, calls=%#v", got, calls)
	}
}

// deliveryPrompt returns the message carrying the ZIP/individual keyboard.
func (h *batchHarness) deliveryPrompt(t *testing.T, calls []telegramCall) telegramCall {
	t.Helper()
	for _, call := range calls {
		if strings.Contains(call.markup, `"delivery:zip:`) && strings.Contains(call.markup, `"delivery:individual:`) && strings.Contains(call.markup, `"cancel:`) {
			return call
		}
	}
	t.Fatalf("delivery keyboard was not shown: %#v", calls)
	return telegramCall{}
}

// TestBatchWithDefaultFormatReportsSkippedLinks covers the fast path: a stored default format
// skips the preview keyboard, so the skipped links must still be reported before downloading.
func TestBatchWithDefaultFormatReportsSkippedLinks(t *testing.T) {
	h := newBatchHarness(t)
	cacheOctaveTrack(t, h.state, "11", "First")
	if err := h.app.setPreference(10, "mp3", "320"); err != nil {
		t.Fatal(err)
	}

	h.app.handleMessage(batchMessage("https://music.octavestreaming.com/album/3?t=11 https://music.octavestreaming.com/album/99?t=5"))
	calls := h.snapshot()
	for _, call := range calls {
		if strings.Contains(call.markup, `"dl:mp3:320:`) {
			t.Fatalf("the format keyboard must be skipped with a default format: %#v", call)
		}
	}
	skipped := false
	for _, call := range calls {
		if (call.method == "sendMessage" || call.method == "editMessageText") && strings.Contains(call.text, "album/99?t=5</code> — skipped: Octave API") {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("the skipped link must be reported on the default-format path: %#v", calls)
	}
	if got := h.count("sendAudio", calls); got != 1 {
		t.Fatalf("sendAudio calls=%d, calls=%#v", got, calls)
	}
	if !h.hasText(calls, tr("all_sent_summary", "en", "sent", "1", "total", "1")) {
		t.Fatalf("summary is missing: %#v", calls)
	}
}

// TestBatchZIPDeliverySendsOneArchive downloads both links for real and packs them into a ZIP;
// the file_id cache must be bypassed because cached tracks cannot be archived.
func TestBatchZIPDeliverySendsOneArchive(t *testing.T) {
	h := newBatchHarness(t)
	cacheOctaveTrack(t, h.state, "11", "First")

	h.app.handleMessage(batchMessage("https://music.octavestreaming.com/album/3?t=11 https://music.octavestreaming.com/album/3?t=12"))
	h.snapshot()
	key := h.pendingKey(t)
	h.app.handleCallback(batchCallback("dl:mp3:320:" + key))
	h.deliveryPrompt(t, h.snapshot())
	h.app.handleCallback(batchCallback("delivery:zip:mp3:320:" + key))
	calls := h.snapshot()
	if got := h.count("sendDocument", calls); got != 1 {
		t.Fatalf("sendDocument calls=%d, calls=%#v", got, calls)
	}
	if got := h.count("sendAudio", calls); got != 0 {
		t.Fatalf("ZIP delivery must not send tracks one by one: calls=%#v", calls)
	}
	if !h.hasText(calls, tr("zipping", "en", "count", "2")) || !h.hasText(calls, tr("zip_sent", "en")) {
		t.Fatalf("ZIP progress messages are missing: %#v", calls)
	}
	var status string
	if err := h.state.db.QueryRow(`SELECT status FROM download_history ORDER BY id DESC LIMIT 1`).Scan(&status); err != nil || status != "delivered" {
		t.Fatalf("history status=%q err=%v", status, err)
	}
	if _, ok := h.app.getURL(key, 10, 10); ok {
		t.Fatal("pending batch must be consumed")
	}
	entries, err := os.ReadDir(h.app.downloader.downloadDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("session files must be cleaned up after ZIP delivery: entries=%v err=%v", entries, err)
	}
}

func TestNeedsDeliveryChoice(t *testing.T) {
	cases := []struct {
		name    string
		pending pendingURL
		want    bool
	}{
		{"single track", pendingURL{Preview: mediaPreview{}}, false},
		{"small playlist", pendingURL{Preview: mediaPreview{IsPlaylist: true, TrackCount: 5}}, false},
		{"large playlist", pendingURL{Preview: mediaPreview{IsPlaylist: true, TrackCount: 20}}, true},
		{"large playlist decided", pendingURL{Preview: mediaPreview{IsPlaylist: true, TrackCount: 20}, Delivery: "zip"}, false},
		{"batch of two", pendingURL{Batch: []string{"a", "b"}}, true},
		{"batch of one", pendingURL{Batch: []string{"a"}}, false},
		{"batch decided", pendingURL{Batch: []string{"a", "b"}, Delivery: "individual"}, false},
	}
	for _, tc := range cases {
		if got := needsDeliveryChoice(tc.pending); got != tc.want {
			t.Errorf("%s: needsDeliveryChoice=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestBatchWithOneFailedDownloadIsPartial(t *testing.T) {
	h := newBatchHarness(t)
	cacheOctaveTrack(t, h.state, "11", "First")
	cacheOctaveTrack(t, h.state, "12", "Second")

	h.app.handleMessage(batchMessage("https://music.octavestreaming.com/album/3?t=11\nhttps://music.octavestreaming.com/album/3?t=12\nhttps://music.octavestreaming.com/track/13"))
	h.snapshot()
	h.app.handleCallback(batchCallback("dl:mp3:320:" + h.pendingKey(t)))
	h.snapshot()
	h.app.handleCallback(batchCallback("delivery:individual:mp3:320:" + h.pendingKey(t)))
	calls := h.snapshot()
	if got := h.count("sendAudio", calls); got != 2 {
		t.Fatalf("sendAudio calls=%d, calls=%#v", got, calls)
	}
	if !h.hasText(calls, tr("batch_partial", "en", "ok", "2", "failed", "1")) {
		t.Fatalf("partial summary is missing: %#v", calls)
	}
	errorReported := false
	for _, call := range calls {
		if call.method == "sendMessage" && strings.Contains(call.text, "error") && strings.Contains(call.text, "HTTP 404") {
			errorReported = true
		}
	}
	if !errorReported {
		t.Fatalf("the failed link must be reported: %#v", calls)
	}
	stats, err := h.state.stats(context.Background())
	if err != nil || stats.DownloadsPartial != 1 || stats.DownloadsOK != 0 {
		t.Fatalf("stats=%#v err=%v", stats, err)
	}
	var status string
	if err := h.state.db.QueryRow(`SELECT status FROM download_history ORDER BY id DESC LIMIT 1`).Scan(&status); err != nil || status != "partial" {
		t.Fatalf("history status=%q err=%v", status, err)
	}
}

func TestBatchRejectsPlaylists(t *testing.T) {
	h := newBatchHarness(t)
	h.app.handleMessage(batchMessage("https://music.octavestreaming.com/album/3?t=11 https://music.octavestreaming.com/album/3"))
	calls := h.snapshot()
	last := calls[len(calls)-1]
	if last.method != "sendMessage" || last.text != tr("batch_no_playlists", "en") {
		t.Fatalf("unexpected reply: %#v", calls)
	}
	h.app.mu.Lock()
	defer h.app.mu.Unlock()
	if len(h.app.urls) != 0 {
		t.Fatal("nothing must be stored for a rejected batch")
	}
}

func TestBatchLimitKeepsFirstFiveLinks(t *testing.T) {
	h := newBatchHarness(t)
	var links []string
	for i := 20; i < 27; i++ {
		links = append(links, "https://music.octavestreaming.com/track/"+strconv.Itoa(i))
	}
	h.app.handleMessage(batchMessage(strings.Join(links, " ")))
	calls := h.snapshot()
	if calls[0].method != "sendMessage" || calls[0].text != tr("batch_limit", "en", "max", "5") {
		t.Fatalf("batch_limit must be sent first: %#v", calls)
	}
	pending, ok := h.app.getURL(h.pendingKey(t), 10, 10)
	if !ok || len(pending.Batch) != maxBatchLinks || pending.Batch[4] != links[4] {
		t.Fatalf("unexpected pending batch: %#v", pending)
	}
}
