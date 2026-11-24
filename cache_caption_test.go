package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildCaptionShowsSourceAndQuality checks the plain caption format: no emoji
// decoration, one information line with format, quality, source and size.
func TestBuildCaptionShowsSourceAndQuality(t *testing.T) {
	result := downloadResult{FilePath: "lemonade.mp3", Title: "lemonade", Artist: "sophie",
		Duration: "3:34", Quality: "320", URL: "https://vk.ru/audio700781870_456239768", Source: "vk.ru"}
	caption := buildCaption(result, 7_300_000, "mp3", "ru", 1, 1)
	for _, want := range []string{
		"<b>lemonade</b>",
		"sophie",
		"3:34",
		"MP3 320",
		"VK",
		"7.0 МБ",
	} {
		if !strings.Contains(caption, want) {
			t.Fatalf("caption missing %q: %q", want, caption)
		}
	}
	for _, banned := range []string{"👤", "⏱", "📦", "🎵", "🎧", "  ", " | "} {
		if strings.Contains(caption, banned) {
			t.Fatalf("caption must not contain %q: %q", banned, caption)
		}
	}
}

// TestBuildCaptionDerivesSourceFromURL checks that fresh downloads without a
// stored Source still show the real download host from their page URL.
func TestBuildCaptionDerivesSourceFromURL(t *testing.T) {
	result := downloadResult{Title: "e.g.", Quality: "320", URL: "https://www.youtube.com/watch?v=abc"}
	if caption := buildCaption(result, 1000, "mp3", "en", 1, 1); !strings.Contains(caption, "MP3 320 · YouTube") {
		t.Fatalf("caption must list format, quality and source: %q", caption)
	}
	if caption := buildCaption(result, 1000, "mp3", "en", 1, 1); strings.Contains(caption, "· ·") {
		t.Fatalf("caption must drop unknown parts cleanly: %q", caption)
	}
	bare := downloadResult{Title: "e.g."}
	if caption := buildCaption(bare, 1000, "mp3", "en", 1, 1); !strings.Contains(caption, "MP3") || strings.Contains(caption, "· ·") {
		t.Fatalf("caption without source must stay clean: %q", caption)
	}
}

// TestBuildCaptionOmitsUnknownSource keeps unknown hosts out of the caption.
func TestBuildCaptionOmitsUnknownSource(t *testing.T) {
	result := downloadResult{Title: "x", URL: "https://weird.example.com/track"}
	if caption := buildCaption(result, 1000, "mp3", "ru", 1, 1); strings.Contains(caption, "weird") || strings.Contains(caption, "example") {
		t.Fatalf("unknown host must not be shown: %q", caption)
	}
}

// TestSourceDisplayNameMapping covers host aliases the bot downloads from.
func TestSourceDisplayNameMapping(t *testing.T) {
	cases := map[string]string{
		"https://youtu.be/abc":         "YouTube",
		"https://www.youtube.com/x":    "YouTube",
		"https://music.youtube.com/x":  "YouTube Music",
		"soundcloud.com":               "SoundCloud",
		"https://x.bandcamp.com/album": "Bandcamp",
		"https://www.newgrounds.com/a": "Newgrounds",
		"https://vk.ru/audio1_2":       "VK",
		"https://vk.com/audio1_2":      "VK",
		"mixcloud.com":                 "Mixcloud",
		"https://audiomack.com/song":   "Audiomack",
		"music.yandex.ru/album/1":      "Яндекс Музыка",
		"":                             "",
		"unknown":                      "",
		"weird.host":                   "",
	}
	for raw, want := range cases {
		if got := sourceDisplayName(raw); got != want {
			t.Errorf("sourceDisplayName(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestCachedAudioKeepsSource checks the SQLite roundtrip of the new column.
func TestCachedAudioKeepsSource(t *testing.T) {
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	entry := cachedAudio{Key: "k1", FileID: "f1", Format: "mp3", Quality: "320", Size: 123, MediaType: "audio", Source: "vk.ru"}
	if err := state.putCachedAudio(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	stored, ok := state.cachedAudio(context.Background(), "k1", time.Minute)
	if !ok || stored.Source != "vk.ru" || stored.Quality != "320" {
		t.Fatalf("stored=%#v ok=%v", stored, ok)
	}
	// overwrite keeps the latest source
	entry.Source = "www.youtube.com"
	if err := state.putCachedAudio(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if stored, _ := state.cachedAudio(context.Background(), "k1", time.Minute); stored.Source != "www.youtube.com" {
		t.Fatalf("source must follow upsert: %#v", stored)
	}
}
