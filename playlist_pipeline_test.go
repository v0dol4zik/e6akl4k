package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestPlaylistPipelineDownloadsNextBatchWhileUploading(t *testing.T) {
	directory := t.TempDir()
	counter := filepath.Join(directory, "downloaded.log")
	entries := make([]string, 20)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"id":"id%d","title":"Track %d","uploader":"Artist","duration":1,"playlist_index":%d,"extractor":"youtube"}`, i+1, i+1, i+1)
	}
	bin := filepath.Join(directory, "fake-yt-dlp")
	script := `#!/bin/sh
case " $* " in
  *" --simulate "*) printf '%s' '{"_type":"playlist","id":"PL3","title":"Album","entries":[` + strings.Join(entries, ",") + `]}' ; exit 0 ;;
esac
dir=''; manifest=''; progress=''; items=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --paths) dir="$2"; shift 2 ;;
    --playlist-items) items="$2"; shift 2 ;;
    --print-to-file)
      template="$2"; output="$3"
      case "$template" in after_move:*) manifest="$output" ;; before_dl:*) progress="$output" ;; esac
      shift 3 ;;
    *) shift ;;
  esac
done
oldifs="$IFS"; IFS=,
for item in $items; do
  path="$dir/$(printf '%06d' "$item")_id$item.mp3"
  printf 'audio' > "$path"
  printf '%s\n' "$item" >> "$progress"
  printf '{"id":"id%s","title":"Track %s","uploader":"Artist","duration":1,"playlist_index":%s,"filepath":"%s","ext":"mp3","extractor":"youtube"}\n' "$item" "$item" "$item" "$path" >> "$manifest"
  printf 'x' >> ` + counter + `
done
IFS="$oldifs"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	downloaded := func() int {
		info, err := os.Stat(counter)
		if err != nil {
			return 0
		}
		return int(info.Size())
	}

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
				deadline := time.Now().Add(5 * time.Second)
				for downloaded() < 20 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if downloaded() == 20 {
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
	dl := &downloader{downloadDir: t.TempDir(), bin: bin, maxFileSize: maxFileSize, maxPlaylistTracks: 75}
	application := newAppWithServices(context.Background(), bot, dl, nil, config{DownloadWorkers: 2, LookupWorkers: 1, ArchiveWorkers: 1, MaxFileSize: maxFileSize})
	pending := pendingURL{URL: "https://www.youtube.com/playlist?list=PL3", RangeStart: 1, RangeEnd: 20, Preview: mediaPreview{IsPlaylist: true, TrackCount: 20}}
	report, err := application.downloadAndSendPlaylistBatches(context.Background(), 10, pending, "mp3", "320", "en", nil, nil)
	if err != nil || report.Delivered != 20 || report.Failed != 0 || groups.Load() != 2 {
		t.Fatalf("report=%#v groups=%d err=%v", report, groups.Load(), err)
	}
	if !overlapped.Load() {
		t.Fatal("second batch was not downloaded during the first Telegram upload")
	}
}
