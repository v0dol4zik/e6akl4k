package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func blockingMediaTool(t *testing.T, blockProbe bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	phase := "download"
	if blockProbe {
		phase = "probe"
	}
	script := `#!/bin/sh
url=''; dir=''; manifest=''; phase=download
for arg in "$@"; do url="$arg"; done
id=${url##*/}
case "$url" in *watch\?v=*) id=${url##*v=}; id=${id%%&*} ;; esac
case " $* " in *" --simulate "*) phase=probe ;; esac
printf '%s:%s\n' "$phase" "$id" >> '` + dir + `/calls.log'
if [ "$phase" = '` + phase + `' ]; then
  : > '` + dir + `/started-'"$id"
  while [ ! -f '` + dir + `/release-'"$id" ]; do sleep 0.01; done
fi
if [ "$phase" = probe ]; then
  printf '{"id":"%s","title":"%s","duration":180,"extractor":"youtube"}' "$id" "$id"
  exit 0
fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    --paths) dir="$2"; shift 2 ;;
    --print-to-file)
      case "$2" in after_move:*) manifest="$3" ;; esac
      shift 3 ;;
    *) shift ;;
  esac
done
path="$dir/$id.mp3"
printf audio > "$path"
printf '{"id":"%s","title":"%s","duration":180,"extractor":"youtube","filepath":"%s"}\n' "$id" "$id" "$path" >> "$manifest"
`
	bin := filepath.Join(dir, "fake-yt-dlp")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func waitMarker(t *testing.T, dir, id string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "started-"+id)); err == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("process %s did not start", id)
		case <-tick.C:
		}
	}
}

func releaseMarker(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "release-"+id), nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBatchPreviewsOverlapWithoutExceedingLookupGate(t *testing.T) {
	h := newBatchHarness(t)
	bin, dir := blockingMediaTool(t, true)
	h.app.downloader.bin = bin
	h.app.lookups = newJobGate(2, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	urls := []string{"https://youtu.be/aaaaaaaaaaa", "https://youtu.be/bbbbbbbbbbb", "https://youtu.be/ccccccccccc"}
	done := make(chan []batchInspection, 1)
	go func() { done <- h.app.inspectBatchLinks(ctx, urls, "mp3", "best") }()
	for _, id := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb"} {
		waitMarker(t, dir, id)
	}
	if _, err := os.Stat(filepath.Join(dir, "started-ccccccccccc")); !os.IsNotExist(err) {
		t.Fatal("third preview bypassed the global gate")
	}
	for _, id := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc"} {
		releaseMarker(t, dir, id)
	}
	select {
	case results := <-done:
		for i, result := range results {
			if result.err != nil || result.preview.URL != urls[i] {
				t.Fatalf("result[%d]=%+v", i, result)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("previews did not finish")
	}
}

func TestBatchDownloadsOverlapPreserveOrderAndDeduplicateAliases(t *testing.T) {
	h := newBatchHarness(t)
	bin, dir := blockingMediaTool(t, false)
	h.app.downloader.bin = bin
	h.app.downloads = newJobGate(3, 0)
	h.app.cfg.DownloadWorkers = 3
	h.app.cfg.BatchDownloadWorkers = 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	items := []pendingURL{{URL: "https://youtu.be/aaaaaaaaaaa"}, {URL: "https://youtu.be/bbbbbbbbbbb"}, {URL: "https://youtu.be/ccccccccccc"}, {URL: "https://music.youtube.com/watch?v=aaaaaaaaaaa&si=alias"}}
	type outcome struct {
		results []downloadResult
		release func()
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		r, release, err := h.app.downloadConcurrent(ctx, items, "mp3", "best", nil)
		done <- outcome{r, release, err}
	}()
	for _, id := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb"} {
		waitMarker(t, dir, id)
	}
	if active, _, _ := h.app.downloads.snapshot(); active != 2 {
		t.Fatalf("active=%d", active)
	}
	_, thirdSlot, err := h.app.downloads.acquire(ctx)
	if err != nil {
		t.Fatalf("another request cannot use third worker: %v", err)
	}
	thirdSlot()
	if _, err := os.Stat(filepath.Join(dir, "started-ccccccccccc")); !os.IsNotExist(err) {
		t.Fatal("batch exceeded two simultaneous tasks")
	}
	for _, id := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc"} {
		releaseMarker(t, dir, id)
	}
	select {
	case result := <-done:
		defer result.release()
		defer h.app.downloader.clearSessions(result.results)
		if result.err != nil || len(result.results) != 4 {
			t.Fatalf("results=%+v err=%v", result.results, result.err)
		}
		for i, id := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc", "aaaaaaaaaaa"} {
			if result.results[i].Title != id || result.results[i].Error != "" {
				t.Fatalf("result[%d]=%+v", i, result.results[i])
			}
		}
		if result.results[0].FilePath != result.results[3].FilePath {
			t.Fatal("URL aliases downloaded twice")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("downloads did not finish")
	}
}

func TestBatchContinuationWorksWithoutWaitingQueueAndCancellationCleansFiles(t *testing.T) {
	for _, cancelDownload := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelDownload), func(t *testing.T) {
			h := newBatchHarness(t)
			bin, dir := blockingMediaTool(t, false)
			h.app.downloader.bin = bin
			h.app.downloads = newJobGate(1, 0)
			h.app.cfg.DownloadWorkers = 1
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			items := []pendingURL{{URL: "https://youtu.be/aaaaaaaaaaa"}, {URL: "https://youtu.be/bbbbbbbbbbb"}}
			done := make(chan error, 1)
			go func() {
				r, release, err := h.app.downloadConcurrent(ctx, items, "mp3", "best", nil)
				h.app.downloader.clearSessions(r)
				release()
				done <- err
			}()
			waitMarker(t, dir, "aaaaaaaaaaa")
			if cancelDownload {
				cancel()
			} else {
				releaseMarker(t, dir, "aaaaaaaaaaa")
				waitMarker(t, dir, "bbbbbbbbbbb")
				releaseMarker(t, dir, "bbbbbbbbbbb")
			}
			select {
			case err := <-done:
				if cancelDownload && !errors.Is(err, context.Canceled) || !cancelDownload && err != nil {
					t.Fatalf("err=%v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("request did not stop")
			}
			if active, waiting, _ := h.app.downloads.snapshot(); active != 0 || waiting != 0 {
				t.Fatalf("gate leaked: %d/%d", active, waiting)
			}
			left, err := os.ReadDir(h.app.downloader.downloadDir)
			if err != nil || len(left) != 0 {
				t.Fatalf("files left: %v %v", left, err)
			}
			if h.app.disk.reserved != 0 {
				t.Fatalf("reserved=%d", h.app.disk.reserved)
			}
		})
	}
}

func TestCachedYouTubeLinkSkipsProbeAndLookupQueue(t *testing.T) {
	h := newBatchHarness(t)
	id := "abcdefghijk"
	cacheTrack(t, h.state, id, "Cached title")
	if err := h.app.setPreference(10, "mp3", "320"); err != nil {
		t.Fatal(err)
	}
	h.app.downloader.bin = "/does-not-exist"
	h.app.lookups = newJobGate(1, 0)
	_, release, err := h.app.lookups.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	h.app.openURL(batchMessage(""), "https://music.youtube.com/watch?v="+id+"&si=one", "en", "", "")
	calls := h.snapshot()
	if h.count("sendAudio", calls) != 1 {
		t.Fatalf("cache was not sent: %+v", calls)
	}
	for _, call := range calls {
		if strings.Contains(call.text, "error") {
			t.Fatalf("unexpected error: %+v", call)
		}
	}
}

func TestEarlyCacheRejectsPlaylistAndUnsafeAliases(t *testing.T) {
	for _, rawURL := range []string{"https://www.youtube.com/watch?v=abcdefghijk&list=PL1", "https://youtube.com/playlist?list=PL1", "https://youtube.com.evil/watch?v=abcdefghijk", "https://user@youtube.com/watch?v=abcdefghijk", "https://youtube.com:444/watch?v=abcdefghijk", "https://youtube.com:/watch?v=abcdefghijk"} {
		if id := singleYouTubeID(rawURL); id != "" {
			t.Fatalf("%q became %q", rawURL, id)
		}
	}
}

func TestPreferredYouTubeUsesOneExtractionWithoutLookupSlot(t *testing.T) {
	h := newBatchHarness(t)
	bin, dir := blockingMediaTool(t, false)
	h.app.downloader.bin = bin
	h.app.cfg.YouTubeSingleExtract = true
	h.app.lookups = newJobGate(1, 0)
	_, release, err := h.app.lookups.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	releaseMarker(t, dir, "aaaaaaaaaaa")
	h.app.openURL(batchMessage(""), "https://youtu.be/aaaaaaaaaaa", "en", "mp3", "best")
	calls, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if err != nil || string(calls) != "download:aaaaaaaaaaa\n" {
		t.Fatalf("source calls=%q err=%v", calls, err)
	}
	if calls := h.snapshot(); h.count("sendAudio", calls) != 1 {
		t.Fatalf("delivery: %+v", calls)
	}
}

func TestStreamingSendsBeforeSecondTrackAndBoundsReadyQueue(t *testing.T) {
	h := newBatchHarness(t)
	bin, dir := blockingMediaTool(t, false)
	h.app.downloader.bin = bin
	h.app.cfg.DownloadWorkers = 3
	h.app.cfg.BatchDownloadWorkers = 2
	h.app.downloads = newJobGate(3, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstSend, uploadRelease := make(chan struct{}), make(chan struct{})
	var sendOnce sync.Once
	h.onSendAudio = func() { sendOnce.Do(func() { close(firstSend); <-uploadRelease }) }
	var uploadOnce sync.Once
	releaseUpload := func() { uploadOnce.Do(func() { close(uploadRelease) }) }
	defer releaseUpload()
	items := []pendingURL{{URL: "https://youtu.be/aaaaaaaaaaa"}, {URL: "https://youtu.be/bbbbbbbbbbb"}, {URL: "https://youtu.be/ccccccccccc"}}
	type outcome struct {
		report deliveryReport
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := h.app.streamBatch(ctx, 10, items, "mp3", "best", "en", &statusReporter{ctx: ctx})
		done <- outcome{r, err}
	}()
	waitMarker(t, dir, "aaaaaaaaaaa")
	waitMarker(t, dir, "bbbbbbbbbbb")
	releaseMarker(t, dir, "aaaaaaaaaaa")
	select {
	case <-firstSend:
	case <-time.After(5 * time.Second):
		t.Fatal("first track waited for the second source")
	}
	if _, err := os.Stat(filepath.Join(dir, "started-ccccccccccc")); !os.IsNotExist(err) {
		t.Fatal("a slow upload did not bound prepared files")
	}
	if active, _, _ := h.app.downloads.snapshot(); active != 1 {
		t.Fatalf("sending track retained its download slot: active=%d", active)
	}
	releaseUpload()
	waitMarker(t, dir, "ccccccccccc")
	releaseMarker(t, dir, "bbbbbbbbbbb")
	releaseMarker(t, dir, "ccccccccccc")
	select {
	case result := <-done:
		if result.err != nil || result.report.Delivered != 3 {
			t.Fatalf("%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not finish")
	}
	if files, _ := os.ReadDir(h.app.downloader.downloadDir); len(files) != 0 {
		t.Fatalf("remaining sessions: %v", files)
	}
}

func TestConcurrentRecipientsShareSourceAndOwnTheirFiles(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			h := newBatchHarness(t)
			bin, dir := blockingMediaTool(t, false)
			h.app.downloader.bin = bin
			h.app.cfg.YouTubeSingleExtract = true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				results []downloadResult
				err     error
			}
			first, second := make(chan outcome, 1), make(chan outcome, 1)
			run := func(ctx context.Context, rawURL string, done chan outcome) {
				preview, _ := h.app.directYouTubePreview(rawURL, "mp3", "best")
				results, err := h.app.runDownloadRangeQueuedWithPreview(ctx, rawURL, "mp3", "best", 0, 0, preview, nil, nil)
				done <- outcome{results, err}
			}
			go run(ctx, "https://youtu.be/aaaaaaaaaaa", first)
			waitMarker(t, dir, "aaaaaaaaaaa")
			go run(context.Background(), "https://music.youtube.com/watch?v=aaaaaaaaaaa&si=x", second)
			deadline := time.Now().Add(5 * time.Second)
			for {
				h.app.sourceFlights.mu.Lock()
				call := h.app.sourceFlights.calls["youtube:aaaaaaaaaaa:mp3:best"]
				joined := call != nil && call.refs == 2
				h.app.sourceFlights.mu.Unlock()
				if joined {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("second recipient did not join")
				}
				time.Sleep(time.Millisecond)
			}
			if cancelFirst {
				cancel()
			}
			releaseMarker(t, dir, "aaaaaaaaaaa")
			one, two := <-first, <-second
			defer h.app.downloader.clearSessions(one.results)
			defer h.app.downloader.clearSessions(two.results)
			if two.err != nil || len(two.results) != 1 || !regularFileExists(two.results[0].FilePath) {
				t.Fatalf("second=%+v", two)
			}
			if cancelFirst {
				if !errors.Is(one.err, context.Canceled) {
					t.Fatalf("first=%+v", one)
				}
			} else {
				if one.err != nil || len(one.results) != 1 || one.results[0].FilePath == two.results[0].FilePath {
					t.Fatalf("first=%+v second=%+v", one, two)
				}
				h.app.downloader.clearSessions(one.results)
				if !regularFileExists(two.results[0].FilePath) {
					t.Fatal("first recipient removed the other's file")
				}
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
			if string(calls) != "download:aaaaaaaaaaa\n" {
				t.Fatalf("source executed more than once: %q", calls)
			}
		})
	}
}

func TestPlaylistSnapshotReusesPositionsCacheAndAliases(t *testing.T) {
	h := newBatchHarness(t)
	bin, dir := blockingMediaTool(t, false)
	h.app.downloader.bin = bin
	h.app.cfg.YouTubeSingleExtract = true
	releaseMarker(t, dir, "aaaaaaaaaaa")
	cacheTrack(t, h.state, "bbbbbbbbbbb", "Cached")
	snapshot := youtubePlaylistSnapshot("https://youtube.com/playlist?list=PLx", &mediaInfo{Entries: []*mediaInfo{{ID: "aaaaaaaaaaa", Title: "First", Duration: 180}, nil, {ID: "bbbbbbbbbbb", Title: "Cached", Duration: 180}, {ID: "aaaaaaaaaaa", Title: "Repeat", Duration: 180}}})
	pending := pendingURL{RangeStart: 1, RangeEnd: 4, Preview: mediaPreview{IsPlaylist: true, TrackCount: 4, PlaylistSnapshot: snapshot}}
	results, err := h.app.downloadPlaylistRange(context.Background(), pending, "mp3", "320", "en", 1, 4, nil)
	defer h.app.downloader.clearSessions(results)
	if err != nil || len(results) != 4 {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if results[1].Error == "" || results[2].Cached == nil || results[0].FilePath == "" || results[0].FilePath != results[3].FilePath {
		t.Fatalf("results=%+v", results)
	}
	for i, r := range results {
		if r.Position != i+1 || r.Total != 4 {
			t.Fatalf("position: %+v", r)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	if string(calls) != "download:aaaaaaaaaaa\n" {
		t.Fatalf("repeated listing or download: %q", calls)
	}
}
