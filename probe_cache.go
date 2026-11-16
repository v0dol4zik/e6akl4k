package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// probeJSONTTL bounds how long a probe dump may back a download; the format URLs
// inside grow stale with YouTube-issued signatures, so older hits are ignored.
const probeJSONTTL = 5 * time.Minute

// probeJSONCache remembers the full dump of a single-track probe so the download
// can start from --load-info instead of a second network resolve.
type probeJSONCache struct {
	mu      sync.Mutex
	entries map[string]probeJSONEntry
}

type probeJSONEntry struct {
	raw []byte
	at  time.Time
}

// probeCacheSize caps the cache; entries rarely exceed a few hundred kilobytes.
const probeCacheSize = 64

func newProbeJSONCache() *probeJSONCache {
	return &probeJSONCache{entries: make(map[string]probeJSONEntry, probeCacheSize)}
}

func (c *probeJSONCache) store(url string, raw []byte) {
	if c == nil || strings.TrimSpace(url) == "" || len(raw) == 0 || len(raw) > 4<<20 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= probeCacheSize {
		var oldestKey string
		oldest := time.Now().Add(time.Hour)
		for key, entry := range c.entries {
			if entry.at.Before(oldest) {
				oldest, oldestKey = entry.at, key
			}
		}
		if oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}
	c.entries[strings.TrimSpace(url)] = probeJSONEntry{raw: raw, at: time.Now()}
}

// get returns a fresh dump of the track, or nil when it is absent or expired.
func (c *probeJSONCache) get(url string) []byte {
	if c == nil {
		return nil
	}
	key := strings.TrimSpace(url)
	if key == "" {
		return nil
	}
	c.mu.Lock()
	entry, ok := c.entries[key]
	if ok && time.Since(entry.at) > probeJSONTTL {
		delete(c.entries, key)
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return nil
	}
	return entry.raw
}

// freshProbeJSON returns the cached dump of a single track for the download fast path.
func (d *downloader) freshProbeJSON(url string) []byte {
	return d.probeCache.get(url)
}

// maybeCacheProbeJSON stores the dump of a single-video probe. Flat-playlist listings
// and search dumps stay untracked: only a self-contained video dump can feed --load-info.
func (d *downloader) maybeCacheProbeJSON(url string, info *mediaInfo, raw []byte) {
	if info == nil || info.Type == "playlist" || len(info.Entries) > 0 {
		return
	}
	if info.ID == "" {
		return
	}
	d.probeCache.store(url, bytes.TrimSpace(raw))
}

// runProbeJSONDownload reuses the probe dump: yt-dlp resolves nothing, downloads the
// picked format straight from the URLs the dump already carries. Any error falls back
// to the normal download path in the caller.
func (d *downloader) runProbeJSONDownload(ctx context.Context, raw []byte, url string, argsFor func(items []int, progressFile string) []string, sessionDir, manifest string, selected []int, playlist bool, progress downloadProgress, total int) (string, error) {
	if playlist || len(selected) > 1 {
		return "", errors.New("probe json has sense only for single tracks")
	}
	path := filepath.Join(sessionDir, "probe-info.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(path) }()
	// Hold the VK gate for VK singles even though the args carry no URL anymore.
	release, err := d.acquireVKMusic(ctx, []string{"--", url})
	if err != nil {
		return "", err
	}
	defer release()
	progressFile := filepath.Join(sessionDir, "progress.log")
	args := argsFor(selected, progressFile)
	tail := len(args) - 2
	if tail < 0 || args[tail] != "--" || args[tail+1] != url {
		return "", errors.New("probe json download: unexpected args tail")
	}
	args = append(args[:tail], "--load-info", path)
	_, stderr, err := d.runWithProgressOnce(ctx, args, progressFile, progress, total)
	return stderr, err
}

// probeJSONDownloaded reports whether the fast pass actually produced a track.
func probeJSONDownloaded(manifest string) bool {
	downloaded, err := readManifest(manifest)
	return err == nil && len(downloaded) > 0
}

// sourceAlreadyMp3 reports a source that yields ready mp3 files, where re-encoding
// the same audio would only burn CPU. VK singles are the known case.
func sourceAlreadyMp3(url, format string) bool {
	_, ok := parseVKMusicURL(url)
	if !ok || format != "mp3" {
		return false
	}
	if strings.Contains(url, "/audio_playlist") || strings.Contains(url, "/music/album/") {
		return false
	}
	return true
}

// regularFilePaths lists the regular files of a session directory for cleanup.
func regularFilePaths(root string) []string {
	var paths []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	return paths
}
