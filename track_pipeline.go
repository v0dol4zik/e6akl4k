package main

import (
	"context"
	"sync"
	"time"
)

// orderedTracks keeps at most width tracks in preparation or awaiting delivery.
// A slow upload applies backpressure before another source process is started.
// consume owns each result until it returns; cleanup also runs on cancellation.
func (a *app) orderedTracks(ctx context.Context, count, width int, prepare func(context.Context, int) preparedTrack, consume func(int, preparedTrack) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if count == 0 {
		return nil
	}
	width = min(max(width, 1), count)
	jobs := make(chan int, width)
	ready := make([]chan preparedTrack, count)
	for i := range ready {
		ready[i] = make(chan preparedTrack, 1)
	}
	for i := range width {
		jobs <- i
	}
	var workers sync.WaitGroup
	for range width {
		workers.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case i, ok := <-jobs:
					if !ok {
						return
					}
					if ctx.Err() != nil {
						return
					}
					ready[i] <- prepare(ctx, i)
				}
			}
		})
	}
	cleanup := func(track preparedTrack) {
		a.downloader.clearSessions(track.results)
		if track.release != nil {
			track.release()
		}
	}
	defer func() {
		cancel()
		close(jobs)
		workers.Wait()
		for _, ch := range ready {
			select {
			case track := <-ch:
				cleanup(track)
			default:
			}
		}
	}()
	for i := range count {
		var track preparedTrack
		select {
		case <-ctx.Done():
			return ctx.Err()
		case track = <-ready[i]:
		}
		err := consume(i, track)
		cleanup(track)
		if err != nil {
			return err
		}
		if next := i + width; next < count {
			jobs <- next
		}
	}
	return ctx.Err()
}

// A request enters the normal queue once. Subsequent tracks yield their slots
// and join the bounded continuation queue, even when the new-job queue is zero.
type trackAdmission struct {
	once    sync.Once
	mu      sync.Mutex
	initial func()
	err     error
}

func (b *trackAdmission) acquire(ctx context.Context, a *app, queued func(int), format, quality string) (func(), error) {
	started := time.Now()
	b.once.Do(func() { _, b.initial, b.err = a.downloads.acquireNotify(ctx, queued) })
	b.mu.Lock()
	slot, err := b.initial, b.err
	b.initial = nil
	b.mu.Unlock()
	if err == nil && slot == nil {
		_, slot, err = a.downloads.acquireContinuation(ctx, queued)
	}
	logMediaStageContext(ctx, "download_queue", "youtube", started, 0, err == nil, "format", format, "quality", quality)
	return slot, err
}

func (a *app) streamBatch(ctx context.Context, chatID int64, items []pendingURL, format, quality, lang string, reporter *statusReporter) (deliveryReport, error) {
	// Reserve for the two largest tracks before launching either. Reserving
	// finished files independently can otherwise block the earlier ordered track.
	var largest, second int64
	for _, item := range items {
		seconds := item.Preview.DurationSeconds
		if seconds <= 0 {
			seconds = averageTrackSeconds
		}
		size := estimateAudioSize(seconds, format, quality)
		if size > largest {
			second, largest = largest, size
		} else if size > second {
			second = size
		}
	}
	release, err := a.disk.acquire(ctx, diskBatchFactor*(largest+second), nil)
	if err != nil {
		return deliveryReport{}, err
	}
	defer release()
	var admission trackAdmission
	report := deliveryReport{}
	err = a.orderedTracks(ctx, len(items), min(a.cfg.BatchDownloadWorkers, a.cfg.DownloadWorkers), func(ctx context.Context, i int) preparedTrack {
		item := items[i]
		if cached, ok := a.cachedResult(ctx, item, format, quality); ok {
			return preparedTrack{results: []downloadResult{cached}}
		}
		results, err := a.prepareSource(ctx, item, format, quality, func(ctx context.Context) ([]downloadResult, error) {
			slot, err := admission.acquire(ctx, a, nil, format, quality)
			if err != nil {
				return nil, err
			}
			defer slot()
			return a.downloader.downloadRangeWithPreview(ctx, item.URL, format, quality, 0, 0, item.Preview, nil)
		})
		return preparedTrack{results: results, err: err}
	}, func(i int, track preparedTrack) error {
		if track.err != nil {
			return track.err
		}
		// Delivery makes its own cleanup call; the outer one is idempotent.
		for j := range track.results {
			track.results[j].Position, track.results[j].Total = i+1, len(items)
		}
		report.add(a.sendResultsIndividuallyWithSummary(chatID, track.results, format, lang, false, reporter))
		return ctx.Err()
	})
	return report, err
}
