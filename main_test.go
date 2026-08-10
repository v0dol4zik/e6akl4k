package main

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPendingURLIsBoundToOwnerAndChat(t *testing.T) {
	a := newApp(context.Background(), nil, nil)
	want := pendingURL{URL: "https://youtu.be/example", UserID: 10, ChatID: -20}
	key, err := a.storeURL(want)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.popURL(key, 11, -20); ok {
		t.Fatal("another user consumed the URL")
	}
	if _, ok := a.popURL(key, 10, -21); ok {
		t.Fatal("URL was consumed from another chat")
	}
	if got, ok := a.popURL(key, 10, -20); !ok || got != want {
		t.Fatalf("owner could not consume URL: got %#v, ok %v", got, ok)
	}
	if _, ok := a.popURL(key, 10, -20); ok {
		t.Fatal("URL was consumed twice")
	}
}

func TestPendingZIPIsBoundToOwnerAndChat(t *testing.T) {
	a := newApp(context.Background(), nil, nil)
	want := zipRequest{Format: "mp3", UserID: 10, ChatID: -20}
	key, err := a.storeZIP(want)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.popZIP(key, 11, -20); ok {
		t.Fatal("another user consumed the ZIP request")
	}
	if _, ok := a.popZIP(key, 10, -21); ok {
		t.Fatal("ZIP request was consumed from another chat")
	}
	if got, ok := a.popZIP(key, 10, -20); !ok || got.Format != want.Format {
		t.Fatalf("owner could not consume ZIP request: got %#v, ok %v", got, ok)
	}
}

func TestActiveDownloadIsBoundToOwnerAndChat(t *testing.T) {
	a := newApp(context.Background(), nil, nil)
	a.active["download"] = activeDownload{userID: 10, chatID: -20}
	if _, ok := a.getActiveDownload("download", 11, -20); ok {
		t.Fatal("another user accessed the active download")
	}
	if _, ok := a.getActiveDownload("download", 10, -21); ok {
		t.Fatal("active download was accessed from another chat")
	}
	if _, ok := a.getActiveDownload("download", 10, -20); !ok {
		t.Fatal("owner could not access the active download")
	}
}

func TestPlaylistTrackLimit(t *testing.T) {
	if err := validatePlaylistSize(maxPlaylistTracks); err != nil {
		t.Fatalf("playlist at the limit was rejected: %v", err)
	}
	err := validatePlaylistSize(maxPlaylistTracks + 1)
	var tooLarge playlistTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("unexpected error type: %T (%v)", err, err)
	}
	if tooLarge.Count != 76 || tooLarge.Limit != 75 {
		t.Fatalf("unexpected limit error: %#v", tooLarge)
	}
}

func TestDetectURL(t *testing.T) {
	tests := map[string]string{
		"смотри youtu.be/dQw4w9WgXcQ":                  "https://youtu.be/dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=abc&list=xyz": "https://www.youtube.com/watch?v=abc&list=xyz",
		"http://youtube.com@127.0.0.1/private":         "",
		"не ссылка":                                    "",
	}
	for input, want := range tests {
		if got := detectURL(input); got != want {
			t.Errorf("detectURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTranslationFallbackAndValues(t *testing.T) {
	if got := tr("downloaded_count", "unknown", "count", "12"); got != "📦 скачано треков: <b>12</b>. как отправить?" {
		t.Fatalf("unexpected translation: %q", got)
	}
}

func TestFormatETA(t *testing.T) {
	if got := formatETA(30*time.Second, "ru"); got != "меньше минуты" {
		t.Fatalf("formatETA short = %q", got)
	}
	if got := formatETA(3*time.Minute+20*time.Second, "ru"); got != "около 3 мин." {
		t.Fatalf("formatETA minutes = %q", got)
	}
}

func TestBuildCaptionEscapesMetadata(t *testing.T) {
	result := downloadResult{FilePath: "track.mp3", Title: "A < B", Artist: "one & two", Duration: "3:14"}
	caption := buildCaption(result, 1024, "mp3", "ru", 1, 1)
	if strings.Contains(caption, "A < B") || strings.Contains(caption, "one & two") {
		t.Fatalf("caption is not escaped: %q", caption)
	}
	if !strings.Contains(caption, "A &lt; B") || !strings.Contains(caption, "one &amp; two") {
		t.Fatalf("escaped values are missing: %q", caption)
	}
}

func TestCreateZIPKeepsDuplicateTracks(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.mp3")
	second := filepath.Join(dir, "second.mp3")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "tracks.zip")
	results := []downloadResult{
		{FilePath: first, Title: "same", Artist: "artist"},
		{FilePath: second, Title: "same", Artist: "artist"},
	}
	if err := createZIP(archive, results); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) != 2 || reader.File[0].Name == reader.File[1].Name {
		t.Fatalf("duplicate files were not preserved: %#v", reader.File)
	}
}
