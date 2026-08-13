package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDownloadRangeSelectsOnlyRequestedPlaylistItems(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-yt-dlp")
	entries := make([]string, 30)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"id":"id%d","title":"Track %d","duration":60,"playlist_index":%d,"extractor":"youtube"}`, i+1, i+1, i+1)
	}
	script := `#!/bin/sh
case " $* " in
  *" --simulate "*) printf '%s' '{"_type":"playlist","title":"List","entries":[` + strings.Join(entries, ",") + `]}' ; exit 0 ;;
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
  printf '{"id":"id%s","title":"Track %s","duration":60,"playlist_index":%s,"filepath":"%s","extractor":"youtube"}\n' "$item" "$item" "$item" "$path" >> "$manifest"
done
IFS="$oldifs"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d := downloader{bin: bin, downloadDir: dir, maxFileSize: maxFileSize, maxPlaylistTracks: 75}
	results, err := d.downloadRange(context.Background(), "https://youtube.com/playlist?list=x", "mp3", "320", 11, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if len(results) > 0 {
			d.clearSession(results[0].Session)
		}
	}()
	if len(results) != 10 {
		t.Fatalf("results=%d", len(results))
	}
	if results[0].Title != "Track 11" || results[9].Title != "Track 20" {
		t.Fatalf("range=%q..%q", results[0].Title, results[9].Title)
	}
	if results[0].CacheKey != "youtube:id11:mp3:320" {
		t.Fatalf("cache key=%q", results[0].CacheKey)
	}
}

func TestMaxDurationFor(t *testing.T) {
	d := downloader{maxFileSize: maxFileSize}
	tests := map[string]int{
		"mp3:128":   2875,
		"mp3:320":   1150,
		"mp3:best":  1445,
		"m4a:best":  890,
		"ogg:best":  2442,
		"flac:best": 217,
	}
	for key, want := range tests {
		parts := strings.SplitN(key, ":", 2)
		if got := d.maxDurationFor(parts[0], parts[1]); got != want {
			t.Errorf("maxDurationFor(%q, %q) = %d, want %d", parts[0], parts[1], got, want)
		}
	}
}

func TestAudioFormatArgs(t *testing.T) {
	tests := []struct {
		format  string
		quality string
		want    []string
	}{
		{"mp3", "best", []string{"--audio-format", "mp3", "--audio-quality", "0"}},
		{"mp3", "320", []string{"--audio-format", "mp3", "--audio-quality", "320K"}},
		{"flac", "best", []string{"--audio-format", "flac"}},
		{"ogg", "best", []string{"--audio-format", "vorbis", "--audio-quality", "5"}},
	}
	for _, test := range tests {
		if got := audioFormatArgs(test.format, test.quality); !reflect.DeepEqual(got, test.want) {
			t.Errorf("audioFormatArgs(%q, %q) = %#v, want %#v", test.format, test.quality, got, test.want)
		}
	}
}

func TestBestErrorLine(t *testing.T) {
	message := "WARNING: transient warning\nERROR: actual failure"
	if got := bestErrorLine(message); got != "ERROR: actual failure" {
		t.Fatalf("bestErrorLine() = %q", got)
	}
}

func TestValidAudioPathStaysInsideSession(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "session")
	if err := os.Mkdir(session, 0o700); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(session, "track.mp3")
	outside := filepath.Join(root, "outside.mp3")
	for _, path := range []string{audio, outside} {
		if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := downloader{}
	if _, ok := d.validAudioPath(session, audio); !ok {
		t.Fatal("audio inside the session was rejected")
	}
	if _, ok := d.validAudioPath(session, outside); ok {
		t.Fatal("audio outside the session was accepted")
	}
}

func TestManifestLineCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.jsonl")
	content := "{\"id\":\"one\"}\nnot-json\n{\"id\":\"two\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := manifestLineCount(path); got != 2 {
		t.Fatalf("manifestLineCount() = %d, want 2", got)
	}
}

func TestOperationsUseIsolatedCookieSnapshot(t *testing.T) {
	dir := t.TempDir()
	cookies := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(cookies, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &downloader{downloadDir: dir, cookiesFile: cookies}
	if err := d.refreshCookieSnapshot(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookies, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := d.isolatedCookieFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" || snapshot == cookies {
		t.Fatalf("snapshot=%q content=%q", snapshot, data)
	}
}

func TestArgsBeforeSeparator(t *testing.T) {
	got := argsBeforeSeparator([]string{"--format", "audio", "--", "https://example.test"}, "--cookies", "snapshot.txt")
	want := []string{"--format", "audio", "--cookies", "snapshot.txt", "--", "https://example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%#v, want %#v", got, want)
	}
}
