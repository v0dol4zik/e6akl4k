package main

import (
	"context"
	"time"
)

// Mixed cached/local playlist results are sent in source order. A rejected
// Telegram file_id is invalidated and downloaded once through the normal gate.
func (a *app) sendCachedResults(chatID int64, results []downloadResult, format, lang string, summary bool, reporters ...*statusReporter) deliveryReport {
	defer a.downloader.clearSessions(results)
	var reporter *statusReporter
	if len(reporters) > 0 {
		reporter = reporters[0]
	}
	ctx := reporterContext(reporter)
	report := deliveryReport{}
	for i, result := range results {
		// The outer call retains the original sessions until every item is sent.
		result.Session = ""
		if ctx.Err() != nil {
			report.Failed += len(results) - i
			break
		}
		if result.Position == 0 {
			result.Position, result.Total = i+1, len(results)
		}
		if result.Cached != nil {
			started := time.Now()
			err := a.sendCachedAudio(chatID, *result.Cached, lang, result)
			logMediaStageContext(ctx, "telegram_file_id_send", "telegram", started, 0, err == nil, "format", format, "quality", result.Quality, "cache_hit", true)
			if err == nil {
				mediaOperationFromContext(ctx).delivered(ctx)
				a.store.increment(a.ctx, "cache_hits")
				report.Delivered++
				continue
			}
			if invalidCachedFileError(err) {
				a.store.deleteCachedAudio(ctx, result.CacheKey)
				preview, _ := a.directYouTubePreview(result.URL, format, result.Quality)
				fresh, downloadErr := a.runDownloadRangeQueuedWithPreview(ctx, result.URL, format, result.Quality, 0, 0, preview, nil, nil)
				if downloadErr == nil && len(fresh) == 1 {
					fresh[0].Position, fresh[0].Total = result.Position, result.Total
					report.add(a.sendResultsIndividuallyWithSummary(chatID, fresh, format, lang, false, reporters...))
					continue
				}
				a.downloader.clearSessions(fresh)
				if downloadErr != nil {
					err = downloadErr
				}
			}
			result.Cached, result.Error = nil, err.Error()
		}
		if result.FilePath != "" && result.Error == "" && !result.TooLarge {
			// A playlist can repeat a file or retain several tracks in one
			// session. Individual delivery must not clean a later item's files.
			local, err := a.downloader.cloneResults([]downloadResult{result})
			if err != nil {
				result.Error = err.Error()
			} else {
				result = local[0]
			}
		}
		report.add(a.sendResultsIndividuallyWithSummary(chatID, []downloadResult{result}, format, lang, false, reporters...))
	}
	if summary {
		a.sendPlaylistSummary(chatID, report, lang)
	}
	return report
}

// only local files may enter an archive, so ZIP jobs omit file_id lookups.
func (a *app) cachedPlaylistResult(ctx context.Context, pending pendingURL, item pendingURL, format, quality string) (downloadResult, bool) {
	if pending.Delivery == "zip" {
		return downloadResult{}, false
	}
	return a.cachedResult(ctx, item, format, quality)
}
