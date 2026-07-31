package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
