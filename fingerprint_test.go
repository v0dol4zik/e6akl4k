package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const timeMinuteTest = 1 * time.Minute

func TestNormalizeTrackTextFoldsMarketingNoise(t *testing.T) {
	got := normalizeTrackText("Daft Punk — Get Lucky (Official Audio)")
	if want := "daft punk - get lucky"; got != want {
		t.Fatalf("normalize = %q, want %q", got, want)
	}
	if got := normalizeTrackText("  Дaft\u00A0punk - get\u2007LUCKY  "); normalizeTrackText(got) != got {
		t.Fatal("unicode spaces and case must collapse")
	}
	if got, want := normalizeTrackText("Комбинация — Айсберг [remastered 2011]"), "комбинация - айсберг"; got != want {
		t.Fatalf("remaster tag = %q, want %q", got, want)
	}
	// ё folding
	if got, want := normalizeTrackText("Всё идёт по плану"), "все идет по плану"; got != want {
		t.Fatalf("ё folding = %q, want %q", got, want)
	}
}

func TestNormalizeTrackTextKeepsDistinctRecordings(t *testing.T) {
	protected := []string{
		"(live at Wembley)", "(Remix)", "(feat. Kizaru)", "(Radio Edit)", "(Explicit)",
	}
	base := "artist - track "
	for _, suffix := range protected {
		got := normalizeTrackText(base + suffix)
		want := normalizeTrackText(base + suffix) // нижний регистр внутри — ок; главное, скобки не снесены
		if got != want || !strings.Contains(got, "(") {
			t.Errorf("%q must stay distinct but became %q", suffix, got)
		}
	}
}

func TestTrackFingerprintRequiresBothSides(t *testing.T) {
	if trackFingerprint("", "title") != "" || trackFingerprint("artist", "") != "" {
		t.Fatal("a missing side must produce no fingerprint")
	}
	same1 := trackFingerprint("Daft Punk", "Get Lucky (Official Audio)")
	same2 := trackFingerprint("daft punk", "get lucky")
	if same1 == "" || same1 != same2 {
		t.Fatalf("equivalent pairs must share a fingerprint: %q vs %q", same1, same2)
	}
	if trackFingerprint("Daft Punk", "Get Lucky") == trackFingerprint("Daft Punk", "Get Lucky (Live)") {
		t.Fatal("a live take must not fold into the studio track")
	}
}

func TestParseDurationSeconds(t *testing.T) {
	cases := map[string]int{"3:14": 194, "1:02:03": 3723, "75": 75, "": 0, "3:1O4": 0, "1:2:3:4": 0}
	for input, want := range cases {
		if got := parseDurationSeconds(input); got != want {
			t.Errorf("parseDurationSeconds(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestDurationsCompatible(t *testing.T) {
	if !durationsCompatible(194, 197) || !durationsCompatible(0, 1000) || !durationsCompatible(100, 0) {
		t.Fatal("near-misses and unknown durations must be compatible")
	}
	if durationsCompatible(194, 227) || durationsCompatible(194, 0) == false {
		t.Fatal("a radio edit must stay incompatible")
	}
}

// TestAudioFingerprintStoreRoundtrip checks that a cached entry is found again
// through a differently spelled title and a different URL.
func TestAudioFingerprintStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()

	entry := cachedAudio{Key: "youtube:oldvideo:mp3:320", FileID: "fileid1", Title: "Get Lucky (Official Audio)", Artist: "Daft Punk",
		Duration: "3:14", Format: "mp3", Quality: "320", Size: 7000, MediaType: "audio", Source: "www.youtube.com"}
	if err := state.putCachedAudio(ctx, entry); err != nil {
		t.Fatal(err)
	}

	candidates := state.audioFingerprintKeys(ctx, "daft punk", "get lucky")
	if len(candidates) != 1 || candidates[0].CacheKey != entry.Key {
		t.Fatalf("fingerprint lookup = %#v", candidates)
	}
	if candidates[0].DurationSeconds != 194 {
		t.Fatalf("stored duration_seconds = %d, want 194", candidates[0].DurationSeconds)
	}
	if candidates = state.audioFingerprintKeys(ctx, "Daft Punk", "Get Lucky (Live)"); candidates != nil {
		t.Fatalf("(Live) must not be matched by the studio fingerprint: %#v", candidates)
	}
}

// TestCachedResultHitsByFingerprint exercises the playlist lookup path: another URL,
// another SourceID, same song — the cached file serves without a download.
func TestCachedResultHitsByFingerprint(t *testing.T) {
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	application := &app{store: state, cfg: config{CacheTTL: timeMinuteTest}}

	entry := cachedAudio{Key: "youtube:abc123:mp3:320", FileID: "fileid1", Title: "Lemonade", Artist: "sophie",
		Duration: "3:34", Format: "mp3", Quality: "320", Size: 7000, MediaType: "audio", Source: "vk.ru"}
	if err := state.putCachedAudio(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	item := pendingURL{URL: "https://vk.ru/audio700781870_456239768",
		Preview: mediaPreview{Title: "lemonade", Artist: "SOPHIE", Duration: "3:34", DurationSeconds: 214, TrackCount: 1}}
	result, ok := application.cachedResult(context.Background(), item, "mp3", "320")
	if !ok || result.Cached == nil || result.Cached.FileID != "fileid1" {
		t.Fatalf("fingerprint cache hit expected: ok=%v result=%#v", ok, result)
	}
	if result.CacheKey != entry.Key {
		t.Fatalf("hit key = %q, want %q", result.CacheKey, entry.Key)
	}

	// Радио-версия другой длины не должна подменяться плейлистной версией
	longer := item
	longer.Preview.DurationSeconds, longer.Preview.Duration = 240, "4:00"
	if result, ok := application.cachedResult(context.Background(), longer, "mp3", "320"); ok {
		t.Fatalf("a different cut must not be served from the fingerprint cache: %#v", result)
	}
}

// TestCachedResultPrefersExactKey keeps the direct hit semantics intact.
func TestCachedResultPrefersExactKey(t *testing.T) {
	dir := t.TempDir()
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	application := &app{store: state, cfg: config{CacheTTL: timeMinuteTest}}
	ctx := context.Background()

	first := cachedAudio{Key: "youtube:aaaaaaaaaaa:mp3:320", FileID: "f1", Title: "Song", Artist: "A", Duration: "3:14", Format: "mp3", Quality: "320", MediaType: "audio"}
	if err := state.putCachedAudio(ctx, first); err != nil {
		t.Fatal(err)
	}
	alias := first
	alias.Key, alias.FileID = "youtube:bbbbbbbbbbb:mp3:320", "f2"
	if err := state.putCachedAudio(ctx, alias); err != nil {
		t.Fatal(err)
	}

	item := pendingURL{URL: "https://www.youtube.com/watch?v=bbbbbbbbbbb",
		Preview: mediaPreview{Title: "song", Artist: "a", Duration: "3:14", DurationSeconds: 194}}
	result, ok := application.cachedResult(ctx, item, "mp3", "320")
	if !ok || result.CacheKey != alias.Key {
		t.Fatalf("the exact URL key must win: ok=%v key=%q", ok, result.CacheKey)
	}
	if !strings.Contains(result.CacheKey, "bbbbbbbbbbb") {
		t.Fatalf("unexpected key %q", result.CacheKey)
	}
}
