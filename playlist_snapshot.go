package main

import (
	"context"
	"strings"
)

type playlistTrack struct {
	ID, Title, Artist string
	Seconds           int
}

// A snapshot fixes the user's selected positions even if YouTube edits the
// playlist later. Missing/private entries retain their position and fail alone.
func youtubePlaylistSnapshot(rawURL string, info *mediaInfo) []playlistTrack {
	host := sourceHost(rawURL)
	if host != "youtu.be" && host != "youtube.com" && !strings.HasSuffix(host, ".youtube.com") || len(info.Entries) > maxExportTracks {
		return nil
	}
	tracks := make([]playlistTrack, len(info.Entries))
	for i, entry := range info.Entries {
		if entry == nil {
			continue
		}
		title, artist, _ := entry.resultMetadata()
		tracks[i] = playlistTrack{Title: title, Artist: artist, Seconds: int(entry.Duration)}
		if singleYouTubeID("https://youtu.be/"+entry.ID) == entry.ID && entry.ID != "" {
			tracks[i].ID = entry.ID
		}
	}
	return tracks
}

func (a *app) downloadSnapshotRange(ctx context.Context, pending pendingURL, format, quality, lang string, start, end int, queued func(int)) ([]downloadResult, error) {
	tracks := pending.Preview.PlaylistSnapshot
	results := make([]downloadResult, end-start+1)
	var items []pendingURL
	var positions []int
	for position := start; position <= end; position++ {
		track := tracks[position-1]
		i := position - start
		if track.ID == "" {
			results[i] = downloadResult{Title: track.Title, Error: tr("playlist_track_unavailable", lang)}
			continue
		}
		rawURL := "https://www.youtube.com/watch?v=" + track.ID
		preview := mediaPreview{URL: rawURL, SourceID: track.ID, Title: track.Title, Artist: track.Artist, DurationSeconds: track.Seconds, Duration: secondsToHMS(track.Seconds), Extractor: "youtube", TrackCount: 1, direct: a.cfg.YouTubeSingleExtract}
		item := pendingURL{URL: rawURL, Preview: preview}
		if cached, ok := a.cachedPlaylistResult(ctx, pending, item, format, quality); ok {
			results[i] = cached
			continue
		}
		items = append(items, item)
		positions = append(positions, i)
	}
	// downloadPlaylistBatches already reserves disk for this part. Nested
	// reservations would double count it and can wait on the request itself.
	downloaded, release, err := a.downloadConcurrentWithReservation(ctx, items, format, quality, queued, false)
	defer release()
	if err != nil {
		return nil, err
	}
	for i, result := range downloaded {
		if i < len(positions) {
			results[positions[i]] = result
		}
	}
	last := pending.RangeEnd
	if last <= 0 {
		last = pending.Preview.TrackCount
	}
	for i := range results {
		results[i].Position, results[i].Total = start+i, last
	}
	return results, nil
}
