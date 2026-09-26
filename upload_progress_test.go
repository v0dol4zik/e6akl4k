package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProgressFileCanBeReopenedAndReportsBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	reporter := &statusReporter{lang: "en", updates: make(chan string, 1), lastPct: -1}
	progress := newUploadBatchProgress(reporter, 10)
	file := progressFile{path: path, progress: progress}
	for attempt := 0; attempt < 2; attempt++ {
		name, reader, err := file.UploadData()
		if err != nil || name != "track.mp3" {
			t.Fatalf("name=%q err=%v", name, err)
		}
		data, err := io.ReadAll(reader)
		if err != nil || string(data) != "0123456789" {
			t.Fatalf("data=%q err=%v", data, err)
		}
	}
	select {
	case update := <-reporter.updates:
		// A fully read file waits for Telegram to store it.
		if !strings.Contains(update, "processing the file") || !strings.Contains(update, "10.0 B") {
			t.Fatalf("unexpected progress update: %q", update)
		}
	default:
		t.Fatal("upload progress was not reported")
	}
}
