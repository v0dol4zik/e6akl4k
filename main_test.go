package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
