package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type recognizerFunc func(context.Context, string) (recognizedTrack, error)

func (f recognizerFunc) recognize(ctx context.Context, path string) (recognizedTrack, error) {
	return f(ctx, path)
}

type voiceTestTelegram struct {
	mu         sync.Mutex
	app        *app
	texts      []string
	markups    []tgbotapi.InlineKeyboardMarkup
	fileCalls  int
	audioCalls int
	audio      string
	slotHeld   bool
	filePath   string
	fileSize   int
	body       string
	status     int
}

func (tg *voiceTestTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	if strings.Contains(r.URL.Path, "/file/bot") {
		if tg.status != 0 {
			w.WriteHeader(tg.status)
		}
		fmt.Fprint(w, tg.body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = r.ParseForm()
	switch filepath.Base(r.URL.Path) {
	case "getMe":
		fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
	case "getFile":
		tg.fileCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{
			"file_id": "voice", "file_path": tg.filePath, "file_size": tg.fileSize,
		}})
	case "sendMessage", "editMessageText":
		tg.texts = append(tg.texts, r.FormValue("text"))
		var markup tgbotapi.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(r.FormValue("reply_markup")), &markup); err == nil && len(markup.InlineKeyboard) > 0 {
			tg.markups = append(tg.markups, markup)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"},"text":"status"}}`)
	case "sendAudio":
		tg.audioCalls++
		tg.audio = r.FormValue("audio")
		// Assert the user slot stays occupied all the way through automatic delivery.
		tg.app.mu.Lock()
		active := tg.app.activeUser[10]
		tg.app.mu.Unlock()
		tg.slotHeld = active
		if !active {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"ok":false,"error_code":500,"description":"user slot was released too soon"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"audio":{"file_id":"full-song-mp3","file_unique_id":"u","duration":185}}}`)
	default:
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}
}

func newVoiceTestApp(t *testing.T, entries string) (*app, *voiceTestTelegram) {
	t.Helper()
	tg := &voiceTestTelegram{filePath: "voice/file.oga", body: "sample"}
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: tg})
	if err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	bin := filepath.Join(t.TempDir(), "yt-dlp")
	fixture := strings.ReplaceAll(entries, "'", "'\"'\"'")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s' '"+fixture+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dl := &downloader{downloadDir: t.TempDir(), bin: bin, maxFileSize: maxFileSize}
	a := newAppWithServices(context.Background(), bot, dl, state, config{LookupWorkers: 1, CacheTTL: time.Hour})
	a.setLang(10, "en")
	a.voiceFiles = handlerClient{handler: tg}
	a.recognizer = recognizerFunc(func(context.Context, string) (recognizedTrack, error) {
		return recognizedTrack{Title: "Track", Artist: "Artist"}, nil
	})
	tg.app = a
	if err := state.putCachedAudio(a.ctx, cachedAudio{
		Key: "youtube:full-song:mp3:320", FileID: "full-song-mp3", Title: "Track", Artist: "Artist",
		Duration: "3:05", Format: "mp3", Quality: "320", MediaType: "audio",
	}); err != nil {
		t.Fatal(err)
	}
	return a, tg
}

func voiceMessage() *tgbotapi.Message {
	return &tgbotapi.Message{
		Voice: &tgbotapi.Voice{FileID: "voice", Duration: 10, FileSize: 6},
		From:  &tgbotapi.User{ID: 10}, Chat: &tgbotapi.Chat{ID: 10, Type: "private"},
	}
}

const fullSongLookup = `{"entries":[{"id":"full-song","title":"Track","uploader":"Artist","duration":185,"url":"full-song"}]}`

func TestVoiceRecognitionSendsFullMP3RegardlessOfPreference(t *testing.T) {
	a, tg := newVoiceTestApp(t, fullSongLookup)
	if err := a.setPreference(10, "flac", "best"); err != nil {
		t.Fatal(err)
	}
	var sample string
	a.recognizer = recognizerFunc(func(ctx context.Context, path string) (recognizedTrack, error) {
		sample = path
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "sample" {
			t.Fatalf("sample=%q err=%v", data, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("sample permissions: info=%v err=%v", info, err)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("recognition is missing its deadline")
		}
		return recognizedTrack{Title: "Track", Artist: "Artist"}, nil
	})
	a.handleMessage(voiceMessage())
	if tg.audioCalls != 1 || tg.audio != "full-song-mp3" || !tg.slotHeld {
		t.Fatalf("expected full MP3 delivery: calls=%d audio=%q texts=%q", tg.audioCalls, tg.audio, tg.texts)
	}
	if len(tg.texts) == 0 || tg.texts[0] != tr("recognition_listening", "en") {
		t.Fatalf("recognition status=%q", tg.texts)
	}
	if _, err := os.Stat(filepath.Dir(sample)); !os.IsNotExist(err) {
		t.Fatalf("temporary recording remains: %v", err)
	}
	if active, _, _ := a.lookups.snapshot(); active != 0 {
		t.Fatal("recognition or search kept its lookup slot")
	}
	if !a.beginUserDownload(10) {
		t.Fatal("recognition kept its user slot after delivery")
	}
	a.finishUserDownload(10)
	format, quality, _ := a.getPreference(10)
	if format != "flac" || quality != "best" {
		t.Fatal("voice recognition changed the user's default format")
	}
}

func TestVoiceRecognitionUncertainMatchRequiresOneTimeMP3Choice(t *testing.T) {
	a, tg := newVoiceTestApp(t, `{"entries":[{"id":"full-song","title":"Track (Live)","uploader":"Artist","duration":185,"url":"full-song"}]}`)
	a.handleMessage(voiceMessage())
	if tg.audioCalls != 0 || len(tg.markups) != 1 {
		t.Fatalf("uncertain match was not offered: audio=%d markups=%#v", tg.audioCalls, tg.markups)
	}
	button := tg.markups[0].InlineKeyboard[0][0]
	if button.CallbackData == nil || !strings.HasPrefix(*button.CallbackData, "dl:mp3:320:") || len(*button.CallbackData) > 64 {
		t.Fatalf("invalid MP3 choice: %#v", button)
	}
	callback := &tgbotapi.CallbackQuery{ID: "choice", From: &tgbotapi.User{ID: 20}, Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 10}}, Data: *button.CallbackData}
	a.handleCallback(callback)
	if tg.audioCalls != 0 {
		t.Fatal("another user consumed the recognition result")
	}
	callback.From.ID = 10
	callback.Message.Chat.ID = 30
	a.handleCallback(callback)
	if tg.audioCalls != 0 {
		t.Fatal("the result was consumed from another chat")
	}
	callback.Message.Chat.ID = 10
	a.handleCallback(callback)
	a.handleCallback(callback)
	if tg.audioCalls != 1 || tg.audio != "full-song-mp3" {
		t.Fatalf("selection must deliver MP3 once: calls=%d audio=%q", tg.audioCalls, tg.audio)
	}
}

func TestVoiceRecognitionRejectsInvalidRequestsBeforeDownloading(t *testing.T) {
	cases := []struct {
		name   string
		change func(*app, *tgbotapi.Message)
		want   string
	}{
		{"short", func(_ *app, m *tgbotapi.Message) { m.Voice.Duration = 2 }, tr("recognition_too_short", "en")},
		{"long", func(_ *app, m *tgbotapi.Message) { m.Voice.Duration = 61 }, tr("recognition_too_long", "en")},
		{"large", func(_ *app, m *tgbotapi.Message) { m.Voice.FileSize = maxVoiceBytes + 1 }, tr("recognition_too_large", "en")},
		{"language", func(a *app, m *tgbotapi.Message) { m.From.ID = 99 }, chooseLanguageText},
		{"group", func(_ *app, m *tgbotapi.Message) { m.Chat.Type = "group" }, ""},
		{"active job", func(a *app, _ *tgbotapi.Message) { a.beginUserDownload(10) }, tr("user_download_active", "en")},
		{"rate limit", func(a *app, _ *tgbotapi.Message) { a.limiter = newRateLimiter(1, time.Hour); a.limiter.allow(10) }, "rate"},
		{"full queue", func(a *app, _ *tgbotapi.Message) { _, _, _ = a.lookups.acquire(a.ctx) }, tr("queue_full", "en")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			a, tg := newVoiceTestApp(t, fullSongLookup)
			a.recognizer = recognizerFunc(func(context.Context, string) (recognizedTrack, error) {
				t.Fatal("invalid request reached recognition")
				return recognizedTrack{}, nil
			})
			m := voiceMessage()
			tt.change(a, m)
			a.handleMessage(m)
			if tg.fileCalls != 0 || tg.audioCalls != 0 {
				t.Fatalf("invalid request downloaded a file: getFile=%d audio=%d", tg.fileCalls, tg.audioCalls)
			}
			if tt.want == "" {
				if len(tg.texts) != 0 {
					t.Fatalf("group voice should be ignored: %q", tg.texts)
				}
			} else if tt.want == "rate" {
				if len(tg.texts) != 1 || !strings.Contains(tg.texts[0], "too many requests") {
					t.Fatalf("rate limit response=%q", tg.texts)
				}
			} else if len(tg.texts) == 0 || tg.texts[len(tg.texts)-1] != tt.want {
				t.Fatalf("response=%q want=%q", tg.texts, tt.want)
			}
		})
	}
}

func TestVoiceRecognitionFailuresCleanUpAndExplain(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errMusicNotRecognized, "recognition_not_found"},
		{errRecognitionUnavailable, "recognition_unavailable"},
		{errRecognitionFailed, "recognition_error"},
		{errInvalidVoiceAudio, "recognition_invalid_audio"},
		{context.DeadlineExceeded, "recognition_timeout"},
	}
	for _, tt := range cases {
		t.Run(tt.want, func(t *testing.T) {
			a, tg := newVoiceTestApp(t, fullSongLookup)
			var sample string
			a.recognizer = recognizerFunc(func(_ context.Context, path string) (recognizedTrack, error) {
				sample = path
				return recognizedTrack{}, tt.err
			})
			a.handleMessage(voiceMessage())
			if tg.audioCalls != 0 || tg.texts[len(tg.texts)-1] != tr(tt.want, "en") {
				t.Fatalf("failure response=%q audio=%d", tg.texts, tg.audioCalls)
			}
			if _, err := os.Stat(filepath.Dir(sample)); !os.IsNotExist(err) {
				t.Fatalf("failed recognition left a recording: %v", err)
			}
			if !a.beginUserDownload(10) {
				t.Fatal("failed recognition kept its user slot")
			}
			a.finishUserDownload(10)
		})
	}
}

func TestVoiceFileDownloadEnforcesActualByteLimit(t *testing.T) {
	cases := []struct {
		name   string
		size   int
		body   string
		status int
		want   error
	}{
		{"metadata size", maxVoiceBytes + 1, "small", 200, errVoiceTooLarge},
		{"stream size", 0, strings.Repeat("x", maxVoiceBytes+1), 200, errVoiceTooLarge},
		{"empty", 0, "", 200, errInvalidVoiceAudio},
		{"server error", 0, "unavailable", 503, nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			a, tg := newVoiceTestApp(t, fullSongLookup)
			tg.fileSize, tg.body, tg.status = tt.size, tt.body, tt.status
			err := a.downloadVoiceFile(a.ctx, "voice", filepath.Join(t.TempDir(), "sample"))
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("download error=%v want=%v", err, tt.want)
			}
		})
	}
}

func TestVoiceFileURLSupportsLocalServerWithoutTrustingPaths(t *testing.T) {
	cases := []struct{ server, path, want string }{
		{"", "voice/file.oga", "https://api.telegram.org/file/bottoken/voice/file.oga"},
		{"http://telegram-proxy:8081", "voice/file name.oga", "http://telegram-proxy:8081/file/bottoken/voice/file%20name.oga"},
	}
	for _, tt := range cases {
		got, err := voiceFileURL(tt.server, "token", tt.path)
		if err != nil || got != tt.want {
			t.Errorf("voiceFileURL(%q, %q)=%q err=%v", tt.server, tt.path, got, err)
		}
	}
	for _, path := range []string{"", "../.env", "voice/../../.env", "voice/./file", "https://other.test/file", "/etc/passwd", "voice/file?token=secret", "voice\\file"} {
		for _, server := range []string{"", "http://telegram-bot-api:8081"} {
			if _, err := voiceFileURL(server, "token", path); err == nil {
				t.Errorf("unsafe Telegram file path accepted: %q", path)
			}
		}
	}
}

func TestVoiceDownloadReadsLocalBotAPIFile(t *testing.T) {
	a, tg := newVoiceTestApp(t, fullSongLookup)
	a.cfg.TelegramAPIURL = "http://telegram-bot-api:8081"
	a.cfg.TelegramFileDir = t.TempDir()
	source := filepath.Join(a.cfg.TelegramFileDir, a.bot.Token, "voice", "file.oga")
	if err := os.MkdirAll(filepath.Dir(source), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("local voice sample"), 0o640); err != nil {
		t.Fatal(err)
	}
	tg.filePath = source
	// Local getFile paths must work without a file-download HTTP client.
	a.voiceFiles = nil
	destination := filepath.Join(t.TempDir(), "sample")
	if err := a.downloadVoiceFile(a.ctx, "voice", destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "local voice sample" || tg.fileCalls != 1 {
		t.Fatalf("local sample=%q getFile calls=%d err=%v", data, tg.fileCalls, err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("sample is not private: info=%v err=%v", info, err)
	}
}

func TestLocalVoiceFileRejectsEscapesAndInvalidFiles(t *testing.T) {
	a, _ := newVoiceTestApp(t, fullSongLookup)
	a.cfg.TelegramAPIURL = "http://telegram-bot-api:8081"
	a.cfg.TelegramFileDir = t.TempDir()
	base := filepath.Join(a.cfg.TelegramFileDir, a.bot.Token)
	if err := os.MkdirAll(filepath.Join(base, "voice"), 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "sample.oga")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "voice", "escape.oga")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(base, "escape")
	if err := os.Symlink(filepath.Dir(outside), linkDir); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(base, "voice", "large.oga")
	if err := os.WriteFile(large, make([]byte, maxVoiceBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(base, "voice", "empty.oga")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(base, "voice", "fifo.oga")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, path string
		want       error
	}{
		{"outside root", outside, nil},
		{"other bot", filepath.Join(a.cfg.TelegramFileDir, "another-token", "voice", "file.oga"), nil},
		{"traversal", base + "/voice/../../another-token/file.oga", nil},
		{"file symlink", link, nil},
		{"directory symlink", filepath.Join(linkDir, filepath.Base(outside)), nil},
		{"directory", filepath.Join(base, "voice"), errInvalidVoiceAudio},
		{"fifo", fifo, errInvalidVoiceAudio},
		{"oversized", large, errVoiceTooLarge},
		{"empty", empty, errInvalidVoiceAudio},
		{"missing", filepath.Join(base, "voice", "missing.oga"), nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "sample")
			err := a.copyLocalVoiceFile(a.ctx, tt.path, destination)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("local sample error=%v want=%v", err, tt.want)
			}
			if strings.Contains(err.Error(), a.bot.Token) || strings.Contains(err.Error(), a.cfg.TelegramFileDir) {
				t.Fatalf("local sample error exposes a private path: %v", err)
			}
			if data, _ := os.ReadFile(destination); len(data) != 0 {
				t.Fatalf("rejected local sample was copied: %q", data)
			}
		})
	}
	ctx, cancel := context.WithCancel(a.ctx)
	cancel()
	if err := a.copyLocalVoiceFile(ctx, empty, filepath.Join(t.TempDir(), "sample")); !errors.Is(err, context.Canceled) {
		t.Fatalf("local copy ignored cancellation: %v", err)
	}
	a.cfg.TelegramFileDir = ""
	if err := a.copyLocalVoiceFile(a.ctx, empty, filepath.Join(t.TempDir(), "sample")); !errors.Is(err, errRecognitionUnavailable) {
		t.Fatalf("missing local directory error=%v", err)
	}
}

func TestVoiceFileClientRefusesRedirects(t *testing.T) {
	client := newVoiceFileClient()
	calls := 0
	client.Transport = voiceRedirectTransport{calls: &calls}
	response, err := client.Get("https://api.telegram.org/file/bottoken/voice/file.oga")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || calls != 1 {
		t.Fatalf("voice download followed a redirect: status=%d calls=%d", response.StatusCode, calls)
	}
}

type voiceRedirectTransport struct{ calls *int }

func (rt voiceRedirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*rt.calls++
	return &http.Response{StatusCode: http.StatusFound, Request: r,
		Header: http.Header{"Location": {"https://other.test/stolen-token"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
}

type voiceCancelClient struct{}

func (voiceCancelClient) Do(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/getFile") {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot"}}`)), Header: make(http.Header)}, nil
}

func TestVoiceMetadataRequestHonorsJobCancellation(t *testing.T) {
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", voiceCancelClient{})
	if err != nil {
		t.Fatal(err)
	}
	a := newApp(context.Background(), bot, nil)
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Millisecond)
	defer cancel()
	err = a.downloadVoiceFile(ctx, "voice", filepath.Join(t.TempDir(), "sample"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("getFile did not honor its deadline: %v", err)
	}
}
