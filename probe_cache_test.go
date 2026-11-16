package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeJSONCacheStoreGet(t *testing.T) {
	cache := newProbeJSONCache()
	if got := cache.get("https://vk.ru/x"); got != nil {
		t.Fatalf("empty cache returned %#v", got)
	}
	cache.store(" https://vk.ru/x ", []byte(`{"id":"1"}`))
	if got := string(cache.get("https://vk.ru/x")); got != `{"id":"1"}` {
		t.Fatalf("get = %q", got)
	}
}

func TestProbeJSONCacheTTLAndLimits(t *testing.T) {
	cache := newProbeJSONCache()
	cache.store("url", []byte("data"))
	cache.mu.Lock()
	entry := cache.entries["url"]
	entry.at = time.Now().Add(-probeJSONTTL - time.Minute)
	cache.entries["url"] = entry
	cache.mu.Unlock()
	if got := cache.get("url"); got != nil {
		t.Fatalf("expired entry must not be returned: %q", got)
	}
	cache.store("url", []byte("data"))
	cache.mu.Lock()
	if _, ok := cache.entries["url"]; !ok {
		cache.mu.Unlock()
		t.Fatal("fresh entry must be kept")
	}
	cache.mu.Unlock()

	tooBig := make([]byte, (4<<20)+1)
	cache.store("big", tooBig)
	if got := cache.get("big"); got != nil {
		t.Fatal("oversized dumps must be skipped")
	}
}

func TestMaybeCacheProbeJSONKeepsSinglesOnly(t *testing.T) {
	d := &downloader{probeCache: newProbeJSONCache()}
	d.maybeCacheProbeJSON("https://vk.ru/audio1_2", &mediaInfo{ID: "1_2", Type: "video"}, []byte(`{"id":"1_2"}`))
	if d.freshProbeJSON("https://vk.ru/audio1_2") == nil {
		t.Fatal("a single-track dump must be cached")
	}

	d.maybeCacheProbeJSON("https://vk.ru/audio_playlist1_2", &mediaInfo{ID: "pl", Type: "playlist", Entries: []*mediaInfo{{ID: "a"}}}, []byte(`{"id":"pl"}`))
	if d.freshProbeJSON("https://vk.ru/audio_playlist1_2") != nil {
		t.Fatal("playlist dumps must not be cached")
	}

	d.maybeCacheProbeJSON("https://example.com/x", &mediaInfo{Type: "video"}, []byte(`{}`))
	if d.freshProbeJSON("https://example.com/x") != nil {
		t.Fatal("a dump without an ID is useless for --load-info")
	}
}

func TestSourceAlreadyMp3(t *testing.T) {
	if !sourceAlreadyMp3("https://vk.ru/audio700781870_456239768_47d170798742400f46", "mp3") {
		t.Fatal("a VK single audio must skip re-encoding for mp3 targets")
	}
	if !sourceAlreadyMp3("https://vk.com/audio700781870_456239768", "mp3") {
		t.Fatal("vk.com single audio must skip re-encoding too")
	}
	for _, url := range []string{
		"https://vk.ru/audio_playlist700781870_1",
		"https://vk.ru/music/album/x",
		"https://www.youtube.com/watch?v=aaaaaaaaaaa",
		"https://soundcloud.com/artist/track",
	} {
		if sourceAlreadyMp3(url, "mp3") {
			t.Fatalf("no re-encode fast path must apply to %q", url)
		}
	}
	if sourceAlreadyMp3("https://vk.ru/audio1_2", "m4a") || sourceAlreadyMp3("https://vk.ru/audio1_2", "flac") {
		t.Fatal("the fast path only makes sense for mp3 targets")
	}
}

func TestProbeJSONDownloaded(t *testing.T) {
	dir := t.TempDir()
	if probeJSONDownloaded(filepath.Join(dir, "missing.json")) {
		t.Fatal("no manifest means nothing was downloaded")
	}
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"id":"abc","title":"T"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !probeJSONDownloaded(manifest) {
		t.Fatal("a manifest with an entry means the fast pass worked")
	}
	if err := os.WriteFile(manifest, []byte(``), 0o600); err != nil {
		t.Fatal(err)
	}
	if probeJSONDownloaded(manifest) {
		t.Fatal("an empty manifest means the fast pass produced nothing")
	}
}

func TestProbeJSONCacheEvictionKeepsRoom(t *testing.T) {
	cache := newProbeJSONCache()
	for i := 0; i < probeCacheSize+8; i++ {
		cache.store("url"+string(rune('a'+i)), []byte("d"))
	}
	cache.mu.Lock()
	count := len(cache.entries)
	cache.mu.Unlock()
	if count > probeCacheSize {
		t.Fatalf("cache must stay capped: %d entries", count)
	}
}
