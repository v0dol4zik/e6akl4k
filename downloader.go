package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
)

const (
	defaultMBPerMinute = 2.40
	// sizeEstimateMargin keeps a suggested lighter format safely below the Telegram limit.
	sizeEstimateMargin = 0.92
	// sizePrecheckSlack skips a variable-bitrate track before download only when its estimate
	// exceeds the limit by a wide margin: lossless and VBR sizes vary a lot with the material, and
	// the real file size is checked again before upload. Constant-bitrate MP3 is estimated exactly.
	sizePrecheckSlack = 1.25
	downloadTimeout   = 2 * time.Hour
	// ytdlpPluginDir holds the bgutil PO token plugin in the Docker image. It is not one of
	// yt-dlp's default plugin folders, so the plugin loads only with a PO token provider set.
	ytdlpPluginDir = "/opt/yt-dlp-plugins"
)

var mbPerMinute = map[string]float64{
	"mp3:128":  0.96,
	"mp3:320":  2.40,
	"mp3:best": 1.91,
	"m4a:best": 3.10,
	"ogg:best": 1.13,
	// 16-bit FLAC (see audioFormatArgs); ffmpeg's default 24-bit output averaged 12.68 MiB/min.
	"flac:best": 7.20,
}

var audioExtensions = map[string]bool{
	".mp3": true, ".flac": true, ".m4a": true, ".ogg": true,
	".opus": true, ".wav": true, ".aac": true,
}

type downloadResult struct {
	FilePath string
	Title    string
	Artist   string
	Duration string
	Error    string
	Session  string
	CacheKey string
	// URL is the track's own page, used to offer a lighter format when the file does not fit.
	URL             string
	DurationSeconds int
	// TooLarge marks a track whose file or estimate (Size) exceeds the Telegram upload limit.
	TooLarge bool
	Size     int64
}

type mediaInfo struct {
	Type           string  `json:"_type"`
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Track          string  `json:"track"`
	Artist         string  `json:"artist"`
	Uploader       string  `json:"uploader"`
	Channel        string  `json:"channel"`
	Duration       float64 `json:"duration"`
	DurationString string  `json:"duration_string"`
	FilePath       string  `json:"filepath"`
	WebpageURL     string  `json:"webpage_url"`
	URL            string  `json:"url"`
	Thumbnail      string  `json:"thumbnail"`
	PlaylistIndex  int     `json:"playlist_index"`
	PlaylistCount  int     `json:"playlist_count"`
	Extractor      string  `json:"extractor"`
	ExtractorKey   string  `json:"extractor_key"`
	// IEKey names the extractor of a --flat-playlist entry, which has no extractor fields.
	IEKey   string       `json:"ie_key"`
	Entries []*mediaInfo `json:"entries"`
}

type mediaPreview struct {
	URL             string
	Title           string
	Artist          string
	Duration        string
	DurationSeconds int
	TrackCount      int
	IsPlaylist      bool
	Estimated128    int64
	Estimated320    int64
	SourceID        string
	Extractor       string
	Thumbnail       string
	// Tracks holds the "Artist - Title" export lines, one per playlist position.
	Tracks []exportTrack
}

// errNothingFound means a search returned no results: the query's outcome, not a bot failure.
var errNothingFound = errors.New("ничего не найдено")

// searchRelaxRetries caps how many trailing words a query that finds nothing may lose.
const searchRelaxRetries = 2

// searchLookup searches YouTube through yt-dlp and ranks the results. A query that finds nothing
// is retried without its last word, which is often a typo or a tag no video title has; the results
// are ranked against the query that found them.
func (d *downloader) searchLookup(ctx context.Context, query string, expectedDuration int) ([]inlineCandidate, error) {
	candidates, err := d.youtubeInlineLookup(ctx, query)
	for retry := 0; errors.Is(err, errNothingFound) && retry < searchRelaxRetries; retry++ {
		shorter, ok := dropLastSearchWord(query)
		if !ok {
			break
		}
		query = shorter
		candidates, err = d.youtubeInlineLookup(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	return rankCandidates(query, expectedDuration, candidates), nil
}

// dropLastSearchWord removes the last word of a query together with the dashes and other
// punctuation-only fields around it. ok is false when fewer than two words would remain.
func dropLastSearchWord(query string) (string, bool) {
	fields := strings.Fields(query)
	trimPunctuation := func() {
		for len(fields) > 0 && !hasLetterOrDigit(fields[len(fields)-1]) {
			fields = fields[:len(fields)-1]
		}
	}
	trimPunctuation()
	if len(fields) == 0 {
		return "", false
	}
	fields = fields[:len(fields)-1]
	trimPunctuation()
	words := 0
	for _, field := range fields {
		if hasLetterOrDigit(field) {
			words++
		}
	}
	if words < 2 {
		return "", false
	}
	return strings.Join(fields, " "), true
}

func hasLetterOrDigit(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

func (d *downloader) youtubeInlineLookup(ctx context.Context, query string) ([]inlineCandidate, error) {
	args := append(d.commonArgs(), "--flat-playlist", "--dump-single-json", "--simulate", "--playlist-end", strconv.Itoa(inlineResultLimit), "--", "ytsearch"+strconv.Itoa(inlineResultLimit)+":"+query)
	stdout, stderr, err := d.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("%s", humanizeError(firstNonEmpty(stderr, errorText(err))))
	}
	if len(bytes.TrimSpace(stdout)) == 0 || bytes.Equal(bytes.TrimSpace(stdout), []byte("null")) {
		return nil, errNothingFound
	}
	var info mediaInfo
	if err := json.Unmarshal(stdout, &info); err != nil {
		return nil, fmt.Errorf("разобрать результаты inline-поиска: %w", err)
	}
	candidates := make([]inlineCandidate, 0, inlineResultLimit)
	for _, entry := range info.Entries {
		if entry == nil || entry.ID == "" {
			continue
		}
		url := firstNonEmpty(entry.WebpageURL, entry.URL)
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			url = "https://www.youtube.com/watch?v=" + entry.ID
		}
		title, artist, duration := entry.resultMetadata()
		candidates = append(candidates, inlineCandidate{
			URL: url, CacheKey: inlineCacheKey(url, "youtube", entry.ID), Title: title, Artist: artist,
			Duration: duration, SourceID: entry.ID, Extractor: "youtube", Thumbnail: youtubeThumbnail(entry.ID),
		})
		if len(candidates) == inlineResultLimit {
			break
		}
	}
	if len(candidates) == 0 {
		return nil, errNothingFound
	}
	return candidates, nil
}

func (m *mediaInfo) resultMetadata() (string, string, string) {
	title := firstNonEmpty(m.Title, m.Track, "Unknown")
	artist := firstNonEmpty(m.Artist, m.Uploader, m.Channel)
	duration := m.DurationString
	if duration == "" {
		duration = secondsToHMS(int(m.Duration))
	}
	return title, artist, duration
}

// pageURL returns the entry's own page. A fully probed entry's url is the media stream itself, so it is
// used only for flat url entries.
func (m *mediaInfo) pageURL() string {
	if m.WebpageURL != "" {
		return m.WebpageURL
	}
	if m.Type == "url" || m.Type == "url_transparent" {
		return m.URL
	}
	return ""
}

type downloader struct {
	bin                string
	ffmpegBin          string
	downloadDir        string
	cookiesFile        string
	maxFileSize        int64
	cookieSnapshotMu   sync.RWMutex
	cookieSnapshot     []byte
	maxPlaylistTracks  int
	ytdlpSleepRequests int
	ytdlpFragments     int
	// youtubeClients and youtubeCookieClients pin the player clients of the anonymous and the
	// signed-in YouTube download; empty keeps yt-dlp's defaults.
	youtubeClients       string
	youtubeCookieClients string
	// potProviderURL is the bgutil PO token server that yt-dlp asks for YouTube PO tokens, and
	// pluginDir the folder with its yt-dlp plugin; both are empty without a provider.
	potProviderURL string
	pluginDir      string
	// youtubeDefaultFirst remembers that yt-dlp's default clients, not youtubeClients, got the
	// last anonymous YouTube download through, so the next one starts with them.
	youtubeDefaultFirst atomic.Bool
	// onCookieRetry, when set, is called every time a YouTube 403 with cookies is retried without them.
	onCookieRetry func()
	// newgroundsHTTPClient is optional; browser checks otherwise use the resolver transport.
	newgroundsHTTPClient *http.Client
}

type downloadProgress func(completed, total int)

type playlistTooLargeError struct {
	Count int
	Limit int
}

func (e playlistTooLargeError) Error() string {
	return fmt.Sprintf("плейлист содержит %d треков, лимит — %d", e.Count, e.Limit)
}

func newDownloader(downloadDir string, maxFileSize int64) (*downloader, error) {
	bin, err := exec.LookPath("yt-dlp")
	if err != nil {
		return nil, errors.New("yt-dlp не найден в PATH")
	}
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("ffmpeg не найден в PATH")
	}
	absoluteDir, err := filepath.Abs(downloadDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absoluteDir, 0o700); err != nil {
		return nil, fmt.Errorf("создать каталог загрузок: %w", err)
	}
	if err := checkWritableDir(absoluteDir); err != nil {
		return nil, fmt.Errorf("каталог загрузок недоступен для записи: %w", err)
	}
	clearAbandonedSessions(absoluteDir)
	if cacheDir := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			return nil, fmt.Errorf("создать каталог кэша: %w", err)
		}
		if err := checkWritableDir(cacheDir); err != nil {
			return nil, fmt.Errorf("каталог кэша недоступен для записи: %w", err)
		}
	}

	cookiesFile := strings.TrimSpace(os.Getenv("YTDLP_COOKIES_FILE"))
	cookiesExplicit := cookiesFile != ""
	if cookiesFile == "" {
		cookiesFile = filepath.Join(filepath.Dir(absoluteDir), "cookies.txt")
	}
	if info, err := os.Stat(cookiesFile); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("проверить cookies %s: %w", cookiesFile, err)
		}
		if cookiesExplicit {
			return nil, fmt.Errorf("YTDLP_COOKIES_FILE=%s указан, но файл не найден", cookiesFile)
		}
		cookiesFile = ""
		log.Print("cookies.txt не найден: YouTube на серверном IP может потребовать вход")
	} else {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("cookies %s не является обычным файлом", cookiesFile)
		}
		file, err := os.Open(cookiesFile)
		if err != nil {
			return nil, fmt.Errorf("cookies %s недоступен для чтения: %w", cookiesFile, err)
		}
		_ = file.Close()
		cookiesFile, _ = filepath.Abs(cookiesFile)
		log.Printf("Использую cookies из %s", cookiesFile)
	}
	d := &downloader{
		bin:               bin,
		ffmpegBin:         ffmpegBin,
		downloadDir:       absoluteDir,
		cookiesFile:       cookiesFile,
		maxFileSize:       maxFileSize,
		maxPlaylistTracks: maxPlaylistTracks,
	}
	if cookiesFile != "" {
		if err := d.refreshCookieSnapshot(); err != nil {
			return nil, fmt.Errorf("прочитать cookies: %w", err)
		}
	}
	return d, nil
}

func (d *downloader) preview(ctx context.Context, url string) (mediaPreview, error) {
	// A full probe of a playlist opens every video and solves its JS challenge, which takes longer
	// than the preview may wait; the flat listing has the titles and durations. A single video is
	// probed in full either way.
	info, stderr, err := d.probe(ctx, url, "--flat-playlist")
	if err != nil {
		if ctx.Err() != nil {
			return mediaPreview{}, ctx.Err()
		}
		return mediaPreview{}, errors.New(humanizeError(firstNonEmpty(stderr, err.Error())))
	}
	preview := mediaPreview{URL: url}
	preview.SourceID = info.ID
	preview.Extractor = firstNonEmpty(info.Extractor, info.ExtractorKey)
	preview.Thumbnail = info.Thumbnail
	preview.Title, preview.Artist, preview.Duration = info.resultMetadata()
	preview.DurationSeconds = int(info.Duration)
	preview.IsPlaylist = info.Type == "playlist" || len(info.Entries) > 0
	if preview.IsPlaylist {
		preview.TrackCount = len(info.Entries)
		preview.DurationSeconds = 0
		for _, entry := range info.Entries {
			if entry != nil {
				preview.DurationSeconds += int(entry.Duration)
			}
		}
		preview.Duration = secondsToHMS(preview.DurationSeconds)
	} else {
		preview.TrackCount = 1
	}
	preview.Tracks = exportTracksFromInfo(info, preview.IsPlaylist)
	preview.Estimated128 = estimateAudioSize(preview.DurationSeconds, "mp3", "128")
	preview.Estimated320 = estimateAudioSize(preview.DurationSeconds, "mp3", "320")
	return preview, nil
}

func estimateAudioSize(seconds int, format, quality string) int64 {
	if seconds <= 0 {
		return 0
	}
	return int64(float64(seconds) / 60 * audioRate(format, quality) * 1024 * 1024)
}

// audioRate returns the typical output size in MiB per minute for a format and quality.
func audioRate(format, quality string) float64 {
	rate, ok := mbPerMinute[strings.ToLower(format)+":"+strings.ToLower(quality)]
	if !ok {
		rate, ok = mbPerMinute[strings.ToLower(format)+":best"]
	}
	if !ok {
		rate = defaultMBPerMinute
	}
	return rate
}

// maxDurationFor returns the longest track the pre-download size check lets through.
func (d *downloader) maxDurationFor(format, quality string) int {
	limitMB := float64(d.maxFileSize) / (1024 * 1024)
	if !constantBitrate(format, quality) {
		limitMB *= sizePrecheckSlack
	}
	return int(limitMB / audioRate(format, quality) * 60)
}

func constantBitrate(format, quality string) bool {
	return strings.EqualFold(format, "mp3") && (quality == "128" || quality == "320")
}

func (d *downloader) download(ctx context.Context, url, format, quality string, progress downloadProgress) ([]downloadResult, error) {
	return d.downloadRange(ctx, url, format, quality, 0, 0, progress)
}

func (d *downloader) downloadRange(ctx context.Context, url, format, quality string, rangeStart, rangeEnd int, progress downloadProgress) ([]downloadResult, error) {
	session, err := randomID()
	if err != nil {
		return nil, err
	}
	sessionDir := filepath.Join(d.downloadDir, session)
	if err := os.Mkdir(sessionDir, 0o700); err != nil {
		return nil, fmt.Errorf("создать каталог сессии: %w", err)
	}
	keepSession := false
	defer func() {
		if !keepSession {
			_ = os.RemoveAll(sessionDir)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	// Each batch of a playlist is downloaded by position, so the flat listing is enough to select
	// it; the metadata of downloaded tracks is then taken from the yt-dlp manifest.
	info, probeErrors, err := d.probe(ctx, url, "--flat-playlist")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []downloadResult{{Error: humanizeError(firstNonEmpty(probeErrors, err.Error())), Session: session}}, nil
	}

	isPlaylist := info.Type == "playlist" || len(info.Entries) > 0
	entries := info.Entries
	if !isPlaylist {
		entries = []*mediaInfo{info}
	}
	if len(entries) == 0 {
		return []downloadResult{{Error: humanizeError(firstNonEmpty(probeErrors, "Плейлист пуст или недоступен.")), Session: session}}, nil
	}
	start, end := 1, len(entries)
	if isPlaylist && rangeStart > 0 {
		start = rangeStart
	}
	if isPlaylist && rangeEnd > 0 {
		end = rangeEnd
	}
	if start < 1 {
		start = 1
	}
	if end > len(entries) {
		end = len(entries)
	}
	if start > end {
		return nil, errors.New("выбран пустой диапазон плейлиста")
	}
	selectionCount := end - start + 1
	limit := d.maxPlaylistTracks
	if limit <= 0 {
		limit = maxPlaylistTracks
	}
	if selectionCount > limit {
		return nil, playlistTooLargeError{Count: selectionCount, Limit: limit}
	}
	if err := validatePlaylistSizeWithLimit(selectionCount, limit); err != nil {
		return nil, err
	}

	maxDuration := d.maxDurationFor(format, quality)
	results := make([]downloadResult, selectionCount)
	selected := make([]int, 0, selectionCount)
	for original := start; original <= end; original++ {
		i := original - start
		entry := entries[original-1]
		if entry == nil {
			results[i] = downloadResult{Error: "Трек пропущен: недоступен или заблокирован (см. логи)", Session: session}
			continue
		}
		title, artist, duration := entry.resultMetadata()
		results[i] = downloadResult{Title: title, Artist: artist, Duration: duration, Session: session, CacheKey: sourceCacheKey(firstNonEmpty(entry.Extractor, entry.ExtractorKey, entry.IEKey), entry.ID, format, quality), URL: entry.pageURL(), DurationSeconds: int(entry.Duration)}
		if entry.Duration > 0 && int(entry.Duration) > maxDuration {
			estimate := estimateAudioSize(int(entry.Duration), format, quality)
			results[i].TooLarge, results[i].Size = true, estimate
			results[i].Error = fmt.Sprintf("«%s» — %s: в %s это около %s, больше лимита Telegram %s.", title, secondsToHMS(int(entry.Duration)), strings.ToUpper(format), humanSize(estimate, defaultLang), humanSize(d.maxFileSize, defaultLang))
			continue
		}
		selected = append(selected, original)
	}

	if len(selected) == 0 {
		return results, nil
	}

	manifest := filepath.Join(sessionDir, "manifest.jsonl")
	downloadErrors, runErr := d.runDownload(ctx, url, format, quality, sessionDir, manifest, selected, isPlaylist, progress, len(selected))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	downloaded, parseErr := readManifest(manifest)
	if parseErr != nil {
		return nil, fmt.Errorf("прочитать манифест yt-dlp: %w", parseErr)
	}

	used := make([]bool, len(downloaded))
	for _, position := range selected {
		entry := entries[position-1]
		resultIndex := position - start
		match := -1
		for j := range downloaded {
			if used[j] {
				continue
			}
			if isPlaylist && downloaded[j].PlaylistIndex == position {
				match = j
				break
			}
			if downloaded[j].ID != "" && downloaded[j].ID == entry.ID {
				match = j
				break
			}
		}
		if match < 0 {
			reason := firstNonEmpty(downloadErrors, errorText(runErr), "Файл не найден: "+results[resultIndex].Title)
			results[resultIndex].Error = humanizeError(reason)
			continue
		}
		used[match] = true
		path, valid := d.validAudioPath(sessionDir, downloaded[match].FilePath)
		if !valid {
			results[resultIndex].Error = "Файл не найден: " + results[resultIndex].Title
			continue
		}
		results[resultIndex].FilePath = path
		results[resultIndex].applyManifest(downloaded[match], format, quality)
	}

	for _, result := range results {
		if result.FilePath != "" {
			keepSession = true
			break
		}
	}
	return results, nil
}

// applyManifest replaces the metadata of a flat playlist entry, which may lack the artist, the
// duration or the extractor, with that of the downloaded track.
func (r *downloadResult) applyManifest(entry mediaInfo, format, quality string) {
	if entry.Title != "" || entry.Track != "" {
		r.Title, r.Artist, r.Duration = entry.resultMetadata()
	}
	if entry.Duration > 0 {
		r.DurationSeconds = int(entry.Duration)
	}
	if extractor := firstNonEmpty(entry.Extractor, entry.ExtractorKey); extractor != "" {
		if key := sourceCacheKey(extractor, entry.ID, format, quality); key != "" {
			r.CacheKey = key
		}
	}
	if link := entry.pageURL(); link != "" {
		r.URL = link
	}
}

func validatePlaylistSize(count int) error {
	return validatePlaylistSizeWithLimit(count, maxPlaylistTracks)
}

func validatePlaylistSizeWithLimit(count, limit int) error {
	if count > limit {
		return playlistTooLargeError{Count: count, Limit: limit}
	}
	return nil
}

// probe dumps the metadata of a link; extra arguments (such as --flat-playlist) go before it.
func (d *downloader) probe(ctx context.Context, url string, extra ...string) (*mediaInfo, string, error) {
	started := time.Now()
	args := append(d.commonArgs(), extra...)
	args = append(args, "--dump-single-json", "--simulate", "--ignore-errors", "--", url)
	stdout, stderr, err := d.run(ctx, args...)
	defer func() { logMediaStage("source_probe", sourceHost(url), started, 0, err == nil) }()
	if bytes.Equal(bytes.TrimSpace(stdout), []byte("null")) {
		if err == nil {
			err = errors.New("yt-dlp не вернул информацию")
		}
		return nil, stderr, err
	}
	var info mediaInfo
	if jsonErr := json.Unmarshal(stdout, &info); jsonErr != nil {
		if err == nil {
			err = jsonErr
		}
		return nil, stderr, err
	}
	return &info, stderr, nil
}

func (d *downloader) runDownload(ctx context.Context, url, format, quality, sessionDir, manifest string, selected []int, playlist bool, progress downloadProgress, total int) (string, error) {
	started := time.Now()
	template := "after_move:%(.{id,title,track,artist,uploader,channel,duration,duration_string,filepath,ext,playlist_index,extractor,extractor_key,webpage_url})j"
	// argsFor downloads the given playlist items and logs each start to progressFile.
	argsFor := func(items []int, progressFile string) []string {
		args := append(d.commonArgs(),
			"--no-simulate",
			"--no-abort-on-error",
			"--sleep-interval", "0",
			"--max-sleep-interval", "3",
			"--paths", sessionDir,
			"--output", "%(autonumber)06d_%(id)s.%(ext)s",
			"--extract-audio",
			"--embed-metadata",
			"--embed-thumbnail",
			"--convert-thumbnails", "jpg",
			"--force-overwrites",
			"--print-to-file", template, manifest,
			"--print-to-file", "before_dl:%(playlist_index)s", progressFile,
		)
		args = append(args, audioFormatArgs(format, quality)...)
		if playlist {
			positions := make([]string, len(items))
			for i, position := range items {
				positions[i] = strconv.Itoa(position)
			}
			args = append(args, "--playlist-items", strings.Join(positions, ","))
		}
		return append(args, "--", url)
	}
	var stderr string
	var err error
	if argsHaveYouTubeTarget([]string{"--", url}) {
		stderr, err = d.downloadYouTube(ctx, argsFor, sessionDir, manifest, selected, playlist, progress, total)
	} else {
		progressFile := filepath.Join(sessionDir, "progress.log")
		_, stderr, err = d.runWithProgress(ctx, argsFor(selected, progressFile), progressFile, progress, total)
	}
	logMediaStage("source_download", sourceHost(url), started, regularFilesSize(sessionDir), err == nil, "format", format, "quality", quality)
	return stderr, err
}

func (d *downloader) runWithProgress(ctx context.Context, args []string, manifest string, progress downloadProgress, total int) ([]byte, string, error) {
	args, clearExtractor, err := d.newgroundsExtractorArgs(args)
	if err != nil {
		return nil, "", err
	}
	defer clearExtractor()
	cookies, cleanup, err := d.isolatedCookieFile()
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	firstArgs := args
	if cookies != "" {
		firstArgs = argsBeforeSeparator(args, "--cookies", cookies)
	}
	stdout, stderr, err := d.runWithProgressOnce(ctx, firstArgs, manifest, progress, total)
	if err != nil && ctx.Err() == nil && newgroundsTarget(args) != "" && isForbiddenFailure(stderr) {
		retryArgs, clearGuardCookies, guardErr := d.newgroundsGuardArgs(ctx, args, cookies)
		if guardErr == nil {
			defer clearGuardCookies()
			return d.runWithProgressOnce(ctx, retryArgs, manifest, progress, total)
		}
		log.Printf("Newgrounds browser check failed: %v", guardErr)
	}
	return stdout, stderr, err
}

// downloadYouTube downloads without cookies first. Without a PO token provider, YouTube answers
// media requests with 403 for most player clients of a signed-in session, and a server IP it has
// flagged gets 429 and bot checks on the web clients. Which clients still work changes from hour
// to hour, so the anonymous passes alternate between youtubeClients and yt-dlp's default clients,
// starting with the set that worked last, and the signed-in pass pins youtubeCookieClients. Only
// the items that failed are downloaded again, so a playlist never starts over: once more without
// cookies and with the other clients after a 403, a bot check, or no audio from the first ones,
// and then with cookies if YouTube asked to sign in (a bot check, an age gate, a members-only
// video).
func (d *downloader) downloadYouTube(ctx context.Context, argsFor func(items []int, progressFile string) []string, sessionDir, manifest string, selected []int, playlist bool, progress downloadProgress, total int) (string, error) {
	progressFile := filepath.Join(sessionDir, "progress.log")
	clients := [2]string{firstNonEmpty(d.youtubeClients, "default"), "default"}
	defaultFirst := d.youtubeDefaultFirst.Load()
	if defaultFirst {
		clients[0], clients[1] = clients[1], clients[0]
	}
	_, stderr, err := d.runWithProgressOnce(ctx, argsBeforeSeparator(argsFor(selected, progressFile), youtubeClientArgs(clients[0])...), progressFile, progress, total)
	for pass, withCookies := range []bool{false, true} {
		if err == nil || ctx.Err() != nil {
			break
		}
		retry := youtubeClientFailure(stderr)
		if withCookies {
			retry = d.cookiesFile != "" && youtubeSignInRequired(stderr)
		}
		if !retry {
			continue
		}
		missing := missingItems(manifest, selected, playlist)
		if len(missing) == 0 {
			break
		}
		// A fresh progress log counts the retried items on top of the finished ones.
		progressFile = filepath.Join(sessionDir, fmt.Sprintf("progress-retry%d.log", pass+1))
		args := argsFor(missing, progressFile)
		var retryProgress downloadProgress
		if progress != nil {
			finished := len(selected) - len(missing)
			retryProgress = func(completed, _ int) { progress(finished+completed, total) }
		}
		if withCookies {
			cookies, cleanup, cookieErr := d.isolatedCookieFile()
			if cookieErr != nil {
				cleanup()
				break
			}
			log.Printf("YouTube просит вход, повторяю загрузку %d из %d с cookies", len(missing), len(selected))
			logMediaStage("youtube_signin_retry", "youtube", time.Now(), 0, true, "items", strconv.Itoa(len(missing)))
			_, stderr, err = d.runWithProgressOnce(ctx, argsBeforeSeparator(args, append([]string{"--cookies", cookies}, youtubeClientArgs(d.youtubeCookieClients)...)...), progressFile, retryProgress, total)
			cleanup()
			continue
		}
		log.Printf("YouTube не отдал аудио без cookies, повторяю загрузку %d из %d с клиентами %s", len(missing), len(selected), clients[1])
		logMediaStage("youtube_forbidden_retry", "youtube", time.Now(), 0, true, "items", strconv.Itoa(len(missing)), "clients", clients[1])
		_, stderr, err = d.runWithProgressOnce(ctx, argsBeforeSeparator(args, youtubeClientArgs(clients[1])...), progressFile, retryProgress, total)
		if err == nil && clients[0] != clients[1] {
			d.youtubeDefaultFirst.Store(!defaultFirst)
		}
	}
	return stderr, err
}

// youtubeClientArgs pins the YouTube player clients of one yt-dlp run.
func youtubeClientArgs(clients string) []string {
	if clients == "" || clients == "default" {
		return nil
	}
	return []string{"--extractor-args", "youtube:player_client=" + clients}
}

// youtubeClientFailure reports whether a YouTube download failed in a way other player clients
// may avoid: a 403 on the media URL, no audio format from the chosen clients, a client whose
// player page YouTube refuses to serve, or a bot check that only some clients get.
func youtubeClientFailure(stderr string) bool {
	low := strings.ToLower(stderr)
	for _, marker := range []string{"requested format is not available", "page needs to be reloaded", "not a bot"} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return isForbiddenFailure(low)
}

// missingItems returns the selected items that the manifest does not list as downloaded yet. An
// unreadable manifest yields none: the caller fails on it anyway, and a retry must not overwrite
// tracks that may already be downloaded.
func missingItems(manifest string, selected []int, playlist bool) []int {
	downloaded, err := readManifest(manifest)
	if err != nil {
		return nil
	}
	if !playlist {
		if len(downloaded) > 0 {
			return nil
		}
		return selected
	}
	done := make(map[int]bool, len(downloaded))
	for _, entry := range downloaded {
		done[entry.PlaylistIndex] = true
	}
	var missing []int
	for _, item := range selected {
		if !done[item] {
			missing = append(missing, item)
		}
	}
	return missing
}

// youtubeSignInRequired reports whether yt-dlp failed because YouTube serves the video only to a
// signed-in session; yt-dlp suggests --cookies in all of these errors.
func youtubeSignInRequired(stderr string) bool {
	low := strings.ToLower(stderr)
	for _, marker := range []string{"sign in", "--cookies", "members-only", "join this channel", "private video", "age-restricted", "inappropriate for some users"} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

func (d *downloader) runWithProgressOnce(ctx context.Context, args []string, manifest string, progress downloadProgress, total int) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, d.bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	var stdout bytes.Buffer
	stderr := &limitedBuffer{limit: 256 * 1024}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	lastCompleted := 0
	stopProgress := make(chan struct{})
	var progressDone sync.WaitGroup
	if progress != nil && total > 1 {
		progressDone.Add(1)
		go func() {
			defer progressDone.Done()
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					completed := progressLineCount(manifest)
					if completed > lastCompleted {
						lastCompleted = completed
						progress(completed, total)
					}
				case <-stopProgress:
					return
				}
			}
		}()
	}
	err := cmd.Run()
	close(stopProgress)
	progressDone.Wait()
	if completed := progressLineCount(manifest); completed > lastCompleted && progress != nil && total > 1 {
		progress(completed, total)
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		log.Printf("yt-dlp завершился с ошибкой: %v: %s", err, firstLine(stderr.String()))
	}
	return stdout.Bytes(), stderr.String(), err
}

func (d *downloader) commonArgs() []string {
	fragments := d.ytdlpFragments
	if fragments <= 0 {
		fragments = 4
	}
	args := []string{
		"--ignore-config",
		"--color", "never",
		"--no-progress",
		"--format", "bestaudio[vcodec=none]/bestaudio/best",
		"--retries", "3",
		"--fragment-retries", "3",
		"--extractor-retries", "3",
		"--concurrent-fragments", strconv.Itoa(fragments),
		"--geo-bypass",
		"--remote-components", "ejs:github",
		"--extractor-args", "vk:force_mobile=1",
		"--add-headers", "Accept-Language:en-US,en;q=0.9",
	}
	if d.ytdlpSleepRequests > 0 {
		args = append(args, "--sleep-requests", strconv.Itoa(d.ytdlpSleepRequests))
	}
	if d.pluginDir != "" {
		args = append(args, "--plugin-dirs", d.pluginDir)
	}
	if d.potProviderURL != "" {
		args = append(args, "--extractor-args", "youtubepot-bgutilhttp:base_url="+d.potProviderURL)
	}
	return args
}

func regularFilesSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// cookieForbiddenRetry reports whether a yt-dlp failure looks like the YouTube 403 that appears when
// a cookie session is bound to SABR-only streaming, and whether retrying without cookies is worth it.
func cookieForbiddenRetry(args []string, stderr string, err error, ctx context.Context) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if !argsHaveYouTubeTarget(args) {
		return false
	}
	low := strings.ToLower(stderr)
	return strings.Contains(low, "http error 403") || strings.Contains(low, "403: forbidden")
}

// argsHaveYouTubeTarget reports whether the yt-dlp target after "--" is a YouTube URL or ytsearch query.
func argsHaveYouTubeTarget(args []string) bool {
	for i, arg := range args {
		if arg != "--" || i+1 >= len(args) {
			continue
		}
		target := strings.ToLower(args[i+1])
		if strings.HasPrefix(target, "ytsearch") {
			return true
		}
		host := sourceHost(target)
		return host == "youtu.be" || host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")
	}
	return false
}

func (d *downloader) run(ctx context.Context, args ...string) ([]byte, string, error) {
	args, clearExtractor, err := d.newgroundsExtractorArgs(args)
	if err != nil {
		return nil, "", err
	}
	defer clearExtractor()
	cookies, cleanup, err := d.isolatedCookieFile()
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	firstArgs := args
	if cookies != "" {
		firstArgs = argsBeforeSeparator(args, "--cookies", cookies)
	}
	stdout, stderr, err := d.runOnce(ctx, firstArgs)
	if err != nil && ctx.Err() == nil && newgroundsTarget(args) != "" && isForbiddenFailure(stderr) {
		retryArgs, clearGuardCookies, guardErr := d.newgroundsGuardArgs(ctx, args, cookies)
		if guardErr == nil {
			defer clearGuardCookies()
			return d.runOnce(ctx, retryArgs)
		}
		log.Printf("Newgrounds browser check failed: %v", guardErr)
	}
	if cookies == "" || !cookieForbiddenRetry(args, stderr, err, ctx) {
		return stdout, stderr, err
	}
	log.Printf("yt-dlp вернул 403 с cookies, повторяю без cookies")
	logMediaStage("cookie_forbidden_retry", "youtube", time.Now(), 0, true)
	if d.onCookieRetry != nil {
		d.onCookieRetry()
	}
	return d.runOnce(ctx, args)
}

func (d *downloader) runOnce(ctx context.Context, args []string) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, d.bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	var stdout bytes.Buffer
	stderr := &limitedBuffer{limit: 256 * 1024}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		log.Printf("yt-dlp завершился с ошибкой: %v: %s", err, firstLine(stderr.String()))
	}
	return stdout.Bytes(), stderr.String(), err
}

func (d *downloader) isolatedCookieFile() (string, func(), error) {
	if d.cookiesFile == "" {
		return "", func() {}, nil
	}
	d.cookieSnapshotMu.RLock()
	snapshot := append([]byte(nil), d.cookieSnapshot...)
	d.cookieSnapshotMu.RUnlock()
	file, err := os.CreateTemp(d.downloadDir, ".cookies-readonly-*.txt")
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := file.Write(snapshot); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func (d *downloader) refreshCookieSnapshot() error {
	if d.cookiesFile == "" {
		return nil
	}
	data, err := os.ReadFile(d.cookiesFile)
	if err != nil {
		return err
	}
	d.cookieSnapshotMu.Lock()
	d.cookieSnapshot = data
	d.cookieSnapshotMu.Unlock()
	return nil
}

func argsBeforeSeparator(args []string, values ...string) []string {
	for i, arg := range args {
		if arg == "--" {
			result := make([]string, 0, len(args)+len(values))
			result = append(result, args[:i]...)
			result = append(result, values...)
			return append(result, args[i:]...)
		}
	}
	return append(args, values...)
}

func checkWritableDir(path string) error {
	file, err := os.CreateTemp(path, ".write-test-*")
	if err != nil {
		return err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func clearAbandonedSessions(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("Не удалось проверить старые сессии: %v", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || (len(entry.Name()) != 8 && len(entry.Name()) != 16) {
			continue
		}
		if _, err := hex.DecodeString(entry.Name()); err != nil {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			log.Printf("Не удалось удалить старую сессию %s: %v", entry.Name(), err)
		}
	}
}

func (d *downloader) validAudioPath(sessionDir, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(sessionDir, path)
	}
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(sessionDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || !audioExtensions[strings.ToLower(filepath.Ext(path))] {
		return "", false
	}
	return path, true
}

func (d *downloader) clearSession(session string) {
	if len(session) != 8 && len(session) != 16 {
		return
	}
	if _, err := hex.DecodeString(session); err != nil {
		return
	}
	if err := os.RemoveAll(filepath.Join(d.downloadDir, session)); err != nil {
		log.Printf("Не удалось удалить файлы сессии %s: %v", session, err)
	}
}

// clearSessions removes the session directories of every result; a playlist of searched
// tracks has one session per track.
func (d *downloader) clearSessions(results []downloadResult) {
	cleared := make(map[string]bool)
	for _, result := range results {
		if result.Session != "" && !cleared[result.Session] {
			cleared[result.Session] = true
			d.clearSession(result.Session)
		}
	}
}

func readManifest(path string) ([]mediaInfo, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []mediaInfo
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		var entry mediaInfo
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

func manifestLineCount(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	count := 0
	for scanner.Scan() {
		var entry mediaInfo
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			count++
		}
	}
	return count
}

func progressLineCount(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count
}

func audioFormatArgs(format, quality string) []string {
	switch strings.ToLower(format) {
	case "flac":
		// The sources are lossy (YouTube Opus), so 16 bits lose nothing; ffmpeg would otherwise
		// write 24-bit FLAC that is about 40% larger.
		return []string{"--audio-format", "flac", "--postprocessor-args", "ExtractAudio:-sample_fmt s16"}
	case "m4a":
		return []string{"--audio-format", "m4a", "--audio-quality", "0"}
	case "ogg":
		return []string{"--audio-format", "vorbis", "--audio-quality", "5"}
	default:
		audioQuality := "0"
		if quality != "best" {
			audioQuality = quality + "K"
		}
		return []string{"--audio-format", "mp3", "--audio-quality", audioQuality}
	}
}

func humanizeError(message string) string {
	message = strings.TrimSpace(message)
	low := strings.ToLower(message)
	switch {
	case strings.Contains(low, "sign in to confirm you") || strings.Contains(low, "not a bot"):
		return "YouTube требует подтверждения, что запрос не от бота. Нужен свежий cookies.txt из браузера, где выполнен вход в аккаунт."
	case strings.Contains(low, "cookies are no longer valid"):
		return "Cookies YouTube устарели (аккаунт обновил их в браузере). Нужен свежий cookies.txt из браузера, где выполнен вход в аккаунт."
	case strings.Contains(low, "private video"):
		return "Видео приватное."
	case strings.Contains(low, "requested format is not available") || strings.Contains(low, "only images are available"):
		return "YouTube не отдал ни одной аудиодорожки. Проверь deno и remote component ejs:github."
	case strings.Contains(low, "n challenge") || strings.Contains(low, "javascript runtime"):
		return "Не удалось решить JS-задачу YouTube: не найден deno или другой JS-рантайм."
	case strings.Contains(low, "video unavailable") || strings.Contains(low, "is not available"):
		return "Видео недоступно (удалено или заблокировано)."
	case strings.Contains(low, "age") && (strings.Contains(low, "confirm") || strings.Contains(low, "restricted")):
		return "Видео с возрастным ограничением: нужны cookies залогиненного аккаунта."
	case strings.Contains(low, "not available in your country") || strings.Contains(low, "geo") && strings.Contains(low, "block"):
		return "Видео заблокировано в этом регионе."
	case strings.Contains(low, "unsupported url") || strings.Contains(low, "is not a valid url"):
		return "Ссылка не поддерживается."
	case strings.Contains(low, "ffmpeg"):
		return "Не установлен ffmpeg: без него не получится сконвертировать аудио."
	}
	if line := bestErrorLine(message); line != "" {
		return shortenRunes(line, 300)
	}
	return "Неизвестная ошибка yt-dlp."
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	return n, nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

func randomID() (string, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func secondsToHMS(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%d:%02d", minutes, secs)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return value[:index]
	}
	return value
}

func bestErrorLine(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.Contains(strings.ToUpper(line), "ERROR:") {
			return line
		}
	}
	return firstLine(value)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// regularFileSize returns the size of a regular file, or 0 when it is missing or not a regular file.
func regularFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}
