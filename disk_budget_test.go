package main

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiskBudgetWaitsForReleasedSpace(t *testing.T) {
	budget := newDiskBudget(func() (int64, error) { return 1000, nil }, 200)
	budget.poll = time.Millisecond
	first, err := budget.acquire(context.Background(), 600, nil)
	if err != nil {
		t.Fatal(err)
	}
	var waited atomic.Int32
	acquired := make(chan error, 1)
	go func() {
		release, err := budget.acquire(context.Background(), 300, func() { waited.Add(1) })
		if err == nil {
			release()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		t.Fatalf("second batch did not wait for space: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	first()
	first()
	if err := <-acquired; err != nil {
		t.Fatal(err)
	}
	if waited.Load() != 1 {
		t.Fatalf("waiting was reported %d times", waited.Load())
	}
	if budget.reserved != 0 {
		t.Fatalf("reserved=%d after every release", budget.reserved)
	}
}

func TestDiskBudgetGivesUpAndHonoursCancellation(t *testing.T) {
	budget := newDiskBudget(func() (int64, error) { return 100, nil }, 50)
	budget.poll, budget.timeout = time.Millisecond, 20*time.Millisecond
	if _, err := budget.acquire(context.Background(), 60, nil); !errors.Is(err, errDiskFull) {
		t.Fatalf("err=%v", err)
	}
	budget.timeout = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := budget.acquire(ctx, 60, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	unreadable := newDiskBudget(func() (int64, error) { return 0, errors.New("statfs") }, 50)
	if release, err := unreadable.acquire(context.Background(), 60, nil); err != nil {
		t.Fatalf("an unreadable disk must not block downloads: %v", err)
	} else {
		release()
	}
	var none *diskBudget
	if release, err := none.acquire(context.Background(), 60, nil); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
}

func TestPlaylistZIPBatchSizeFollowsFormatWeight(t *testing.T) {
	for _, test := range []struct {
		format, quality string
		want            int
	}{
		{"mp3", "320", 50},
		{"mp3", "128", 50},
		{"m4a", "best", 50},
		{"flac", "best", 24},
	} {
		if got := playlistZIPBatchSize(test.format, test.quality); got != test.want {
			t.Errorf("%s:%s batch=%d, want %d", test.format, test.quality, got, test.want)
		}
	}
	pending := pendingURL{URL: "https://www.youtube.com/playlist?list=PL", RangeStart: 1, RangeEnd: 30, Delivery: "zip", Preview: mediaPreview{IsPlaylist: true, TrackCount: 30}}
	if batchedPlaylistDelivery(pending, "mp3", "320") || !batchedPlaylistDelivery(pending, "flac", "best") {
		t.Fatal("30 tracks are one MP3 archive but two FLAC batches")
	}
}

func TestEstimateSelectionUsesKnownLengths(t *testing.T) {
	preview := mediaPreview{IsPlaylist: true, TrackCount: 3, Tracks: []exportTrack{{Title: "a", Seconds: 60}, {Title: "b"}, {Title: "c", Seconds: 120}}}
	want := estimateAudioSize(60, "mp3", "320") + estimateAudioSize(averageTrackSeconds, "mp3", "320")
	if got := estimateSelectionBytes(preview, 1, 2, "mp3", "320"); got != want {
		t.Fatalf("estimate=%d, want %d", got, want)
	}
	pending := pendingURL{RangeStart: 1, RangeEnd: 200, Preview: mediaPreview{IsPlaylist: true, TrackCount: 394}}
	flac := selectionEstimate(pending, "flac", "best", "en")
	if !strings.Contains(flac, "FLAC") || !strings.Contains(flac, "200") || !strings.Contains(flac, "MP3 320") {
		t.Fatalf("flac estimate=%q", flac)
	}
	if mp3 := selectionEstimate(pending, "mp3", "320", "en"); strings.Count(mp3, "MP3 320") != 1 {
		t.Fatalf("mp3 estimate=%q", mp3)
	}
}

func TestCreateZIPConsumesPackedTracks(t *testing.T) {
	dir := t.TempDir()
	var results []downloadResult
	for _, name := range []string{"one", "two"} {
		path := filepath.Join(dir, name+".mp3")
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		results = append(results, downloadResult{FilePath: path, Title: name})
	}
	archive := filepath.Join(dir, "batch.zip")
	if err := createZIP(archive, results, true); err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if _, err := os.Stat(result.FilePath); !os.IsNotExist(err) {
			t.Fatalf("%s is still on disk: %v", result.FilePath, err)
		}
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) != 2 {
		t.Fatalf("archive holds %d files", len(reader.File))
	}
}

func TestPlaylistStopsWhenDiskStaysFull(t *testing.T) {
	bin, _ := fakeSearchYTDLP(t)
	telegram := &exportTelegram{}
	application := newExportTestApp(t, telegram)
	downloadDir := t.TempDir()
	application.downloader = &downloader{downloadDir: downloadDir, bin: bin, maxFileSize: maxFileSize, maxPlaylistTracks: maxPlaylistTracks}
	application.cfg.MaxFileSize, application.cfg.MaxPlaylistTracks = maxFileSize, maxPlaylistTracks
	application.disk = newDiskBudget(func() (int64, error) { return 1 << 20, nil }, 1<<30)
	application.disk.poll, application.disk.timeout = time.Millisecond, 10*time.Millisecond
	tracks := []exportTrack{{Artist: "A", Title: "One"}, {Artist: "B", Title: "Two"}}
	pending := pendingURL{ChatID: 10, UserID: 10, Delivery: "zip", Preview: mediaPreview{IsPlaylist: true, TrackCount: len(tracks), Tracks: tracks, Extractor: tracklistExtractor}}
	report, err := application.downloadAndSendPlaylistZIPs(context.Background(), 10, pending, "mp3", "320", "en", nil)
	if !errors.Is(err, errDiskFull) || report.Delivered != 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	for _, call := range telegram.snapshot() {
		if call.method == "sendDocument" {
			t.Fatal("an archive was sent without disk space")
		}
	}
	if left, err := os.ReadDir(downloadDir); err != nil || len(left) != 0 {
		t.Fatalf("files left on disk: %v %v", left, err)
	}
}
