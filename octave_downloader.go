package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Telegram documents remote audio URLs up to 20 MB; keep headroom for bitrate
// estimates and container overhead before choosing the no-local-download path.
const telegramRemoteAudioLimit = 19 * 1024 * 1024

type octaveRemoteAudio struct {
	URL           string
	Title         string
	Artist        string
	Duration      int
	EstimatedSize int64
	CacheKey      string
}

func (d *downloader) octaveTracksForURL(ctx context.Context, rawURL string) (octaveAlbum, []octaveTrack, bool, error) {
	ref, ok := parseOctaveURL(rawURL)
	if !ok || d.octave == nil {
		return octaveAlbum{}, nil, false, errors.New("некорректная ссылка Octave")
	}
	if ref.AlbumID == "" {
		return octaveAlbum{}, []octaveTrack{{ID: ref.TrackID, Title: "Octave track " + ref.TrackID}}, false, nil
	}
	album, err := d.octave.getAlbum(ctx, ref.AlbumID)
	if err != nil {
		return octaveAlbum{}, nil, false, err
	}
	if ref.TrackID == "" {
		return album, album.Tracks, true, nil
	}
	for _, track := range album.Tracks {
		if track.ID == ref.TrackID {
			return album, []octaveTrack{track}, false, nil
		}
	}
	return octaveAlbum{}, nil, false, errors.New("трек из параметра t не найден в альбоме Octave")
}

func (d *downloader) previewOctave(ctx context.Context, rawURL string) (mediaPreview, error) {
	album, tracks, playlist, err := d.octaveTracksForURL(ctx, rawURL)
	if err != nil {
		return mediaPreview{}, err
	}
	if len(tracks) == 0 {
		return mediaPreview{}, errors.New("альбом Octave пуст или недоступен")
	}
	preview := mediaPreview{URL: rawURL, Extractor: "octave", TrackCount: len(tracks), IsPlaylist: playlist}
	if playlist {
		preview.SourceID, preview.Title, preview.Artist = album.ID, album.Title, album.Artist.Name
		for _, track := range tracks {
			preview.DurationSeconds += track.Duration
		}
	} else {
		track := tracks[0]
		preview.SourceID, preview.Title, preview.Artist = track.ID, track.Title, track.Artist.Name
		preview.DurationSeconds = track.Duration
		preview.TrackCount = 1
	}
	preview.Duration = secondsToHMS(preview.DurationSeconds)
	preview.Estimated128 = estimateAudioSize(preview.DurationSeconds, "mp3", "128")
	preview.Estimated320 = estimateAudioSize(preview.DurationSeconds, "mp3", "320")
	return preview, nil
}

func (d *downloader) downloadOctaveRange(ctx context.Context, rawURL, format, quality string, rangeStart, rangeEnd int, progress downloadProgress) ([]downloadResult, error) {
	_, tracks, playlist, err := d.octaveTracksForURL(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, errors.New("Octave не вернул треки")
	}
	start, end := 1, len(tracks)
	if playlist && rangeStart > 0 {
		start = rangeStart
	}
	if playlist && rangeEnd > 0 {
		end = rangeEnd
	}
	if start < 1 {
		start = 1
	}
	if end > len(tracks) {
		end = len(tracks)
	}
	if start > end {
		return nil, errors.New("выбран пустой диапазон альбома Octave")
	}
	selectionCount := end - start + 1
	limit := d.maxPlaylistTracks
	if limit <= 0 {
		limit = maxPlaylistTracks
	}
	if err := validatePlaylistSizeWithLimit(selectionCount, limit); err != nil {
		return nil, err
	}

	session, err := randomID()
	if err != nil {
		return nil, err
	}
	sessionDir := filepath.Join(d.downloadDir, session)
	if err := os.Mkdir(sessionDir, 0o700); err != nil {
		return nil, fmt.Errorf("создать каталог Octave-сессии: %w", err)
	}
	keepSession := false
	defer func() {
		if !keepSession {
			_ = os.RemoveAll(sessionDir)
		}
	}()

	downloadCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	coverPath := ""
	if needsOctaveConversion(format, quality) {
		for _, track := range tracks[start-1 : end] {
			if coverURL := track.coverURL(); coverURL != "" {
				candidate := filepath.Join(sessionDir, ".album-cover.jpg")
				coverStarted := time.Now()
				coverErr := d.downloadOctaveCover(downloadCtx, coverURL, candidate)
				logMediaStage("cover_download", "octave", coverStarted, regularFileSize(candidate), coverErr == nil, "mode", "session_cache")
				if coverErr == nil {
					coverPath = candidate
				}
				break
			}
		}
	}
	results := make([]downloadResult, selectionCount)
	maxDuration := d.maxDurationFor(format, quality)
	for position := start; position <= end; position++ {
		if err := downloadCtx.Err(); err != nil {
			return nil, err
		}
		track := tracks[position-1]
		index := position - start
		results[index] = downloadResult{
			Title: track.Title, Artist: track.Artist.Name, Duration: secondsToHMS(track.Duration),
			Session: session, CacheKey: sourceCacheKey("octave", track.ID, format, quality),
		}
		if track.Duration > 0 && track.Duration > maxDuration {
			results[index].Error = fmt.Sprintf("«%s» — %s, это дольше %s: файл не влезет в лимит Telegram.", track.Title, secondsToHMS(track.Duration), secondsToHMS(maxDuration))
			continue
		}
		output, trackErr := d.downloadOctaveTrack(downloadCtx, track, format, quality, sessionDir, coverPath, position)
		if trackErr != nil {
			results[index].Error = trackErr.Error()
		} else {
			results[index].FilePath = output
			keepSession = true
		}
		if progress != nil && selectionCount > 1 {
			progress(index+1, selectionCount)
		}
	}
	return results, nil
}

func needsOctaveConversion(format, quality string) bool {
	return !strings.EqualFold(format, "mp3") || quality != "128" && quality != "320"
}

func octaveSourceQuality(format, quality string) string {
	if strings.EqualFold(format, "flac") {
		return "lossless"
	}
	if strings.EqualFold(format, "mp3") && quality == "128" {
		return "128"
	}
	return "320"
}

func octaveOutputExtension(format string) string {
	switch strings.ToLower(format) {
	case "flac", "m4a", "ogg":
		return strings.ToLower(format)
	default:
		return "mp3"
	}
}

func (d *downloader) octaveRemoteMP3(ctx context.Context, rawURL string, preview mediaPreview, quality string) (octaveRemoteAudio, bool, error) {
	if quality != "128" && quality != "320" {
		return octaveRemoteAudio{}, false, nil
	}
	ref, validURL := parseOctaveURL(rawURL)
	if !validURL || ref.TrackID == "" || ref.TrackID != preview.SourceID || !strings.EqualFold(preview.Extractor, "octave") || preview.DurationSeconds <= 0 {
		return octaveRemoteAudio{}, false, nil
	}
	estimatedSize := estimateAudioSize(preview.DurationSeconds, "mp3", quality)
	if estimatedSize <= 0 || estimatedSize > telegramRemoteAudioLimit {
		return octaveRemoteAudio{}, false, nil
	}
	token, err := d.octave.playbackToken(ctx)
	if err != nil {
		return octaveRemoteAudio{}, false, err
	}
	audioURL, err := d.octave.audioURL(ref.TrackID, quality, token)
	if err != nil {
		return octaveRemoteAudio{}, false, err
	}
	return octaveRemoteAudio{
		URL: audioURL, Title: preview.Title, Artist: preview.Artist,
		Duration: preview.DurationSeconds, EstimatedSize: estimatedSize,
		CacheKey: sourceCacheKey("octave", ref.TrackID, "mp3", quality),
	}, true, nil
}

func (d *downloader) downloadOctaveTrack(ctx context.Context, track octaveTrack, format, quality, sessionDir, coverPath string, position int) (string, error) {
	sourceQuality := octaveSourceQuality(format, quality)
	sourceExtension := "mp3"
	if sourceQuality == "lossless" {
		sourceExtension = "flac"
	}
	sourcePath := filepath.Join(sessionDir, fmt.Sprintf(".%06d_%s.source.%s", position, track.ID, sourceExtension))
	defer os.Remove(sourcePath)
	downloadStarted := time.Now()
	downloadErr := d.downloadOctaveAudio(ctx, track.ID, sourceQuality, sourcePath)
	logMediaStage("source_download", "octave", downloadStarted, regularFileSize(sourcePath), downloadErr == nil, "format", format, "quality", quality)
	if downloadErr != nil {
		return "", downloadErr
	}

	outputPath := filepath.Join(sessionDir, fmt.Sprintf("%06d_%s.%s", position, track.ID, octaveOutputExtension(format)))
	if strings.EqualFold(format, "mp3") && (quality == "128" || quality == "320") {
		prepareStarted := time.Now()
		renameErr := os.Rename(sourcePath, outputPath)
		logMediaStage("audio_prepare", "octave", prepareStarted, regularFileSize(outputPath), renameErr == nil, "mode", "passthrough", "format", format, "quality", quality)
		if renameErr != nil {
			return "", fmt.Errorf("подготовить MP3 Octave без конвертации: %w", renameErr)
		}
		return d.validateOctaveOutput(outputPath, track)
	}

	convertStarted := time.Now()
	convertErr := d.convertOctaveAudio(ctx, sourcePath, coverPath, outputPath, track, format, quality)
	logMediaStage("transcode", "octave", convertStarted, regularFileSize(outputPath), convertErr == nil, "format", format, "quality", quality)
	if convertErr != nil {
		return "", convertErr
	}
	return d.validateOctaveOutput(outputPath, track)
}

func (d *downloader) validateOctaveOutput(outputPath string, track octaveTrack) (string, error) {
	info, err := os.Stat(outputPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("Octave не создал готовый файл")
	}
	if d.maxFileSize > 0 && info.Size() > d.maxFileSize {
		_ = os.Remove(outputPath)
		return "", fmt.Errorf("«%s» — файл слишком большой (%d MiB)", track.Title, info.Size()/(1024*1024))
	}
	return outputPath, nil
}

func regularFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}

var octaveRetrySleep = func(ctx context.Context, attempt int) error {
	delay := time.Duration(1<<attempt) * 250 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *downloader) downloadOctaveAudio(ctx context.Context, trackID, quality, destination string) error {
	if d.octave == nil || d.octave.client == nil {
		return errors.New("Octave HTTP client не настроен")
	}
	limit := d.maxFileSize * 4
	if limit < 64*1024*1024 {
		limit = 64 * 1024 * 1024
	}
	partial := destination + ".part"
	defer func() {
		if ctx.Err() != nil {
			_ = os.Remove(partial)
		}
	}()
	var lastErr error
	var validator string
	authFailures := 0
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := d.octave.playbackToken(ctx)
		if err != nil {
			lastErr = err
		} else {
			endpoint, urlErr := d.octave.audioURL(trackID, quality, token)
			if urlErr != nil {
				return urlErr
			}
			offset := regularFileSize(partial)
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if requestErr != nil {
				return requestErr
			}
			request.Header.Set("Accept", "audio/*")
			if offset > 0 {
				request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
				if validator != "" {
					request.Header.Set("If-Range", validator)
				}
			}
			response, requestErr := d.octave.client.Do(request)
			if requestErr != nil {
				lastErr = fmt.Errorf("скачать аудио Octave: %s", strings.ReplaceAll(requestErr.Error(), token, "[redacted]"))
			} else {
				lastErr = d.appendOctaveResponse(response, partial, offset, limit, &validator)
				if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
					authFailures++
					d.octave.invalidatePlaybackToken()
					if authFailures >= 2 {
						_ = os.Remove(partial)
						return lastErr
					}
				}
				if lastErr == nil {
					if err := os.Rename(partial, destination); err != nil {
						return err
					}
					return nil
				}
			}
		}
		logMediaStage("source_retry", "octave", time.Now(), regularFileSize(partial), false, "attempt", attempt+1, "resumed_bytes", regularFileSize(partial))
		if attempt < 2 {
			if reporter := statusReporterFromContext(ctx); reporter != nil {
				reporter.stage(tr("stage_retry", reporter.lang, "attempt", strconv.Itoa(attempt+2), "bytes", humanSize(regularFileSize(partial), reporter.lang)))
			}
			if err := octaveRetrySleep(ctx, attempt); err != nil {
				return err
			}
		}
	}
	_ = os.Remove(partial)
	return lastErr
}

func (d *downloader) appendOctaveResponse(response *http.Response, partial string, offset, limit int64, validator *string) error {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Octave audio вернул HTTP %d", response.StatusCode)
	}
	if contentType := strings.ToLower(response.Header.Get("Content-Type")); contentType != "" && !strings.HasPrefix(contentType, "audio/") && contentType != "application/octet-stream" {
		return fmt.Errorf("Octave вернул неожиданный Content-Type %q", contentType)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 && response.StatusCode == http.StatusPartialContent {
		prefix := fmt.Sprintf("bytes %d-", offset)
		if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Range")), prefix) {
			return errors.New("Octave вернул некорректный Content-Range")
		}
		flags |= os.O_APPEND
	} else {
		offset = 0
		flags |= os.O_TRUNC
	}
	if etag := response.Header.Get("ETag"); etag != "" {
		*validator = etag
	} else if modified := response.Header.Get("Last-Modified"); modified != "" {
		*validator = modified
	}
	file, err := os.OpenFile(partial, flags, 0o600)
	if err != nil {
		return err
	}
	remaining := limit - offset
	if remaining < 0 {
		_ = file.Close()
		return errors.New("исходный файл Octave превышает безопасный лимит")
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, remaining+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("записать аудио Octave: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if written > remaining {
		return errors.New("исходный файл Octave превышает безопасный лимит")
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return fmt.Errorf("Octave прервал ответ: получено %d из %d байт", written, response.ContentLength)
	}
	return nil
}

func (d *downloader) downloadOctaveCover(ctx context.Context, rawURL, destination string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "cdn-images.dzcdn.net") {
		return errors.New("небезопасный URL обложки Octave")
	}
	client := d.octave.coverClient
	if client == nil {
		client = newOctaveHTTPClient("cdn-images.dzcdn.net")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("обложка Octave вернула HTTP %d", response.StatusCode)
	}
	if contentType := strings.ToLower(response.Header.Get("Content-Type")); contentType != "" && !strings.HasPrefix(contentType, "image/") {
		return errors.New("Octave вернул не изображение вместо обложки")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, 10*1024*1024+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > 10*1024*1024 {
		_ = os.Remove(destination)
		return errors.New("не удалось безопасно сохранить обложку Octave")
	}
	return nil
}

func (d *downloader) convertOctaveAudio(ctx context.Context, sourcePath, coverPath, outputPath string, track octaveTrack, format, quality string) error {
	ffmpeg := d.ffmpegBin
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			return errors.New("ffmpeg не найден в PATH")
		}
	}
	run := func(withCover bool) error {
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", sourcePath}
		if withCover {
			args = append(args, "-i", coverPath)
		}
		args = append(args, "-map", "0:a:0")
		if withCover {
			args = append(args, "-map", "1:v:0")
		}
		switch strings.ToLower(format) {
		case "flac":
			args = append(args, "-c:a", "copy")
		case "m4a":
			args = append(args, "-c:a", "aac", "-b:a", "256k")
		case "ogg":
			args = append(args, "-c:a", "libvorbis", "-q:a", "5")
		default:
			if quality == "128" || quality == "320" {
				args = append(args, "-c:a", "copy", "-id3v2_version", "3")
			} else {
				args = append(args, "-c:a", "libmp3lame", "-q:a", "0", "-id3v2_version", "3")
			}
		}
		if withCover {
			args = append(args, "-c:v", "mjpeg", "-disposition:v:0", "attached_pic")
		} else {
			args = append(args, "-vn")
		}
		args = append(args,
			"-metadata", "title="+track.Title,
			"-metadata", "artist="+track.Artist.Name,
			"-metadata", "album="+track.Album.Title,
		)
		if track.Position > 0 {
			args = append(args, "-metadata", "track="+strconv.Itoa(track.Position))
		}
		if track.Year != "" {
			args = append(args, "-metadata", "date="+track.Year)
		}
		if track.Genre != "" {
			args = append(args, "-metadata", "genre="+track.Genre)
		}
		args = append(args, outputPath)
		command := exec.CommandContext(ctx, ffmpeg, args...)
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error {
			if command.Process == nil {
				return os.ErrProcessDone
			}
			return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		command.WaitDelay = 5 * time.Second
		stderr := &limitedBuffer{limit: 64 * 1024}
		command.Stdout = io.Discard
		command.Stderr = stderr
		if err := command.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ffmpeg Octave: %w: %s", err, firstLine(stderr.String()))
		}
		return nil
	}
	withCover := coverPath != "" && !strings.EqualFold(format, "ogg")
	if err := run(withCover); err != nil && withCover {
		_ = os.Remove(outputPath)
		return run(false)
	} else {
		return err
	}
}
