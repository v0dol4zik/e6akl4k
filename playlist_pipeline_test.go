package main

import (
	"context"
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

// fakePlaylistYTDLP writes a fake yt-dlp serving a playlist of count tracks: it downloads the
// requested --playlist-items and appends one byte per downloaded track to the returned counter.
func fakePlaylistYTDLP(t *testing.T, count int) (bin, counter string) {
	t.Helper()
	directory := t.TempDir()
	counter = filepath.Join(directory, "downloaded.log")
	entries := make([]string, count)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"id":"id%d","title":"Track %d","uploader":"Artist","duration":1,"playlist_index":%d,"extractor":"youtube"}`, i+1, i+1, i+1)
	}
	bin = filepath.Join(directory, "fake-yt-dlp")
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
	return bin, counter
}

func TestPlaylistPipelineDownloadsNextBatchWhileUploading(t *testing.T) {
	bin, counter := fakePlaylistYTDLP(t, 20)
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

func TestPlaylistZIPDeliveryGoesInBatches(t *testing.T) {
	bin, counter := fakePlaylistYTDLP(t, 120)
	var mu sync.Mutex
	var names, captions []string
	var summary string
	telegramHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"test_bot"}}`)
			return
		case "sendDocument":
			if err := r.ParseMultipartForm(2 << 20); err == nil && len(r.MultipartForm.File["document"]) == 1 {
				mu.Lock()
				names = append(names, r.MultipartForm.File["document"][0].Filename)
				captions = append(captions, r.FormValue("caption"))
				mu.Unlock()
			}
		case "sendMessage":
			mu.Lock()
			summary = r.FormValue("text")
			mu.Unlock()
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99,"date":1,"chat":{"id":10,"type":"private"}}}`)
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegramHandler})
	if err != nil {
		t.Fatal(err)
	}
	downloadDir := t.TempDir()
	dl := &downloader{downloadDir: downloadDir, bin: bin, maxFileSize: maxFileSize, maxPlaylistTracks: maxPlaylistTracks}
	application := newAppWithServices(context.Background(), bot, dl, nil, config{DownloadWorkers: 2, LookupWorkers: 1, ArchiveWorkers: 1, MaxFileSize: maxFileSize, MaxPlaylistTracks: maxPlaylistTracks})
	pending := pendingURL{URL: "https://www.youtube.com/playlist?list=PL3", RangeStart: 1, RangeEnd: 120, Delivery: "zip", Preview: mediaPreview{IsPlaylist: true, TrackCount: 120}}
	if !batchedPlaylistDelivery(pending, "mp3", "320") {
		t.Fatal("a 120-track ZIP selection is not delivered in batches")
	}
	report, err := application.downloadAndSendPlaylistZIPs(context.Background(), 10, pending, "mp3", "320", "en", nil)
	if err != nil || report.Delivered != 120 || report.Failed != 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if info, err := os.Stat(counter); err != nil || info.Size() != 120 {
		t.Fatalf("downloaded tracks: %v %v", info, err)
	}
	mu.Lock()
	defer mu.Unlock()
	wantNames := []string{"playlist_001-050_", "playlist_051-100_", "playlist_101-120_"}
	wantCaptions := []string{"tracks 1–50", "tracks 51–100", "tracks 101–120"}
	if len(names) != len(wantNames) {
		t.Fatalf("archives=%v", names)
	}
	for i := range wantNames {
		if !strings.HasPrefix(names[i], wantNames[i]) || !strings.HasSuffix(names[i], ".zip") || !strings.Contains(captions[i], wantCaptions[i]) {
			t.Fatalf("archive %d: name=%q caption=%q", i+1, names[i], captions[i])
		}
	}
	if !strings.Contains(summary, "120/120") {
		t.Fatalf("summary=%q", summary)
	}
	if left, err := os.ReadDir(downloadDir); err != nil || len(left) != 0 {
		t.Fatalf("sessions left on disk: %v %v", left, err)
	}
}
