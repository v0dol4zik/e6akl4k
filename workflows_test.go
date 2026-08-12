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

func TestCachedCallbackFlow(t *testing.T) {
	var sendAudioCalls atomic.Int32
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
	var historyStatus string
	if err := state.db.QueryRow(`SELECT status FROM download_history ORDER BY id DESC LIMIT 1`).Scan(&historyStatus); err != nil || historyStatus != "ok" {
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
