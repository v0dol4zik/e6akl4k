package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestInlineService() *inlineService {
	return &inlineService{
		candidates: make(map[string]inlineCandidate),
		active:     make(map[string]inlineActiveDownload),
		activeUser: make(map[int64]string),
		queries:    make(map[int64]inlineActiveQuery),
		fileIDs:    make(map[string]string),
	}
}

func TestLatestInlineQueryCancelsPreviousOne(t *testing.T) {
	service := newTestInlineService()
	firstCtx, firstID := service.beginQuery(context.Background(), 10)
	secondCtx, secondID := service.beginQuery(context.Background(), 10)
	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("previous inline query was not cancelled")
	}
	service.finishQuery(10, firstID)
	select {
	case <-secondCtx.Done():
		t.Fatal("finishing an old query cancelled the current query")
	default:
	}
	service.finishQuery(10, secondID)
	select {
	case <-secondCtx.Done():
	default:
		t.Fatal("current inline query context was not released")
	}
}

func TestInlineLookupReturnsDirectURLWithoutStartingYTDLP(t *testing.T) {
	d := downloader{bin: "/does/not/exist"}
	candidates, err := d.inlineLookup(context.Background(), "https://youtu.be/video-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].URL != "https://youtu.be/video-id" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

func TestOnlyOneInlineDownloadPerUser(t *testing.T) {
	service := newTestInlineService()
	first := inlineActiveDownload{cancel: func() {}, userID: 10, inlineMessageID: "first"}
	second := inlineActiveDownload{cancel: func() {}, userID: 10, inlineMessageID: "second"}
	if !service.setActive("first", first) {
		t.Fatal("first inline download was rejected")
	}
	if service.setActive("second", second) {
		t.Fatal("second inline download for the same user was accepted")
	}
	service.clearActive("first")
	if !service.setActive("second", second) {
		t.Fatal("new inline download was rejected after the first one finished")
	}
}

func TestInlineCandidateIsBoundToUser(t *testing.T) {
	service := newTestInlineService()
	id, err := service.storeCandidate(inlineCandidate{URL: "https://youtu.be/test", UserID: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.takeCandidate(id, 11); ok {
		t.Fatal("another user consumed an inline result")
	}
	if candidate, ok := service.takeCandidate(id, 10); !ok || candidate.URL == "" {
		t.Fatalf("owner could not consume inline result: %#v, %v", candidate, ok)
	}
	if _, ok := service.takeCandidate(id, 10); ok {
		t.Fatal("inline result was consumed twice")
	}
}

func TestExpiredInlineCandidateIsRejected(t *testing.T) {
	service := newTestInlineService()
	service.candidates["expired"] = inlineCandidate{UserID: 10, ExpiresAt: time.Now().Add(-time.Second)}
	if _, ok := service.takeCandidate("expired", 10); ok {
		t.Fatal("expired inline result was accepted")
	}
}

func TestInlineCancelIsBoundToUserAndMessage(t *testing.T) {
	service := newTestInlineService()
	cancelled := make(chan struct{}, 1)
	service.active["result"] = inlineActiveDownload{
		cancel:          func() { cancelled <- struct{}{} },
		userID:          10,
		inlineMessageID: "message",
	}
	if service.cancelDownload("result", 11, "message") {
		t.Fatal("another user cancelled the inline download")
	}
	if service.cancelDownload("result", 10, "other-message") {
		t.Fatal("download was cancelled from another inline message")
	}
	if !service.cancelDownload("result", 10, "message") {
		t.Fatal("owner could not cancel inline download")
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("cancel function was not called")
	}
}

func TestInlineCachePersistsFileIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inline-cache.json")
	service := newTestInlineService()
	service.cachePath = path
	if err := service.cacheFileID("track", "file-id"); err != nil {
		t.Fatal(err)
	}
	loaded := newTestInlineService()
	loaded.cachePath = path
	if err := loaded.loadCache(); err != nil {
		t.Fatal(err)
	}
	if got := loaded.cachedFileID("track"); got != "file-id" {
		t.Fatalf("cached file ID = %q", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected cache permissions: info=%v err=%v", info, err)
	}
}

func TestInlineHelpers(t *testing.T) {
	if got := inlineDurationSeconds("1:02:03"); got != 3723 {
		t.Fatalf("duration = %d", got)
	}
	if got := inlineDurationSeconds("invalid"); got != 0 {
		t.Fatalf("invalid duration = %d", got)
	}
	first := inlineCacheKey("https://example.com/a", "")
	second := inlineCacheKey("https://example.com/b", "")
	if first == second || !strings.HasPrefix(first, "url:") {
		t.Fatalf("unexpected URL cache keys: %q %q", first, second)
	}
	if got := inlineCacheKey("ignored", "video-id"); got != "youtube:video-id:mp3:320" {
		t.Fatalf("unexpected YouTube cache key: %q", got)
	}
	caption := inlineResultCaption(inlineCandidate{Title: "A < B", Artist: "one & two"}, "en", true)
	if strings.Contains(caption, "A < B") || strings.Contains(caption, "one & two") {
		t.Fatalf("inline caption is not escaped: %q", caption)
	}
	markupJSON, err := json.Marshal(emptyInlineKeyboard())
	if err != nil {
		t.Fatal(err)
	}
	if string(markupJSON) != `{"inline_keyboard":[]}` {
		t.Fatalf("empty inline keyboard = %s", markupJSON)
	}
}

func TestInlineLookupBuildsSearchCandidates(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fake-yt-dlp")
	script := `#!/bin/sh
printf '%s' '{"entries":[{"id":"video-id","title":"Track","uploader":"Artist","duration":125,"url":"video-id"}]}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d := downloader{bin: bin}
	candidates, err := d.inlineLookup(context.Background(), "track name")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d", len(candidates))
	}
	got := candidates[0]
	if got.URL != "https://www.youtube.com/watch?v=video-id" || got.Title != "Track" || got.Artist != "Artist" || got.Duration != "2:05" {
		t.Fatalf("unexpected candidate: %#v", got)
	}
	if got.CacheKey != "youtube:video-id:mp3:320" {
		t.Fatalf("unexpected cache key: %q", got.CacheKey)
	}
}

func TestSearchesUseYouTube(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fake-yt-dlp")
	script := `#!/bin/sh
printf '%s' '{"entries":[{"id":"youtube-id","title":"Fallback","uploader":"Artist","duration":60,"url":"youtube-id"}]}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d := downloader{bin: bin}
	searches := []struct {
		name string
		run  func() ([]inlineCandidate, error)
	}{
		{name: "inline", run: func() ([]inlineCandidate, error) {
			return d.inlineLookup(context.Background(), "fallback track")
		}},
		{name: "private", run: func() ([]inlineCandidate, error) {
			return d.searchLookup(context.Background(), "fallback track", 60)
		}},
	}
	for _, search := range searches {
		t.Run(search.name, func(t *testing.T) {
			candidates, err := search.run()
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) != 1 || candidates[0].Extractor != "youtube" || candidates[0].SourceID != "youtube-id" {
				t.Fatalf("unexpected YouTube candidates: %#v", candidates)
			}
		})
	}
}
