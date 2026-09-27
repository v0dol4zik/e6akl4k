package main

import (
	"context"
	"errors"
	"html"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// tracklistExtractor marks a playlist preview whose tracks are known only by artist and title: a
// Deezer or Yandex Music collection or a list pasted as text. Every selected track is searched on
// YouTube and its best match is downloaded.
const tracklistExtractor = "tracklist"

// tracklistSearchTimeout bounds the YouTube search of one tracklist entry.
const tracklistSearchTimeout = 30 * time.Second

func searchedTracklist(preview mediaPreview) bool {
	return preview.IsPlaylist && preview.Extractor == tracklistExtractor
}

// downloadPlaylistRange downloads the tracks start to end of a playlist selection within one
// download slot: a playlist link through one yt-dlp run, a searched tracklist track by track.
func (a *app) downloadPlaylistRange(ctx context.Context, pending pendingURL, format, quality, lang string, start, end int, reporter *statusReporter) ([]downloadResult, error) {
	queued := func(position int) {
		reporter.stage(tr("queued", lang, "position", strconv.Itoa(position)))
	}
	if !searchedTracklist(pending.Preview) {
		return a.runDownloadRangeQueued(ctx, pending.URL, format, quality, start, end, queued, nil)
	}
	_, release, err := a.downloads.acquireNotify(ctx, queued)
	if err != nil {
		if errors.Is(err, errQueueFull) && a.store != nil {
			a.store.increment(a.ctx, "queue_rejected")
		}
		return nil, err
	}
	defer release()
	last := pending.RangeEnd
	if last <= 0 || last > pending.Preview.TrackCount {
		last = pending.Preview.TrackCount
	}
	return a.downloadTracklistRange(ctx, pending.Preview.Tracks, format, quality, lang, start, end, last, reporter)
}

// downloadTracklistRange searches the tracks start to end (1-based) of a tracklist on YouTube one
// by one and downloads the best match of each. A track that is not found or fails to download
// becomes a failed result, so the rest of the list still downloads; last is the final track of
// the whole selection, shown in the progress.
func (a *app) downloadTracklistRange(ctx context.Context, tracks []exportTrack, format, quality, lang string, start, end, last int, reporter *statusReporter) ([]downloadResult, error) {
	results := make([]downloadResult, 0, max(end-start+1, 0))
	for position := start; position <= end; position++ {
		if err := ctx.Err(); err != nil {
			a.downloader.clearSessions(results)
			return nil, err
		}
		var track exportTrack
		if position >= 1 && position <= len(tracks) {
			track = tracks[position-1]
		}
		reporter.stage(tr("tracklist_progress", lang, "current", strconv.Itoa(position), "total", strconv.Itoa(last), "title", html.EscapeString(shortenRunes(track.line(), 80))))
		results = append(results, a.downloadTracklistTrack(ctx, track, format, quality, lang))
		if err := ctx.Err(); err != nil {
			a.downloader.clearSessions(results)
			return nil, err
		}
	}
	return results, nil
}

// downloadTracklistTrack downloads the best YouTube match of one tracklist entry. Failures are
// returned as a result naming the entry, since a YouTube title alone would not tell the user
// which line of the list was lost.
func (a *app) downloadTracklistTrack(ctx context.Context, track exportTrack, format, quality, lang string) downloadResult {
	line := track.line()
	failed := downloadResult{Title: track.Title, Artist: track.Artist, DurationSeconds: track.Seconds}
	query := strings.TrimSpace(strings.TrimSpace(track.Artist) + " " + strings.TrimSpace(track.Title))
	if query == "" {
		failed.Error = tr("unknown_error", lang)
		return failed
	}
	searchCtx, cancel := context.WithTimeout(ctx, tracklistSearchTimeout)
	candidates, err := a.downloader.searchLookup(searchCtx, query, track.Seconds)
	cancel()
	if err == nil && len(candidates) == 0 {
		err = errNothingFound
	}
	if err != nil {
		if errors.Is(err, errNothingFound) {
			failed.Error = tr("tracklist_not_found", lang, "track", line)
		} else {
			failed.Error = tr("tracklist_search_failed", lang, "track", line, "error", err.Error())
		}
		return failed
	}
	candidate := candidates[0]
	failed.URL = candidate.URL
	results, err := a.downloader.downloadRange(ctx, candidate.URL, format, quality, 0, 0, nil)
	if err != nil || len(results) == 0 {
		a.downloader.clearSessions(results)
		if err == nil {
			err = errors.New(tr("unknown_error", lang))
		}
		if ctx.Err() == nil {
			log.Printf("Ошибка загрузки трека из списка source=%s: %v", sourceHost(candidate.URL), err)
			a.reportDownloadFailure(err.Error(), sourceHost(candidate.URL))
		}
		failed.Error = line + ": " + err.Error()
		return failed
	}
	// A single video yields one result; its session is shared with nothing else.
	result := results[0]
	if result.URL == "" {
		// A lighter-format retry re-downloads the matched video itself.
		result.URL = candidate.URL
	}
	if result.Error != "" && !result.TooLarge {
		a.reportDownloadFailure(result.Error, sourceHost(candidate.URL))
		result.Error = line + ": " + result.Error
	}
	return result
}

var (
	tracklistNumbering  = regexp.MustCompile(`^\d{1,4}[.)]\s+`)
	tracklistSeparators = []string{" - ", " – ", " — "}
)

// pastedTracklist reads a message of "Artist - Title" lines, as /export and other tracklist
// exporters write them, optionally numbered. Text that does not look like such a list returns
// nil and is searched as one query: at least two lines, most of them with a dash.
func pastedTracklist(text string) []exportTrack {
	var tracks []exportTrack
	dashed := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(tracklistNumbering.ReplaceAllString(strings.TrimSpace(line), ""))
		if line == "" {
			continue
		}
		track := exportTrack{Title: line}
		cut := -1
		separator := ""
		for _, candidate := range tracklistSeparators {
			if index := strings.Index(line, candidate); index >= 0 && (cut < 0 || index < cut) {
				cut, separator = index, candidate
			}
		}
		if cut >= 0 {
			if artist, title := strings.TrimSpace(line[:cut]), strings.TrimSpace(line[cut+len(separator):]); artist != "" && title != "" {
				track = exportTrack{Artist: artist, Title: title}
				dashed++
			}
		}
		tracks = append(tracks, track)
	}
	if len(tracks) < 2 || dashed < 2 || dashed*2 < len(tracks) {
		return nil
	}
	return tracks[:min(len(tracks), maxExportTracks)]
}

// presentPastedTracklist offers the ranges of a pasted tracklist; each selected track is then
// searched on YouTube and downloaded like a playlist track.
func (a *app) presentPastedTracklist(message *tgbotapi.Message, tracks []exportTrack, lang string) {
	chatID := message.Chat.ID
	preview := mediaPreview{Title: tr("pasted_tracklist_title", lang), TrackCount: len(tracks), IsPlaylist: true, Tracks: tracks, Extractor: tracklistExtractor}
	key, err := a.storeURL(pendingURL{ChatID: chatID, UserID: message.From.ID, Preview: preview})
	if err != nil {
		log.Printf("save pending URL: %v", err)
		a.sendText(chatID, tr("download_error", lang, "error", tr("internal_id_error", lang)), "HTML", nil)
		return
	}
	text := strings.Join([]string{
		"📝 <b>" + html.EscapeString(preview.Title) + "</b>",
		tr("preview_tracks", lang, "count", strconv.Itoa(len(tracks))),
		"",
		tr("pasted_tracklist", lang),
		tr("choose_range", lang),
	}, "\n")
	rows := rangeRows(key, len(tracks), a.cfg.MaxPlaylistTracks, lang)
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr("btn_cancel", lang), "cancel:"+key)))
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	a.sendText(chatID, text, "HTML", &markup)
}
