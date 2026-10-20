package main

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A watch URL that also selects a playlist must retain playlist semantics.
func singleYouTubeID(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || parsed.Host != parsed.Hostname() || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Query().Get("list") != "" {
		return ""
	}
	id, playlist := youtubeLinkTarget(rawURL)
	if playlist {
		return ""
	}
	return id
}

func downloadIdentity(rawURL, format, quality string) string {
	if id := singleYouTubeID(rawURL); id != "" {
		return sourceCacheKey("youtube", id, format, quality)
	}
	return generalCacheKey(rawURL, format, quality)
}

// No stream URLs or cookies cross process boundaries on this path. yt-dlp
// obtains metadata and applies the duration/live filter before downloading.
func (a *app) directYouTubePreview(rawURL, format, quality string) (mediaPreview, bool) {
	id := singleYouTubeID(rawURL)
	if !a.cfg.YouTubeSingleExtract || id == "" || !validDownloadOption(format, quality) {
		return mediaPreview{}, false
	}
	return mediaPreview{URL: rawURL, Title: "YouTube", TrackCount: 1, SourceID: id, Extractor: "youtube", direct: true}, true
}

func (a *app) cachedYouTubePreview(ctx context.Context, rawURL, format, quality string) (mediaPreview, bool) {
	id := singleYouTubeID(rawURL)
	if id == "" || a.store == nil {
		return mediaPreview{}, false
	}
	options := [][2]string{{format, quality}}
	if !validDownloadOption(format, quality) {
		options = [][2]string{{"mp3", "best"}, {"mp3", "320"}, {"mp3", "128"}, {"m4a", "best"}, {"flac", "best"}, {"ogg", "best"}}
	}
	for _, option := range options {
		entry, ok := a.store.cachedAudio(ctx, sourceCacheKey("youtube", id, option[0], option[1]), a.cfg.CacheTTL)
		if !ok {
			continue
		}
		return mediaPreview{URL: rawURL, Title: entry.Title, Artist: entry.Artist, Duration: entry.Duration, DurationSeconds: inlineDurationSeconds(entry.Duration), TrackCount: 1, SourceID: id, Extractor: "youtube", Thumbnail: youtubeThumbnail(id)}, true
	}
	return mediaPreview{}, false
}

type batchInspection struct {
	preview mediaPreview
	err     error
}

func (a *app) inspectBatchLinks(ctx context.Context, urls []string, format, quality string) []batchInspection {
	results := make([]batchInspection, len(urls))
	jobs := make(chan int, len(urls))
	for i := range urls {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(2, len(urls)) {
		workers.Go(func() {
			for i := range jobs {
				rawURL := urls[i]
				if requiresMusicResolution(rawURL) {
					continue
				}
				if preview, ok := a.cachedYouTubePreview(ctx, rawURL, format, quality); ok {
					results[i].preview = preview
					continue
				}
				if preview, ok := a.directYouTubePreview(rawURL, format, quality); ok {
					results[i].preview = preview
					continue
				}
				func() {
					linkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
					defer cancel()
					started := time.Now()
					_, release, err := a.lookups.acquire(linkCtx)
					logMediaStageContext(ctx, "lookup_queue", sourceHost(rawURL), started, 0, err == nil)
					if err != nil {
						results[i].err = err
						return
					}
					defer release()
					results[i].preview, results[i].err = a.inspectURL(linkCtx, rawURL)
				}()
			}
		})
	}
	workers.Wait()
	return results
}

func youtubeSourceName(rawURL string) string {
	if singleYouTubeID(rawURL) != "" {
		return "youtube"
	}
	return strings.ToLower(sourceHost(rawURL))
}

func (a *app) cachedResult(ctx context.Context, item pendingURL, format, quality string) (downloadResult, bool) {
	if a.store == nil {
		return downloadResult{}, false
	}
	keys := []string{downloadIdentity(item.URL, format, quality), sourceCacheKey(item.Preview.Extractor, item.Preview.SourceID, format, quality), generalCacheKey(item.URL, format, quality)}
	for _, key := range keys {
		if entry, ok := a.store.cachedAudio(ctx, key, a.cfg.CacheTTL); ok && entry.Size <= a.fileLimit() {
			return downloadResult{Cached: &entry, CacheKey: key, Quality: quality, URL: item.URL, Title: entry.Title, Artist: entry.Artist, Duration: entry.Duration, DurationSeconds: inlineDurationSeconds(entry.Duration), Size: entry.Size}, true
		}
	}
	return downloadResult{}, false
}
