package main

import (
	"context"
	"sync"
)

type preparedTrack struct {
	results []downloadResult
	release func()
	err     error
}

// downloadConcurrent admits the request once, then gives each track its own
// global slot, with a disk reservation for the album. File ownership passes to the caller only
// after all workers have stopped. Duplicate aliases share a session in this batch.
func (a *app) downloadConcurrent(ctx context.Context, items []pendingURL, format, quality string, queued func(int)) ([]downloadResult, func(), error) {
	return a.downloadConcurrentWithReservation(ctx, items, format, quality, queued, true)
}

func (a *app) downloadConcurrentWithReservation(ctx context.Context, items []pendingURL, format, quality string, queued func(int), reserve bool) ([]downloadResult, func(), error) {
	if len(items) == 0 {
		return nil, func() {}, nil
	}
	unique := make([]pendingURL, 0, len(items))
	positions := make([]int, len(items))
	keys := make(map[string]int)
	for i, item := range items {
		key := downloadIdentity(item.URL, format, quality)
		position, ok := keys[key]
		if !ok {
			position = len(unique)
			keys[key] = position
			unique = append(unique, item)
		}
		positions[i] = position
	}
	prepared := make([]preparedTrack, len(unique))
	var reservation int64
	for _, item := range unique {
		seconds := item.Preview.DurationSeconds
		if seconds <= 0 {
			seconds = averageTrackSeconds
		}
		if seconds <= a.downloader.maxDurationFor(format, quality) {
			reservation += diskBatchFactor * estimateAudioSize(seconds, format, quality)
		}
	}
	// Reserve the whole album before starting. Per-track waits while retaining
	// finished files could otherwise wait on this request's own reservation.
	initialDisk := func() {}
	var err error
	if reserve {
		initialDisk, err = a.disk.acquire(ctx, reservation, nil)
		if err != nil {
			return nil, func() {}, err
		}
	}
	jobs := make(chan int, len(unique))
	for i := range unique {
		jobs <- i
	}
	close(jobs)
	var admission trackAdmission
	run := func(i int) {
		item := unique[i]
		prepared[i].results, prepared[i].err = a.prepareSource(ctx, item, format, quality, func(ctx context.Context) ([]downloadResult, error) {
			slot, err := admission.acquire(ctx, a, queued, format, quality)
			if err != nil {
				return nil, err
			}
			defer slot()
			return a.downloader.downloadRangeWithPreview(ctx, item.URL, format, quality, 0, 0, item.Preview, nil)
		})
	}
	var workers sync.WaitGroup
	for range min(a.cfg.BatchDownloadWorkers, a.cfg.DownloadWorkers, len(unique)) {
		workers.Go(func() {
			for i := range jobs {
				run(i)
			}
		})
	}
	workers.Wait()
	var once sync.Once
	release := func() {
		once.Do(func() {
			initialDisk()
		})
	}
	if ctx.Err() != nil {
		for _, track := range prepared {
			a.downloader.clearSessions(track.results)
		}
		release()
		return nil, func() {}, ctx.Err()
	}
	if admission.err != nil {
		for _, track := range prepared {
			a.downloader.clearSessions(track.results)
		}
		release()
		return nil, func() {}, admission.err
	}
	results := make([]downloadResult, 0, len(items))
	for i, position := range positions {
		track := prepared[position]
		if track.err != nil || len(track.results) == 0 {
			a.downloader.clearSessions(track.results)
			reason := tr("unknown_error", defaultLang)
			if track.err != nil {
				reason = track.err.Error()
			}
			results = append(results, downloadResult{Title: items[i].Preview.Title, Artist: items[i].Preview.Artist, URL: items[i].URL, Error: reason})
			continue
		}
		for _, result := range track.results {
			if result.URL == "" {
				result.URL = items[i].URL
			}
			results = append(results, result)
		}
	}
	return results, release, nil
}
