package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSourceRejectionKeepsLighterFormatMetadata(t *testing.T) {
	h := newBatchHarness(t)
	h.app.cfg.YouTubeSingleExtract = true
	bin := filepath.Join(t.TempDir(), "reject-long")
	script := `#!/bin/sh
case " $* " in
  *" --simulate "*) printf '%s' '{"id":"aaaaaaaaaaa","title":"Long","artist":"Artist","duration":9000,"extractor":"youtube","webpage_url":"https://youtu.be/aaaaaaaaaaa"}' ;;
  *) case " $* " in *" --match-filter !is_live & duration <= "*) exit 0 ;; *) exit 17 ;; esac ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	h.app.downloader.bin = bin
	preview, _ := h.app.directYouTubePreview("https://youtu.be/aaaaaaaaaaa", "mp3", "320")
	results, err := h.app.downloader.downloadRangeWithPreview(context.Background(), preview.URL, "mp3", "320", 0, 0, preview, nil)
	defer h.app.downloader.clearSessions(results)
	if err != nil || len(results) != 1 || !results[0].TooLarge || results[0].DurationSeconds != 9000 || results[0].URL != preview.URL || results[0].FilePath != "" {
		t.Fatalf("results=%+v err=%v", results, err)
	}
}

func TestYouTubePreviewTimeoutFallbackIsBoundedAndKeepsSourceGuards(t *testing.T) {
	for _, tc := range []struct {
		name, rawURL                     string
		err                              error
		disabled, cancelled, unavailable bool
		wantFallback                     bool
	}{
		{name: "valid", rawURL: "https://youtu.be/aaaaaaaaaaa", err: context.DeadlineExceeded, wantFallback: true},
		{name: "playlist", rawURL: "https://youtube.com/watch?v=aaaaaaaaaaa&list=PL1", err: context.DeadlineExceeded},
		{name: "unsafe host", rawURL: "https://youtube.com.evil/watch?v=aaaaaaaaaaa", err: context.DeadlineExceeded},
		{name: "port", rawURL: "https://youtube.com:444/watch?v=aaaaaaaaaaa", err: context.DeadlineExceeded},
		{name: "disabled", rawURL: "https://youtu.be/aaaaaaaaaaa", err: context.DeadlineExceeded, disabled: true},
		{name: "cancelled request", rawURL: "https://youtu.be/aaaaaaaaaaa", err: context.DeadlineExceeded, cancelled: true},
		{name: "cancelled probe", rawURL: "https://youtu.be/aaaaaaaaaaa", err: context.Canceled},
		{name: "unavailable video", rawURL: "https://youtu.be/aaaaaaaaaaa", err: errors.New("video unavailable")},
		{name: "unavailable oembed", rawURL: "https://youtu.be/aaaaaaaaaaa", err: context.DeadlineExceeded, unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBatchHarness(t)
			h.app.cfg.YouTubeSingleExtract = !tc.disabled
			calls := 0
			previous := makeResolverClient
			makeResolverClient = func() httpDoer {
				return handlerClient{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Scheme != "https" || r.URL.Host != "www.youtube.com" || r.URL.Path != "/oembed" || r.URL.Query().Get("url") != "https://www.youtube.com/watch?v=aaaaaaaaaaa" {
						t.Errorf("unexpected oEmbed endpoint: %s", r.URL)
					}
					deadline, ok := r.Context().Deadline()
					if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
						t.Error("oEmbed request has no bounded deadline")
					}
					if tc.unavailable {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					fmt.Fprint(w, `{"title":"Title","author_name":"Artist"}`)
				})}
			}
			t.Cleanup(func() { makeResolverClient = previous })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			preview, err := h.app.youtubePreviewAfterTimeout(ctx, tc.rawURL, tc.err)
			if tc.wantFallback {
				if err != nil || calls != 1 || preview.URL != tc.rawURL || preview.SourceID != "aaaaaaaaaaa" || preview.Title != "Title" || preview.Artist != "Artist" || !preview.direct || preview.IsPlaylist || preview.TrackCount != 1 || preview.DurationSeconds != 0 || preview.probe != nil {
					t.Fatalf("preview=%+v err=%v calls=%d", preview, err, calls)
				}
			} else {
				wantCalls := 0
				if tc.unavailable {
					wantCalls = 1
				}
				if !errors.Is(err, tc.err) || calls != wantCalls || preview.SourceID != "" {
					t.Fatalf("preview=%+v err=%v calls=%d", preview, err, calls)
				}
			}
		})
	}
}

func TestContinuationQueueDoesNotConsumeNewRequestQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g := newJobGate(1, 1)
	_, release, err := g.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	continued, normal := make(chan struct{}), make(chan struct{})
	continuationDone, normalDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, r, err := g.acquireContinuation(ctx, func(int) { close(continued) })
		if r != nil {
			r()
		}
		continuationDone <- err
	}()
	select {
	case <-continued:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		_, r, err := g.acquireNotify(ctx, func(int) { close(normal) })
		if r != nil {
			r()
		}
		normalDone <- err
	}()
	select {
	case <-normal:
	case err := <-normalDone:
		t.Fatalf("new queue was occupied by a continuation: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	release()
	if err := <-continuationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-normalDone; err != nil {
		t.Fatal(err)
	}
}

func TestCacheFlightSurvivesFirstRecipientCancellation(t *testing.T) {
	var g flightGroup
	var calls atomic.Int32
	firstCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	fn := func(ctx context.Context) (cachedAudio, error) {
		calls.Add(1)
		select {
		case <-release:
			return cachedAudio{FileID: "shared"}, nil
		case <-ctx.Done():
			return cachedAudio{}, ctx.Err()
		}
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := g.doContext(firstCtx, "one", fn); first <- err }()
	go func() {
		entry, err := g.doContext(context.Background(), "one", fn)
		if err == nil && entry.FileID != "shared" {
			err = errors.New("wrong result")
		}
		second <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		call := g.m["one"]
		joined := call != nil && call.refs == 2
		g.mu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recipients did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-second; err != nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestSpeedConfigurationIsBounded(t *testing.T) {
	t.Setenv("BOT_TOKEN", "token")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, key := range []string{"BATCH_DOWNLOAD_WORKERS", "BATCH_STREAM_RESULTS", "YOUTUBE_SINGLE_EXTRACT"} {
		t.Setenv(key, "")
	}
	cfg, err := loadConfig()
	if err != nil || cfg.BatchDownloadWorkers != 2 || cfg.BatchStreamResults || !cfg.YouTubeSingleExtract {
		t.Fatalf("defaults: workers=%d stream=%t direct=%t err=%v", cfg.BatchDownloadWorkers, cfg.BatchStreamResults, cfg.YouTubeSingleExtract, err)
	}
	for _, value := range []string{"0", "3", "many"} {
		t.Setenv("BATCH_DOWNLOAD_WORKERS", value)
		if _, err := loadConfig(); err == nil {
			t.Fatalf("accepted workers %s", value)
		}
	}
	t.Setenv("BATCH_DOWNLOAD_WORKERS", "1")
	t.Setenv("BATCH_STREAM_RESULTS", "true")
	t.Setenv("YOUTUBE_SINGLE_EXTRACT", "false")
	cfg, err = loadConfig()
	if err != nil || cfg.BatchDownloadWorkers != 1 || !cfg.BatchStreamResults || cfg.YouTubeSingleExtract {
		t.Fatalf("overrides: %v", err)
	}
}

func TestTracklistCacheSkipsFullDownloadGate(t *testing.T) {
	h := newBatchHarness(t)
	cacheTrack(t, h.state, "11", "Cached")
	h.app.downloads = newJobGate(1, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, release, err := h.app.downloads.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	pending := pendingURL{Preview: mediaPreview{IsPlaylist: true, TrackCount: 2, Extractor: tracklistExtractor, Tracks: []exportTrack{{Artist: "Artist", Title: "One"}, {Artist: "Artist", Title: "Two"}}}}
	results, err := h.app.downloadPlaylistRange(ctx, pending, "mp3", "320", "en", 1, 2, nil)
	if err != nil || len(results) != 2 || results[0].Cached == nil || results[1].Cached == nil {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	report := h.app.sendResultsIndividuallyWithSummary(10, results, "mp3", "en", false, &statusReporter{ctx: ctx})
	if report.Delivered != 2 || report.Failed != 0 {
		t.Fatalf("report=%+v", report)
	}
	if files, _ := os.ReadDir(h.app.downloader.downloadDir); len(files) != 0 {
		t.Fatalf("cache created files: %v", files)
	}
}

func TestMixedCachedDeliveryPreservesRepeatedFilesAndSharedSessions(t *testing.T) {
	h := newBatchHarness(t)
	session := "aabbccdd"
	dir := filepath.Join(h.app.downloader.downloadDir, session)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var results []downloadResult
	for _, name := range []string{"one", "two"} {
		path := filepath.Join(dir, name+".mp3")
		if err := os.WriteFile(path, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		results = append(results, downloadResult{FilePath: path, Session: session, Title: name})
	}
	results = []downloadResult{results[0], {Cached: &cachedAudio{FileID: "already-cached", Format: "mp3", MediaType: "audio"}}, results[1], results[0]}
	var sent []string
	h.app.bot.Client = handlerClient{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if filepath.Base(r.URL.Path) == "sendAudio" {
			if err := r.ParseMultipartForm(1 << 20); err == nil {
				defer r.MultipartForm.RemoveAll()
				file, _, err := r.FormFile("audio")
				if err != nil {
					t.Error(err)
				} else {
					data, err := io.ReadAll(file)
					file.Close()
					if err != nil {
						t.Error(err)
					}
					sent = append(sent, string(data))
				}
			} else {
				sent = append(sent, r.FormValue("audio"))
			}
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"audio":{"file_id":"sent","duration":1}}}`)
	})}
	report := h.app.sendResultsIndividuallyWithSummary(10, results, "mp3", "en", false)
	if report.Delivered != 4 || report.Failed != 0 || strings.Join(sent, ",") != "one,already-cached,two,one" {
		t.Fatalf("report=%+v sent=%v", report, sent)
	}
	if files, _ := os.ReadDir(h.app.downloader.downloadDir); len(files) != 0 {
		t.Fatalf("files left: %v", files)
	}
}

func TestZIPRepeatedSourceSurvivesAcrossArchiveParts(t *testing.T) {
	h := newBatchHarness(t)
	h.app.cfg.MaxFileSize = 1000
	session := "aabbccdd"
	dir := filepath.Join(h.app.downloader.downloadDir, session)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "one.mp3")
	payload := bytes.Repeat([]byte("a"), 700)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	result := downloadResult{FilePath: path, Session: session, Title: "One"}
	sent := 0
	h.app.bot.Client = handlerClient{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if filepath.Base(r.URL.Path) == "sendDocument" {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			} else {
				defer r.MultipartForm.RemoveAll()
				file, _, err := r.FormFile("document")
				if err != nil {
					t.Error(err)
				} else {
					data, err := io.ReadAll(file)
					file.Close()
					reader, zipErr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
					if err != nil || zipErr != nil || len(reader.File) != 1 {
						t.Errorf("invalid archive: read=%v zip=%v", err, zipErr)
					} else {
						entry, err := reader.File[0].Open()
						if err != nil {
							t.Error(err)
						} else {
							contents, err := io.ReadAll(entry)
							entry.Close()
							if err != nil || !bytes.Equal(contents, payload) {
								t.Errorf("archive lost source: %v", err)
							}
							sent++
						}
					}
				}
			}
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"}}}`)
	})}
	report, err := h.app.sendZIPArchives(10, []downloadResult{result, result, result}, "mp3", "en", nil)
	if err != nil || report.Delivered != 3 || report.Failed != 0 || sent != 3 {
		t.Fatalf("report=%+v sent=%d err=%v", report, sent, err)
	}
	if files, _ := os.ReadDir(h.app.downloader.downloadDir); len(files) != 0 {
		t.Fatalf("files left: %v", files)
	}
}

// This integration check is entirely offline. It catches yt-dlp's --print
// shorthand and verifies that finished transfer precedes ffmpeg/after_move.
func TestYTDLPStageHooksWithLocalAudio(t *testing.T) {
	bin, err := exec.LookPath("yt-dlp")
	if err != nil {
		t.Skip("yt-dlp unavailable")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	data := make([]byte, 44+16000)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 16000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], 16000)
	if err := os.WriteFile(wav, data, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--ignore-config", "--enable-file-urls", "--no-simulate", "--progress", "--newline", "--progress-template", "download:" + mediaEventPrefix + "%(progress.status)s:%(progress.downloaded_bytes)j", "--print", "before_dl:" + mediaEventPrefix + "transfer", "--print", "after_move:" + mediaEventPrefix + "complete", "-x", "--audio-format", "mp3", "-o", filepath.Join(dir, "out.%(ext)s"), "file://" + wav}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	events := newDownloadEvents(ctx, args)
	stdout, stderr := events.stream(), events.stream()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	stdout.close()
	stderr.close()
	events.close(true)
	if events.size == 0 || !events.transferAt.IsZero() || !events.postAt.IsZero() || events.preparedAt.IsZero() {
		t.Fatalf("hooks missing: %+v", events)
	}
	// Check the production filter with readable local media: a short non-live
	// source passes, while oversized, live and unknown-duration sources do not.
	for _, tc := range []struct {
		name     string
		duration any
		live     bool
		allowed  bool
	}{{"short", 1, false, true}, {"oversized", 3600, false, false}, {"live", 1, true, false}, {"unknown", nil, false, false}} {
		info := map[string]any{"id": "local", "title": "local", "is_live": tc.live, "extractor": "Generic", "extractor_key": "Generic", "webpage_url": "file://" + wav, "url": "file://" + wav, "ext": "wav"}
		if tc.duration != nil {
			info["duration"] = tc.duration
		}
		jsonData, _ := json.Marshal(info)
		infoFile := filepath.Join(dir, "info.json")
		if err := os.WriteFile(infoFile, jsonData, 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"--ignore-config", "--enable-file-urls", "--no-simulate", "--match-filter", "!is_live & duration <= 60", "--print", "before_dl:" + mediaEventPrefix + "transfer", "--load-info-json", infoFile, "-o", filepath.Join(dir, tc.name+".%(ext)s")}
		output, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("filter failed: %v %s", err, output)
		}
		if strings.Contains(string(output), mediaEventPrefix+"transfer") != tc.allowed {
			t.Fatalf("%s: wrong filter result: %s", tc.name, output)
		}
		if _, err := os.Stat(filepath.Join(dir, tc.name+".wav")); (err == nil) != tc.allowed {
			t.Fatalf("%s: unexpected downloaded file: %v", tc.name, err)
		}
	}
}
