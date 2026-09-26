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
	handled, succeeded, _ := app.tryCachedDownload(context.Background(), 10, pending, "mp3", "320", "en", nil)
	if !handled || !succeeded || sendAudioCalls.Load() != 1 {
		t.Fatalf("handled=%v succeeded=%v calls=%d", handled, succeeded, sendAudioCalls.Load())
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
	handled, succeeded, _ := app.tryCachedDownload(context.Background(), 10, pending, "flac", "best", "en", nil)
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

func TestPresentSearchResultsShowsYouTubeCandidates(t *testing.T) {
	youtubeBin := filepath.Join(t.TempDir(), "fake-yt-dlp")
	script := `#!/bin/sh
printf '%s' '{"entries":[{"id":"youtube-id","title":"Track","uploader":"Artist","duration":185,"url":"youtube-id"}]}'
`
	if err := os.WriteFile(youtubeBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		resolved   bool
		wantPrefix string
	}{
		{name: "search", wantPrefix: tr("search_results", "en")},
		{name: "resolved link keeps its prefix", resolved: true, wantPrefix: tr("resolved_results", "en")},
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
			if got[0] != tc.wantPrefix {
				t.Fatalf("header=%q, want %q", got[0], tc.wantPrefix)
			}
			stats, err := state.stats(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if stats.Searches != 1 {
				t.Fatalf("stats=%+v, want 1 search", stats)
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
	// onSendAudio runs inside the fake Telegram handler for every sendAudio call.
	onSendAudio func()
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

// newBatchHarness wires a fake Telegram and a fake yt-dlp where videos 11 and 12 download
// successfully, 13 fails while downloading, 77 is too long for FLAC under the 50 MB limit, 99 fails
// to probe, and playlist PL3 holds 11 and 12.
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
				if h.onSendAudio != nil {
					h.onSendAudio()
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"audio":{"file_id":"cached-file","file_unique_id":"u","duration":1}}}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"},"text":"status"}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	ytdlp := filepath.Join(t.TempDir(), "fake-yt-dlp")
	script := `#!/bin/sh
url=''
for a in "$@"; do url="$a"; done
title=''; duration=0; id=''
case "$url" in
  *youtu.be/11) id=11; title=First; duration=180 ;;
  *youtu.be/12) id=12; title=Second; duration=200 ;;
  *youtu.be/13) id=13; title=Third; duration=210 ;;
  *youtu.be/77) id=77; title=Long; duration=1200 ;;
  *youtu.be/99) printf 'ERROR: [youtube] %s: Video unavailable\n' "$url" >&2; exit 1 ;;
  *list=PL3) id=PL3 ;;
  *youtu.be/*) id=${url##*/}; title="Track $id"; duration=120 ;;
  *) printf 'ERROR: [youtube] %s: Video unavailable\n' "$url" >&2; exit 1 ;;
esac
case " $* " in
  *" --simulate "*)
    if [ "$id" = PL3 ]; then
      printf '%s' '{"_type":"playlist","id":"PL3","title":"Album","entries":[{"id":"11","title":"First","uploader":"Artist","duration":180,"extractor":"youtube"},{"id":"12","title":"Second","uploader":"Artist","duration":200,"extractor":"youtube"}]}'
    else
      printf '{"id":"%s","title":"%s","uploader":"Artist","duration":%s,"extractor":"youtube"}' "$id" "$title" "$duration"
    fi
    exit 0 ;;
esac
if [ "$id" = 13 ]; then
  printf 'ERROR: unable to download video data: HTTP 404 Not Found\n' >&2
  exit 1
fi
dir=''; manifest=''; progress=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --paths) dir="$2"; shift 2 ;;
    --print-to-file)
      template="$2"; output="$3"
      case "$template" in after_move:*) manifest="$output" ;; before_dl:*) progress="$output" ;; esac
      shift 3 ;;
    *) shift ;;
  esac
done
path="$dir/000001_$id.mp3"
printf 'audio' > "$path"
printf '1\n' >> "$progress"
printf '{"id":"%s","title":"%s","uploader":"Artist","duration":%s,"filepath":"%s","ext":"mp3","extractor":"youtube"}\n' "$id" "$title" "$duration" "$path" >> "$manifest"
`
	if err := os.WriteFile(ytdlp, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegramHandler})
	if err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	dl := &downloader{downloadDir: t.TempDir(), bin: ytdlp, maxFileSize: maxFileSize, maxPlaylistTracks: 75}
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

func cacheTrack(t *testing.T, state *store, trackID, title string) {
	t.Helper()
	if err := state.putCachedAudio(context.Background(), cachedAudio{Key: sourceCacheKey("youtube", trackID, "mp3", "320"), FileID: "cached-file", Title: title, Format: "mp3", Quality: "320"}); err != nil {
		t.Fatal(err)
	}
}

func TestBatchOfTwoLinksShowsPreviewAndDeliversBoth(t *testing.T) {
	h := newBatchHarness(t)
	cacheTrack(t, h.state, "11", "First")
	cacheTrack(t, h.state, "12", "Second")

	h.app.handleMessage(batchMessage("https://youtu.be/11 and https://youtu.be/12"))
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
	cacheTrack(t, h.state, "11", "First")

	h.app.handleMessage(batchMessage("https://youtu.be/11 https://youtu.be/99"))
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
	if !strings.Contains(preview.text, "links in the message: 1") || !strings.Contains(preview.text, "youtu.be/99</code> — skipped: ") {
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
	cacheTrack(t, h.state, "11", "First")
	if err := h.app.setPreference(10, "mp3", "320"); err != nil {
		t.Fatal(err)
	}

	h.app.handleMessage(batchMessage("https://youtu.be/11 https://youtu.be/99"))
	calls := h.snapshot()
	for _, call := range calls {
		if strings.Contains(call.markup, `"dl:mp3:320:`) {
			t.Fatalf("the format keyboard must be skipped with a default format: %#v", call)
		}
	}
	skipped := false
	for _, call := range calls {
		if (call.method == "sendMessage" || call.method == "editMessageText") && strings.Contains(call.text, "youtu.be/99</code> — skipped: ") {
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
	cacheTrack(t, h.state, "11", "First")

	h.app.handleMessage(batchMessage("https://youtu.be/11 https://youtu.be/12"))
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
	cacheTrack(t, h.state, "11", "First")
	cacheTrack(t, h.state, "12", "Second")

	h.app.handleMessage(batchMessage("https://youtu.be/11\nhttps://youtu.be/12\nhttps://youtu.be/13"))
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
	h.app.handleMessage(batchMessage("https://youtu.be/11 https://www.youtube.com/playlist?list=PL3"))
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
		links = append(links, "https://youtu.be/"+strconv.Itoa(i))
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

func TestBatchReleasesDownloadSlotBeforeDelivery(t *testing.T) {
	h := newBatchHarness(t)
	cacheTrack(t, h.state, "11", "First")
	var activeDuringSend []int
	h.onSendAudio = func() {
		active, _, _ := h.app.downloads.snapshot()
		activeDuringSend = append(activeDuringSend, active)
	}

	// One cached link and one that must really be downloaded through the fake yt-dlp.
	h.app.handleMessage(batchMessage("https://youtu.be/11 and https://youtu.be/12"))
	key := h.pendingKey(t)
	h.app.handleCallback(batchCallback("dl:mp3:320:" + key))
	h.snapshot()
	h.app.handleCallback(batchCallback("delivery:individual:mp3:320:" + key))
	calls := h.snapshot()
	if got := h.count("sendAudio", calls); got != 2 {
		t.Fatalf("sendAudio calls=%d, calls=%#v", got, calls)
	}
	if len(activeDuringSend) != 2 {
		t.Fatalf("expected two sendAudio observations, got %v", activeDuringSend)
	}
	for _, active := range activeDuringSend {
		if active != 0 {
			t.Fatalf("download slot must be released before Telegram delivery, active=%v", activeDuringSend)
		}
	}
	if active, waiting, _ := h.app.downloads.snapshot(); active != 0 || waiting != 0 {
		t.Fatalf("download gate leaked: active=%d waiting=%d", active, waiting)
	}
}
