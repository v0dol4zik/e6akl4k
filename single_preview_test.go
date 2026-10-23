package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSingleYouTubeFormatChoiceAvoidsDuplicateExtraction(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		disabled, unavailable bool
	}{
		{name: "metadata preview"},
		{name: "oembed unavailable", unavailable: true},
		{name: "single extraction disabled", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBatchHarness(t)
			bin, dir := blockingMediaTool(t, false)
			h.app.downloader.bin = bin
			h.app.cfg.YouTubeSingleExtract = !tc.disabled
			releaseMarker(t, dir, "aaaaaaaaaaa")
			metadataCalls := 0
			previous := makeResolverClient
			makeResolverClient = func() httpDoer {
				return handlerClient{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					metadataCalls++
					if r.URL.Scheme != "https" || r.URL.Host != "www.youtube.com" || r.URL.Path != "/oembed" || r.URL.Query().Get("url") != "https://www.youtube.com/watch?v=aaaaaaaaaaa" {
						t.Errorf("unexpected metadata endpoint: %s", r.URL)
					}
					deadline, ok := r.Context().Deadline()
					if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 3*time.Second {
						t.Error("metadata request exceeds three-second budget")
					}
					if active, _, _ := h.app.lookups.snapshot(); active != 1 {
						t.Errorf("metadata request bypassed lookup gate: active=%d", active)
					}
					if tc.unavailable {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					fmt.Fprint(w, `{"title":"Title","author_name":"Artist"}`)
				})}
			}
			t.Cleanup(func() { makeResolverClient = previous })
			rawURL := "https://music.youtube.com/watch?v=aaaaaaaaaaa&si=share"
			h.app.openURL(batchMessage(rawURL), rawURL, "en", "", "")
			key := h.pendingKey(t)
			pending, ok := h.app.getURL(key, 10, 10)
			quick := !tc.disabled && !tc.unavailable
			if !ok || pending.URL != rawURL || pending.Preview.SourceID != "aaaaaaaaaaa" || pending.Preview.direct != quick {
				t.Fatalf("pending=%+v ok=%t", pending, ok)
			}
			if quick && (pending.Preview.Title != "Title" || pending.Preview.Artist != "Artist" || pending.Preview.DurationSeconds != 0) {
				t.Fatalf("metadata preview=%+v", pending.Preview)
			}
			wantMetadataCalls := 1
			if tc.disabled {
				wantMetadataCalls = 0
			}
			if metadataCalls != wantMetadataCalls {
				t.Fatalf("metadata calls=%d, want %d", metadataCalls, wantMetadataCalls)
			}
			calls := h.snapshot()
			if h.count("sendAudio", calls) != 0 {
				t.Fatalf("download started before format choice: %+v", calls)
			}
			keyboard := false
			for _, call := range calls {
				keyboard = keyboard || strings.Contains(call.markup, "dl:mp3:best:"+key)
			}
			if !keyboard {
				t.Fatalf("format choice missing: %+v", calls)
			}
			before, err := os.ReadFile(filepath.Join(dir, "calls.log"))
			wantBefore := ""
			if !quick {
				wantBefore = "probe:aaaaaaaaaaa\n"
			}
			if (err != nil && !os.IsNotExist(err)) || string(before) != wantBefore {
				t.Fatalf("source calls before choice=%q err=%v", before, err)
			}
			h.app.handleCallback(batchCallback("dl:mp3:best:" + key))
			after, err := os.ReadFile(filepath.Join(dir, "calls.log"))
			if err != nil || string(after) != wantBefore+"download:aaaaaaaaaaa\n" {
				t.Fatalf("source extraction repeated after choice: %q err=%v", after, err)
			}
			if calls := h.snapshot(); h.count("sendAudio", calls) != 1 {
				t.Fatalf("delivery failed: %+v", calls)
			}
			if active, waiting, _ := h.app.lookups.snapshot(); active != 0 || waiting != 0 {
				t.Fatalf("lookup gate leaked: %d/%d", active, waiting)
			}
		})
	}
}

func TestFastYouTubePreviewKeepsPlaylistAndSourceGuards(t *testing.T) {
	h := newBatchHarness(t)
	h.app.cfg.YouTubeSingleExtract = true
	previous := makeResolverClient
	makeResolverClient = func() httpDoer {
		t.Error("ineligible preview made a metadata request")
		return previous()
	}
	t.Cleanup(func() { makeResolverClient = previous })
	for _, rawURL := range []string{
		"https://youtube.com/watch?v=aaaaaaaaaaa&list=PL1",
		"https://youtube.com/playlist?list=PL1",
		"https://youtube.com.evil/watch?v=aaaaaaaaaaa",
		"https://user@youtube.com/watch?v=aaaaaaaaaaa",
		"https://youtube.com:444/watch?v=aaaaaaaaaaa",
		"https://youtu.be/invalid",
		"https://example.com/audio",
	} {
		if preview, ok := h.app.fastYouTubePreview(context.Background(), rawURL); ok || preview.SourceID != "" {
			t.Fatalf("ineligible link got a direct preview: %s %+v", rawURL, preview)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if preview, ok := h.app.fastYouTubePreview(ctx, "https://youtu.be/aaaaaaaaaaa"); ok || preview.SourceID != "" {
		t.Fatalf("cancelled request got a direct preview: %+v", preview)
	}
}
