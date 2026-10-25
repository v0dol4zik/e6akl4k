package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// Opt-in: real session files and links stay outside the repository. No URLs,
// cookie values, tokens or extractor JSON are printed on this path.
func TestVKMusicLive(t *testing.T) {
	input := os.Getenv("VK_MUSIC_LIVE_INPUTS")
	if input == "" {
		t.Skip("set VK_MUSIC_LIVE_INPUTS and VK_COOKIES_FILE for live verification")
	}
	var inputs struct{ Tracks []string }
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal("cannot read private VK live inputs")
	}
	if json.Unmarshal(data, &inputs) != nil || len(inputs.Tracks) == 0 {
		t.Fatal("invalid private VK live inputs")
	}
	bin := os.Getenv("VK_MUSIC_LIVE_YTDLP")
	if bin == "" {
		bin, err = exec.LookPath("yt-dlp")
		if err != nil {
			t.Fatal(err)
		}
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	d := &downloader{bin: bin, ffmpegBin: ffmpeg, downloadDir: t.TempDir(), vkCookiesFile: os.Getenv("VK_COOKIES_FILE"), maxFileSize: maxFileSize}
	for i, raw := range inputs.Tracks {
		t.Run(strconv.Itoa(i+1), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			started := time.Now()
			preview, err := d.preview(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			previewTime := time.Since(started)
			if preview.DurationSeconds <= 0 || preview.IsPlaylist || preview.TrackCount != 1 || preview.Extractor != "vk_music" {
				t.Fatal("invalid live VK track preview")
			}
			results, err := d.downloadRangeWithPreview(ctx, raw, "mp3", "best", 0, 0, preview, nil)
			defer d.clearSessions(results)
			if err != nil || len(results) != 1 {
				t.Fatalf("VK download failed: %v", err)
			}
			if results[0].FilePath == "" || results[0].Error != "" {
				t.Fatal(results[0].Error)
			}
			stat, err := os.Stat(results[0].FilePath)
			if err != nil {
				t.Fatal("missing final VK audio")
			}
			t.Logf("full audio verified: duration=%ds bytes=%d preview=%s total=%s", results[0].DurationSeconds, stat.Size(), previewTime.Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
		})
	}
}
