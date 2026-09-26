package main

import (
	"context"
	"errors"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// maxExportTracks caps one exported tracklist. Export reads only metadata, so the cap is
	// independent of MAX_PLAYLIST_TRACKS.
	maxExportTracks = 1000
	// exportTimeout bounds /export and /cover, including the wait for a lookup slot.
	exportTimeout = 60 * time.Second
)

var (
	errExportUnsupported = errors.New("export unsupported")
	errExportEmpty       = errors.New("export empty")
	// videoTitleTag matches bracketed upload tags such as "(Official Video)" or "[Lyrics]".
	videoTitleTag = regexp.MustCompile(`(?i)\s*[(\[][^()\[\]]*(\bofficial\b|\bvideo\b|\baudio\b|\blyrics?\b|\bvisuali[sz]er\b|\bhd\b|\bhq\b|\b4k\b|\bmv\b|клип|премьера|видео|аудио)[^()\[\]]*[)\]]`)
)

// exportTrack is one "Artist - Title" line of an exported tracklist. An empty value marks an
// unavailable playlist entry, so that range indices keep matching the source order.
type exportTrack struct {
	Artist string
	Title  string
}

func (t exportTrack) line() string {
	artist, title := strings.TrimSpace(t.Artist), strings.TrimSpace(t.Title)
	switch {
	case artist != "" && title != "":
		return artist + " - " + title
	case title != "":
		return title
	default:
		return artist
	}
}

// exportResult is a tracklist ready to be rendered. Name titles the caption and file names,
// Total is the source size when the list was capped, and CoverURL is a direct image from a
// music API (empty for yt-dlp sources, whose covers come from thumbnails).
type exportResult struct {
	Name     string
	Tracks   []exportTrack
	Playlist bool
	Total    int
	CoverURL string
}

func (r exportResult) lines() []string {
	lines := make([]string, 0, len(r.Tracks))
	for _, track := range r.Tracks {
		if line := track.line(); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// trackFromInfo builds a line from yt-dlp metadata. Music metadata (track and artist) is used
// as is; a plain upload title goes through uploadTrack with the channel as a fallback artist.
func trackFromInfo(info *mediaInfo) exportTrack {
	if info == nil {
		return exportTrack{}
	}
	uploader := firstNonEmpty(info.Artist, channelArtist(firstNonEmpty(info.Channel, info.Uploader)))
	if track := strings.TrimSpace(info.Track); track != "" {
		return exportTrack{Artist: uploader, Title: track}
	}
	return uploadTrack(uploader, info.Title)
}

// uploadTrack normalizes a raw upload title: "(Official Video)"-style tags are dropped and an
// embedded "Artist - Title" wins over the uploader name.
func uploadTrack(uploader, title string) exportTrack {
	title = strings.TrimSpace(title)
	if stripped := strings.TrimSpace(videoTitleTag.ReplaceAllString(title, "")); stripped != "" {
		title = stripped
	}
	if artist, rest, ok := splitArtistTitle(title); ok {
		return exportTrack{Artist: artist, Title: rest}
	}
	return exportTrack{Artist: channelArtist(uploader), Title: title}
}

// splitArtistTitle cuts a title at its first spaced hyphen or dash.
func splitArtistTitle(title string) (string, string, bool) {
	cut, width := -1, 0
	for _, dash := range []string{" - ", " – ", " — "} {
		if index := strings.Index(title, dash); index >= 0 && (cut < 0 || index < cut) {
			cut, width = index, len(dash)
		}
	}
	if cut < 0 {
		return "", "", false
	}
	artist, rest := strings.TrimSpace(title[:cut]), strings.TrimSpace(title[cut+width:])
	return artist, rest, artist != "" && rest != ""
}

// channelArtist turns a YouTube "Artist - Topic" auto-generated channel into the artist name.
func channelArtist(channel string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(channel), " - Topic"))
}

// playlistName drops the "Album - " prefix of YouTube Music albums and names the artist instead.
func playlistName(title, uploader string) string {
	title = strings.TrimSpace(title)
	if album, ok := strings.CutPrefix(title, "Album - "); ok {
		if artist := channelArtist(uploader); artist != "" {
			return artist + " - " + album
		}
		return album
	}
	return title
}

// exportTracksFromInfo keeps one entry per playlist position (capped at maxExportTracks), so
// that a chosen range can be sliced by index.
func exportTracksFromInfo(info *mediaInfo, playlist bool) []exportTrack {
	if !playlist {
		return []exportTrack{trackFromInfo(info)}
	}
	entries := info.Entries[:min(len(info.Entries), maxExportTracks)]
	tracks := make([]exportTrack, 0, len(entries))
	for _, entry := range entries {
		tracks = append(tracks, trackFromInfo(entry))
	}
	return tracks
}

func exportFromInfo(info *mediaInfo) exportResult {
	playlist := info.Type == "playlist" || len(info.Entries) > 0
	result := exportResult{Tracks: exportTracksFromInfo(info, playlist), Playlist: playlist}
	if playlist {
		result.Name = playlistName(info.Title, firstNonEmpty(info.Uploader, info.Channel))
		if count := max(info.PlaylistCount, len(info.Entries)); count > len(result.Tracks) {
			result.Total = count
		}
	} else {
		result.Name = result.Tracks[0].line()
	}
	return result
}

// previewTracks returns the stored lines of a preview. Search results carry no yt-dlp entry,
// so their single line is rebuilt from the shown title and artist.
func previewTracks(preview mediaPreview) []exportTrack {
	if len(preview.Tracks) > 0 || preview.IsPlaylist {
		return preview.Tracks
	}
	return []exportTrack{uploadTrack(preview.Artist, preview.Title)}
}

// pendingExport renders a stored preview without touching the network: a chosen playlist
// range is honoured and a multi-link message becomes one combined list.
func pendingExport(pending pendingURL) exportResult {
	if len(pending.Batch) > 0 {
		result := exportResult{Playlist: true}
		for _, preview := range pending.BatchPreviews {
			result.Tracks = append(result.Tracks, previewTracks(preview)...)
		}
		return result
	}
	preview := pending.Preview
	tracks := previewTracks(preview)
	result := exportResult{Tracks: tracks, Playlist: preview.IsPlaylist}
	if !preview.IsPlaylist {
		if len(tracks) > 0 {
			result.Name = tracks[0].line()
		}
		return result
	}
	result.Name = playlistName(preview.Title, preview.Artist)
	if start := pending.RangeStart; start > 0 && start <= len(tracks) {
		result.Tracks = tracks[start-1 : min(max(pending.RangeEnd, start), len(tracks))]
	} else if preview.TrackCount > len(tracks) {
		result.Total = preview.TrackCount
	}
	return result
}

// exportTracklist resolves a link for /export: Deezer and Yandex Music through their public
// APIs, single tracks of other streaming services through the link metadata, and everything
// else through a flat yt-dlp probe.
func (a *app) exportTracklist(ctx context.Context, rawURL string) (exportResult, error) {
	if result, handled, err := musicServiceTracklist(ctx, rawURL, true); handled {
		return result, err
	}
	info, stderr, err := a.downloader.probe(ctx, rawURL, "--flat-playlist", "--playlist-end", strconv.Itoa(maxExportTracks))
	if err != nil {
		if ctx.Err() != nil {
			return exportResult{}, ctx.Err()
		}
		return exportResult{}, errors.New(humanizeError(firstNonEmpty(stderr, err.Error())))
	}
	return exportFromInfo(info), nil
}

// sendExport posts a single track as a copyable line and a playlist as a .txt file.
func (a *app) sendExport(chatID int64, result exportResult, lang string) error {
	lines := result.lines()
	if len(lines) == 0 {
		return errExportEmpty
	}
	if !result.Playlist && len(lines) == 1 {
		if a.sendText(chatID, tr("export_single", lang, "line", html.EscapeString(lines[0])), "HTML", nil) == nil {
			return errors.New("не удалось отправить треклист")
		}
		return nil
	}
	count := strconv.Itoa(len(lines))
	if result.Total > len(lines) {
		count = tr("export_count_capped", lang, "count", count, "total", strconv.Itoa(result.Total))
	}
	name := firstNonEmpty(result.Name, tr("export_untitled", lang))
	document := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{
		Name:  exportFileName(result.Name, "tracklist") + ".txt",
		Bytes: []byte(strings.Join(lines, "\n") + "\n"),
	})
	document.Caption = tr("export_caption", lang, "name", html.EscapeString(shortenRunes(name, 200)), "count", count)
	document.ParseMode = "HTML"
	_, err := sendTelegram(a.bot, document)
	return err
}

func exportFileName(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return sanitizeArchiveName(name)
}

func callbackChatID(callback *tgbotapi.CallbackQuery) int64 {
	if callback.Message != nil && callback.Message.Chat != nil {
		return callback.Message.Chat.ID
	}
	return callback.From.ID
}

// allowUserRequest applies the per-user rate limit to export, cover and last.fm requests.
func (a *app) allowUserRequest(chatID, userID int64, lang string) bool {
	allowed, retry := a.limiter.allow(userID)
	if !allowed {
		if a.store != nil {
			a.store.increment(a.ctx, "rate_limited")
		}
		a.sendText(chatID, tr("rate_limited", lang, "seconds", strconv.Itoa(int(retry.Seconds())+1)), "", nil)
	}
	return allowed
}

// handleExportCallback exports the tracklist of a shown preview; the stored link stays
// available for downloading.
func (a *app) handleExportCallback(callback *tgbotapi.CallbackQuery) {
	userID, chatID := callback.From.ID, callbackChatID(callback)
	lang := a.langOrDefault(userID)
	if !a.allowUserRequest(chatID, userID, lang) {
		return
	}
	pending, ok := a.getURL(strings.TrimPrefix(callback.Data, "export:"), userID, chatID)
	if !ok {
		a.sendText(userID, tr("action_unavailable", lang), "", nil)
		return
	}
	if err := a.sendExport(chatID, pendingExport(pending), lang); err != nil {
		a.exportFailed(chatID, userID, pending.URL, "export", lang, nil, err)
		return
	}
	if a.store != nil {
		a.store.increment(a.ctx, "exports")
	}
}

// handleCoverCallback sends the cover of every link of a shown preview.
func (a *app) handleCoverCallback(callback *tgbotapi.CallbackQuery) {
	userID, chatID := callback.From.ID, callbackChatID(callback)
	lang := a.langOrDefault(userID)
	if !a.allowUserRequest(chatID, userID, lang) {
		return
	}
	pending, ok := a.getURL(strings.TrimPrefix(callback.Data, "cover:"), userID, chatID)
	if !ok {
		a.sendText(userID, tr("action_unavailable", lang), "", nil)
		return
	}
	links := pending.Batch
	if len(links) == 0 && pending.URL != "" {
		links = []string{pending.URL}
	}
	if len(links) == 0 {
		// last.fm lists are stored without a link and have no cover.
		a.sendText(userID, tr("action_unavailable", lang), "", nil)
		return
	}
	for _, link := range links {
		a.runExportJob(chatID, userID, link, lang, true)
	}
}

// handleExportCommand serves /export and /cover. The link comes from the arguments or from
// the message the command replies to.
func (a *app) handleExportCommand(message *tgbotapi.Message, lang string, cover bool) {
	rawURL := detectURL(message.CommandArguments())
	if rawURL == "" && message.ReplyToMessage != nil {
		rawURL = detectURL(firstNonEmpty(message.ReplyToMessage.Text, message.ReplyToMessage.Caption))
	}
	if rawURL == "" {
		key := "export_usage"
		if cover {
			key = "cover_usage"
		}
		a.sendText(message.Chat.ID, tr(key, lang), "HTML", nil)
		return
	}
	if !a.allowUserRequest(message.Chat.ID, message.From.ID, lang) {
		return
	}
	a.runExportJob(message.Chat.ID, message.From.ID, rawURL, lang, cover)
}

// runExportJob fetches a tracklist or a cover inside a lookup slot and reports the outcome in
// place of a status message.
func (a *app) runExportJob(chatID, userID int64, rawURL, lang string, cover bool) {
	stage, working := "export", "export_working"
	if cover {
		stage, working = "cover", "cover_working"
	}
	status := a.sendText(chatID, tr(working, lang), "HTML", nil)
	ctx, cancel := context.WithTimeout(a.ctx, exportTimeout)
	defer cancel()
	_, release, err := a.lookups.acquire(ctx)
	if err != nil {
		if errors.Is(err, errQueueFull) && a.store != nil {
			a.store.increment(a.ctx, "queue_rejected")
		}
		a.deleteStatusMessage(status)
		a.handleQueueError(chatID, lang, err, errorReport{Stage: stage, UserID: userID, URL: rawURL})
		return
	}
	if cover {
		var image coverImage
		if image, err = a.coverFor(ctx, rawURL); err == nil {
			err = a.sendCover(chatID, image, lang)
		}
	} else {
		var result exportResult
		if result, err = a.exportTracklist(ctx, rawURL); err == nil {
			err = a.sendExport(chatID, result, lang)
		}
	}
	release()
	if err != nil {
		a.exportFailed(chatID, userID, rawURL, stage, lang, status, err)
		return
	}
	a.deleteStatusMessage(status)
	if a.store != nil {
		a.store.increment(a.ctx, stage+"s")
	}
}

// exportFailed explains a failed export or cover. Unsupported links and missing covers are the
// user's case, not a bug, so only other failures reach the operator chat.
func (a *app) exportFailed(chatID, userID int64, rawURL, stage, lang string, status *tgbotapi.Message, err error) {
	var text string
	switch {
	case errors.Is(err, errExportUnsupported):
		text = tr(stage+"_unsupported", lang)
	case errors.Is(err, errExportEmpty):
		text = tr("export_empty", lang)
	case errors.Is(err, errCoverNotFound):
		text = tr("cover_not_found", lang)
	case errors.Is(err, context.DeadlineExceeded):
		text = tr("export_timeout", lang)
	default:
		a.reportError(errorReport{Stage: stage, ChatID: chatID, UserID: userID, URL: rawURL, Error: err.Error()})
		text = tr(stage+"_error", lang, "error", html.EscapeString(err.Error()))
	}
	a.replaceStatusText(chatID, status, text)
}
