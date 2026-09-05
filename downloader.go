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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultMBPerMinute = 2.40
	sizeEstimateMargin = 0.92
	downloadTimeout    = 2 * time.Hour
)

var mbPerMinute = map[string]float64{
	"mp3:128":   0.96,
	"mp3:320":   2.40,
	"mp3:best":  1.91,
	"m4a:best":  3.10,
	"ogg:best":  1.13,
	"flac:best": 12.68,
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
}

type mediaInfo struct {
	Type           string       `json:"_type"`
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	Track          string       `json:"track"`
	Artist         string       `json:"artist"`
	Uploader       string       `json:"uploader"`
	Channel        string       `json:"channel"`
	Duration       float64      `json:"duration"`
	DurationString string       `json:"duration_string"`
	FilePath       string       `json:"filepath"`
	WebpageURL     string       `json:"webpage_url"`
	URL            string       `json:"url"`
	Thumbnail      string       `json:"thumbnail"`
	PlaylistIndex  int          `json:"playlist_index"`
	Extractor      string       `json:"extractor"`
	ExtractorKey   string       `json:"extractor_key"`
	Entries        []*mediaInfo `json:"entries"`
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
}

const octaveSearchTimeout = 8 * time.Second

// searchOutcome describes which source produced text-search candidates and why YouTube was used.
type searchOutcome struct {
	Source         string // "octave" or "youtube"
	FallbackReason string // "", "no_results", or "api_error"
}

func (d *downloader) inlineLookup(ctx context.Context, query string) ([]inlineCandidate, error) {
	return d.searchLookup(ctx, query, 0)
}

// searchLookup keeps the YouTube-only behaviour used by inline mode.
func (d *downloader) searchLookup(ctx context.Context, query string, expectedDuration int) ([]inlineCandidate, error) {
	if candidates, handled, err := d.directURLCandidates(ctx, query); handled {
		return candidates, err
	}
	youtube, err := d.youtubeInlineLookup(ctx, query)
	if err != nil {
		return nil, err
	}
	return rankCandidates(query, expectedDuration, youtube), nil
}

// textSearch searches Octave first and falls back to YouTube, reporting the source and the fallback reason.
func (d *downloader) textSearch(ctx context.Context, query string, expectedDuration int) ([]inlineCandidate, searchOutcome, error) {
	if candidates, handled, err := d.directURLCandidates(ctx, query); handled {
		source := "youtube"
		if len(candidates) > 0 && candidates[0].Extractor == "octave" {
			source = "octave"
		}
		return candidates, searchOutcome{Source: source}, err
	}
	outcome := searchOutcome{Source: "youtube"}
	if d.octave != nil {
		octave, err := d.octaveTextLookup(ctx, query)
		switch {
		case err != nil:
			log.Printf("octave search failed, falling back to youtube: %s", redactTraceText(err.Error()))
			outcome.FallbackReason = "api_error"
		case len(octave) > 0:
			return rankCandidates(query, expectedDuration, octave), searchOutcome{Source: "octave"}, nil
		default:
			outcome.FallbackReason = "no_results"
		}
	}
	youtube, err := d.youtubeInlineLookup(ctx, query)
	if err != nil {
		return nil, outcome, err
	}
	return rankCandidates(query, expectedDuration, youtube), outcome, nil
}

// directURLCandidates resolves a pasted link into a single candidate; handled is false for free-text queries.
func (d *downloader) directURLCandidates(ctx context.Context, query string) ([]inlineCandidate, bool, error) {
	directURL := detectURL(query)
	if directURL == "" {
		return nil, false, nil
	}
	if _, ok := parseOctaveURL(directURL); ok && d.octave != nil {
		preview, err := d.previewOctave(ctx, directURL)
		if err != nil {
			return nil, true, err
		}
		if preview.IsPlaylist {
			return nil, true, errors.New("альбомы Octave скачиваются в личном чате с ботом")
		}
		return []inlineCandidate{{
			URL: directURL, CacheKey: sourceCacheKey("octave", preview.SourceID, "mp3", "320"),
			Title: preview.Title, Artist: preview.Artist, Duration: preview.Duration,
			SourceID: preview.SourceID, Extractor: "octave",
		}}, true, nil
	}
	return []inlineCandidate{{
		URL: directURL, CacheKey: inlineCacheKey(directURL, ""), Title: directURL,
	}}, true, nil
}

func (d *downloader) octaveTextLookup(ctx context.Context, query string) ([]inlineCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, octaveSearchTimeout)
	defer cancel()
	tracks, err := d.octave.searchTracks(ctx, query, inlineResultLimit, 0)
	if err != nil {
		return nil, err
	}
	candidates := make([]inlineCandidate, 0, len(tracks))
	for _, track := range tracks {
		if !numericOctaveID(track.ID) {
			continue
		}
		candidates = append(candidates, octaveSearchCandidate(track))
		if len(candidates) == inlineResultLimit {
			break
		}
	}
	return candidates, nil
}

func octaveSearchCandidate(track octaveTrack) inlineCandidate {
	return inlineCandidate{
		URL:       octaveTrackURL(track),
		CacheKey:  sourceCacheKey("octave", track.ID, "mp3", "320"),
		Title:     firstNonEmpty(track.Title, "Unknown"),
		Artist:    track.Artist.Name,
		Duration:  secondsToHMS(track.Duration),
		SourceID:  track.ID,
		Extractor: "octave",
	}
}

func (d *downloader) youtubeInlineLookup(ctx context.Context, query string) ([]inlineCandidate, error) {
	args := append(d.commonArgs(), "--flat-playlist", "--dump-single-json", "--simulate", "--playlist-end", strconv.Itoa(inlineResultLimit), "--", "ytsearch"+strconv.Itoa(inlineResultLimit)+":"+query)
	stdout, stderr, err := d.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("%s", humanizeError(firstNonEmpty(stderr, errorText(err))))
	}
	if len(bytes.TrimSpace(stdout)) == 0 || bytes.Equal(bytes.TrimSpace(stdout), []byte("null")) {
		return nil, errors.New("ничего не найдено")
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
			URL: url, CacheKey: inlineCacheKey(url, entry.ID), Title: title, Artist: artist,
			Duration: duration, SourceID: entry.ID, Extractor: "youtube",
		})
		if len(candidates) == inlineResultLimit {
			break
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("ничего не найдено")
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

type downloader struct {
	bin                string
	ffmpegBin          string
	downloadDir        string
	cookiesFile        string
	maxFileSize        int64
	cookieSnapshotMu   sync.RWMutex
	cookieSnapshot     []byte
	maxPlaylistTracks  int
	octave             *octaveClient
	ytdlpSleepRequests int
	ytdlpFragments     int
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
		octave:            newOctaveClient(),
	}
	if cookiesFile != "" {
		if err := d.refreshCookieSnapshot(); err != nil {
			return nil, fmt.Errorf("прочитать cookies: %w", err)
		}
	}
	return d, nil
}

func (d *downloader) preview(ctx context.Context, url string) (mediaPreview, error) {
	if _, ok := parseOctaveURL(url); ok && d.octave != nil {
		started := time.Now()
		preview, err := d.previewOctave(ctx, url)
		logMediaStage("source_probe", "octave", started, 0, err == nil)
		return preview, err
	}
	info, stderr, err := d.probe(ctx, url)
	if err != nil {
		if ctx.Err() != nil {
			return mediaPreview{}, ctx.Err()
		}
		return mediaPreview{}, errors.New(humanizeError(firstNonEmpty(stderr, err.Error())))
	}
	preview := mediaPreview{URL: url}
	preview.SourceID = info.ID
	preview.Extractor = firstNonEmpty(info.Extractor, info.ExtractorKey)
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
	preview.Estimated128 = estimateAudioSize(preview.DurationSeconds, "mp3", "128")
	preview.Estimated320 = estimateAudioSize(preview.DurationSeconds, "mp3", "320")
	return preview, nil
}

func estimateAudioSize(seconds int, format, quality string) int64 {
	if seconds <= 0 {
		return 0
	}
	rate := mbPerMinute[strings.ToLower(format)+":"+strings.ToLower(quality)]
	if rate == 0 {
		rate = defaultMBPerMinute
	}
	return int64(float64(seconds) / 60 * rate * 1024 * 1024)
}

func (d *downloader) maxDurationFor(format, quality string) int {
	key := strings.ToLower(format) + ":" + strings.ToLower(quality)
	rate, ok := mbPerMinute[key]
	if !ok {
		rate, ok = mbPerMinute[strings.ToLower(format)+":best"]
	}
	if !ok {
		rate = defaultMBPerMinute
	}
	limitMB := float64(d.maxFileSize) / (1024 * 1024) * sizeEstimateMargin
	return int(limitMB / rate * 60)
}

func (d *downloader) download(ctx context.Context, url, format, quality string, progress downloadProgress) ([]downloadResult, error) {
	return d.downloadRange(ctx, url, format, quality, 0, 0, progress)
}

func (d *downloader) downloadRange(ctx context.Context, url, format, quality string, rangeStart, rangeEnd int, progress downloadProgress) ([]downloadResult, error) {
	if _, ok := parseOctaveURL(url); ok && d.octave != nil {
		return d.downloadOctaveRange(ctx, url, format, quality, rangeStart, rangeEnd, progress)
	}
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

	info, probeErrors, err := d.probe(ctx, url)
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
		results[i] = downloadResult{Title: title, Artist: artist, Duration: duration, Session: session, CacheKey: sourceCacheKey(firstNonEmpty(entry.Extractor, entry.ExtractorKey), entry.ID, format, quality)}
		if entry.Duration > 0 && int(entry.Duration) > maxDuration {
			results[i].Error = fmt.Sprintf("«%s» — %s, это дольше %s: файл не влезет в лимит Telegram.", title, secondsToHMS(int(entry.Duration)), secondsToHMS(maxDuration))
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
	}

	for _, result := range results {
		if result.FilePath != "" {
			keepSession = true
			break
		}
	}
	return results, nil
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

func (d *downloader) probe(ctx context.Context, url string) (*mediaInfo, string, error) {
	started := time.Now()
	args := append(d.commonArgs(), "--dump-single-json", "--simulate", "--ignore-errors", "--", url)
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
	template := "after_move:%(.{id,title,track,artist,uploader,channel,duration,duration_string,filepath,ext,playlist_index})j"
	progressFile := filepath.Join(sessionDir, "progress.log")
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
		items := make([]string, len(selected))
		for i, position := range selected {
			items[i] = strconv.Itoa(position)
		}
		args = append(args, "--playlist-items", strings.Join(items, ","))
	}
	args = append(args, "--", url)
	_, stderr, err := d.runWithProgress(ctx, args, progressFile, progress, total)
	logMediaStage("source_download", sourceHost(url), started, regularFilesSize(sessionDir), err == nil, "format", format, "quality", quality)
	return stderr, err
}

func (d *downloader) runWithProgress(ctx context.Context, args []string, manifest string, progress downloadProgress, total int) ([]byte, string, error) {
	cookies, cleanup, err := d.isolatedCookieFile()
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	if cookies != "" {
		args = argsBeforeSeparator(args, "--cookies", cookies)
	}
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
	err = cmd.Run()
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

func (d *downloader) run(ctx context.Context, args ...string) ([]byte, string, error) {
	cookies, cleanup, err := d.isolatedCookieFile()
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	if cookies != "" {
		args = argsBeforeSeparator(args, "--cookies", cookies)
	}
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
	err = cmd.Run()
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
		return []string{"--audio-format", "flac"}
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
