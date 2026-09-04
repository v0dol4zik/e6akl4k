package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestPlaylistPipelineDownloadsNextBatchWhileUploading(t *testing.T) {
	var audioCalls atomic.Int32
	octaveHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/album/3":
			w.Header().Set("Content-Type", "application/json")
			var tracks strings.Builder
			for index := 1; index <= 20; index++ {
				if index > 1 {
					tracks.WriteByte(',')
				}
				fmt.Fprintf(&tracks, `{"id":"%d","title":"Track %d","duration":1}`, index, index)
			}
			fmt.Fprintf(w, `{"album":{"id":"3","title":"Album","artist":{"id":"2","name":"Artist"},"tracks":[%s]}}`, tracks.String())
		case "/api/playback-token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"octk_test_token","expiresIn":7200}`)
		case "/audio/320":
			audioCalls.Add(1)
			w.Header().Set("Content-Type", "audio/mpeg")
			fmt.Fprint(w, "audio")
		default:
			http.NotFound(w, r)
		}
	})

	var groups atomic.Int32
	var overlapped atomic.Bool
	telegramHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"test_bot"}}`)
		case "sendMediaGroup":
			group := groups.Add(1)
			if group == 1 {
				deadline := time.Now().Add(time.Second)
				for audioCalls.Load() < 20 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if audioCalls.Load() == 20 {
					overlapped.Store(true)
				}
			}
			_ = r.ParseMultipartForm(2 << 20)
			var messages strings.Builder
			for index := 0; index < 10; index++ {
				if index > 0 {
					messages.WriteByte(',')
				}
				fmt.Fprintf(&messages, `{"message_id":%d,"date":1,"chat":{"id":10,"type":"private"},"audio":{"file_id":"f%d","file_unique_id":"u%d","duration":1}}`, index+1, index, index)
			}
			fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, messages.String())
		default:
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":99,"date":1,"chat":{"id":10,"type":"private"}}}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegramHandler})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	dl := &downloader{downloadDir: directory, maxFileSize: maxFileSize, maxPlaylistTracks: 75, octave: testOctaveClient(octaveHandler)}
	application := newAppWithServices(context.Background(), bot, dl, nil, config{DownloadWorkers: 2, LookupWorkers: 1, ArchiveWorkers: 1, MaxFileSize: maxFileSize})
	pending := pendingURL{URL: "https://music.octavestreaming.com/album/3", RangeStart: 1, RangeEnd: 20, Preview: mediaPreview{IsPlaylist: true, TrackCount: 20}}
	report, err := application.downloadAndSendPlaylistBatches(context.Background(), 10, pending, "mp3", "320", "en", nil, nil)
	if err != nil || report.Delivered != 20 || report.Failed != 0 || groups.Load() != 2 {
		t.Fatalf("report=%#v groups=%d err=%v", report, groups.Load(), err)
	}
	if !overlapped.Load() {
		t.Fatal("second batch was not downloaded during the first Telegram upload")
	}
}
