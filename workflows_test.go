package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
