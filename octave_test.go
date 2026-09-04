package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func testOctaveClient(handler http.Handler) *octaveClient {
	return &octaveClient{baseURL: "https://octave.test", client: handlerClient{handler: handler}}
}

func TestOctaveDownloadResumesWithRange(t *testing.T) {
	var audioCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/playback-token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"octk_test_token","expiresIn":7200}`)
		case "/audio/320":
			call := audioCalls.Add(1)
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Header().Set("ETag", `"one"`)
			if call == 1 {
				w.Header().Set("Content-Length", "10")
				fmt.Fprint(w, "abcd")
				return
			}
			if r.Header.Get("Range") != "bytes=4-" || r.Header.Get("If-Range") != `"one"` {
				t.Errorf("resume headers: Range=%q If-Range=%q", r.Header.Get("Range"), r.Header.Get("If-Range"))
			}
			w.Header().Set("Content-Range", "bytes 4-9/10")
			w.WriteHeader(http.StatusPartialContent)
			fmt.Fprint(w, "efghij")
		default:
			http.NotFound(w, r)
		}
	})
	previousSleep := octaveRetrySleep
	octaveRetrySleep = func(context.Context, int) error { return nil }
	t.Cleanup(func() { octaveRetrySleep = previousSleep })
	directory := t.TempDir()
	destination := filepath.Join(directory, "audio.mp3")
	d := &downloader{octave: testOctaveClient(handler), maxFileSize: maxFileSize}
	if err := d.downloadOctaveAudio(context.Background(), "11", "320", destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "abcdefghij" || audioCalls.Load() != 2 {
		t.Fatalf("data=%q calls=%d err=%v", data, audioCalls.Load(), err)
	}
}

func TestOctaveDownloadRefreshesRejectedTokenOnce(t *testing.T) {
	var tokenCalls atomic.Int32
	var audioCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/playback-token":
			call := tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":"octk_token_%d","expiresIn":7200}`, call)
		case "/audio/320":
			audioCalls.Add(1)
			if r.URL.Query().Get("k") == "octk_token_1" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			fmt.Fprint(w, "audio")
		default:
			http.NotFound(w, r)
		}
	})
	previousSleep := octaveRetrySleep
	octaveRetrySleep = func(context.Context, int) error { return nil }
	t.Cleanup(func() { octaveRetrySleep = previousSleep })
	destination := filepath.Join(t.TempDir(), "audio.mp3")
	d := &downloader{octave: testOctaveClient(handler), maxFileSize: maxFileSize}
	if err := d.downloadOctaveAudio(context.Background(), "11", "320", destination); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 2 || audioCalls.Load() != 2 || string(mustReadFile(t, destination)) != "audio" {
		t.Fatalf("token_calls=%d audio_calls=%d", tokenCalls.Load(), audioCalls.Load())
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOctaveAlbumDownloadsCoverOnce(t *testing.T) {
	var coverCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/album/3":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"album":{"id":"3","title":"Album","artist":{"id":"2","name":"Artist"},"cover_xl":"https://cdn-images.dzcdn.net/images/cover/test.jpg","tracks":[{"id":"11","title":"One","duration":1},{"id":"12","title":"Two","duration":1}]}}`)
		case "/api/playback-token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"octk_test_token","expiresIn":7200}`)
		case "/audio/320":
			w.Header().Set("Content-Type", "audio/mpeg")
			fmt.Fprint(w, "audio")
		case "/images/cover/test.jpg":
			coverCalls.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			fmt.Fprint(w, "cover")
		default:
			http.NotFound(w, r)
		}
	})
	directory := t.TempDir()
	ffmpeg := filepath.Join(directory, "ffmpeg")
	script := "#!/bin/sh\nsrc=''\nprev=''\nlast=''\nfor arg do\n  if [ \"$prev\" = '-i' ] && [ -z \"$src\" ]; then src=$arg; fi\n  prev=$arg\n  last=$arg\ndone\ncp \"$src\" \"$last\"\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := testOctaveClient(handler)
	client.coverClient = handlerClient{handler: handler}
	d := &downloader{downloadDir: directory, ffmpegBin: ffmpeg, maxFileSize: maxFileSize, maxPlaylistTracks: 75, octave: client}
	results, err := d.downloadOctaveRange(context.Background(), "https://music.octavestreaming.com/album/3", "m4a", "best", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.clearSession(results[0].Session)
	if len(results) != 2 || results[0].Error != "" || results[1].Error != "" || coverCalls.Load() != 1 {
		t.Fatalf("results=%#v cover_calls=%d", results, coverCalls.Load())
	}
}

func TestParseOctaveURL(t *testing.T) {
	tests := []struct {
		raw  string
		want octaveReference
		ok   bool
	}{
		{"https://music.octavestreaming.com/album/1014994861?t=4115799051", octaveReference{AlbumID: "1014994861", TrackID: "4115799051"}, true},
		{"https://music.octavestreaming.com/album/1014994861", octaveReference{AlbumID: "1014994861"}, true},
		{"https://api.octavestreaming.com/api/track/4115799051", octaveReference{TrackID: "4115799051"}, true},
		{"http://music.octavestreaming.com/album/1?t=2", octaveReference{}, false},
		{"https://music.octavestreaming.com/album/not-a-number?t=4115799051", octaveReference{}, false},
		{"https://music.octavestreaming.com@127.0.0.1/album/1?t=2", octaveReference{}, false},
		{"https://evil.example/album/1?t=2", octaveReference{}, false},
	}
	for _, test := range tests {
		got, ok := parseOctaveURL(test.raw)
		if ok != test.ok || got != test.want {
			t.Errorf("parseOctaveURL(%q) = %#v, %v; want %#v, %v", test.raw, got, ok, test.want, test.ok)
		}
	}
}

func TestOctaveClientSearchAlbumAndTokenCache(t *testing.T) {
	var tokenCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/search/tracks":
			if r.URL.Query().Get("query") != "A & B" || r.URL.Query().Get("limit") != "5" {
				t.Errorf("unexpected search query: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"results":[{"id":"11","title":"Track","artist":{"id":"2","name":"Artist"},"album":{"id":"3","title":"Album"},"duration":125}]}`)
		case "/api/album/3":
			fmt.Fprint(w, `{"album":{"id":"3","title":"Album","artist":{"id":"2","name":"Artist"},"releaseDate":"2026-01-02","tracks":[{"id":"11","title":"Track","artist":{"id":"2","name":"Artist"},"duration":125}]}}`)
		case "/api/playback-token":
			tokenCalls.Add(1)
			fmt.Fprint(w, `{"token":"octk_test_token","expiresIn":7200}`)
		default:
			http.NotFound(w, r)
		}
	})
	client := testOctaveClient(handler)

	tracks, err := client.searchTracks(context.Background(), "A & B", 5, 0)
	if err != nil || len(tracks) != 1 || tracks[0].Title != "Track" {
		t.Fatalf("tracks=%#v err=%v", tracks, err)
	}
	album, err := client.getAlbum(context.Background(), "3")
	if err != nil || len(album.Tracks) != 1 || album.Tracks[0].Position != 1 || album.Tracks[0].Year != "2026" || album.Tracks[0].Album.ID != "3" {
		t.Fatalf("album=%#v err=%v", album, err)
	}
	first, err := client.playbackToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.playbackToken(context.Background())
	if err != nil || first != second || tokenCalls.Load() != 1 {
		t.Fatalf("tokens=%q/%q calls=%d err=%v", first, second, tokenCalls.Load(), err)
	}
}

func TestOctavePreview(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/album/3":
			fmt.Fprint(w, `{"album":{"id":"3","title":"Album","artist":{"id":"2","name":"Artist"},"tracks":[{"id":"11","title":"First","artist":{"id":"2","name":"Artist"},"duration":61},{"id":"12","title":"Second","artist":{"id":"2","name":"Artist"},"duration":62}]}}`)
		default:
			http.NotFound(w, r)
		}
	})
	d := &downloader{octave: testOctaveClient(handler)}

	preview, err := d.preview(context.Background(), "https://music.octavestreaming.com/album/3?t=12")
	if err != nil || preview.Title != "Second" || preview.SourceID != "12" || preview.Extractor != "octave" || preview.IsPlaylist {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	albumPreview, err := d.preview(context.Background(), "https://music.octavestreaming.com/album/3")
	if err != nil || !albumPreview.IsPlaylist || albumPreview.TrackCount != 2 || albumPreview.DurationSeconds != 123 {
		t.Fatalf("album preview=%#v err=%v", albumPreview, err)
	}
}

func TestOctaveRemoteMP3RejectsEstimatedFilesAboveURLLimit(t *testing.T) {
	var tokenCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/album/3":
			fmt.Fprint(w, `{"album":{"id":"3","title":"Album","artist":{"id":"2","name":"Artist"},"tracks":[{"id":"11","title":"Long Track","artist":{"id":"2","name":"Artist"},"duration":600}]}}`)
		case "/api/playback-token":
			tokenCalls.Add(1)
			fmt.Fprint(w, `{"token":"octk_test_token","expiresIn":7200}`)
		default:
			http.NotFound(w, r)
		}
	})
	d := &downloader{octave: testOctaveClient(handler)}
	preview := mediaPreview{SourceID: "11", Extractor: "octave", Title: "Long Track", Artist: "Artist", DurationSeconds: 600}
	remote, ok, err := d.octaveRemoteMP3(context.Background(), "https://music.octavestreaming.com/album/3?t=11", preview, "320")
	if err != nil || ok || remote.URL != "" || tokenCalls.Load() != 0 {
		t.Fatalf("remote=%#v ok=%v token_calls=%d err=%v", remote, ok, tokenCalls.Load(), err)
	}
}

func TestOctaveDownloadUsesDirectAudioAndCacheKey(t *testing.T) {
	var audioCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/album/3":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"album":{"id":"3","title":"Album","artist":{"id":"2","name":"Artist"},"tracks":[{"id":"11","title":"Track","artist":{"id":"2","name":"Artist"},"duration":60}]}}`)
		case "/api/playback-token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"octk_test_token","expiresIn":7200}`)
		case "/audio/320":
			audioCalls.Add(1)
			if r.URL.Query().Get("track") != "11" || r.URL.Query().Get("k") != "octk_test_token" {
				t.Errorf("audio query=%s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			fmt.Fprint(w, "audio-data")
		default:
			http.NotFound(w, r)
		}
	})

	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "fake-ffmpeg")
	script := "#!/bin/sh\nexit 99\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d := &downloader{
		downloadDir: dir, ffmpegBin: ffmpeg, maxFileSize: maxFileSize, maxPlaylistTracks: 75,
		octave: testOctaveClient(handler),
	}
	results, err := d.downloadRange(context.Background(), "https://music.octavestreaming.com/album/3?t=11", "mp3", "320", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != "" || results[0].CacheKey != "octave:11:mp3:320" || !regularFileExists(results[0].FilePath) {
		t.Fatalf("results=%#v", results)
	}
	defer d.clearSession(results[0].Session)
	data, err := os.ReadFile(results[0].FilePath)
	if err != nil || string(data) != "audio-data" || audioCalls.Load() != 1 {
		t.Fatalf("data=%q calls=%d err=%v", data, audioCalls.Load(), err)
	}
	if strings.Contains(results[0].FilePath, "octk_") {
		t.Fatal("playback token leaked into the file path")
	}
}

func TestConvertOctaveAudioWithFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	mp3Source := filepath.Join(dir, "source.mp3")
	flacSource := filepath.Join(dir, "source.flac")
	cover := filepath.Join(dir, "cover.jpg")
	commands := [][]string{
		{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.2", "-c:a", "libmp3lame", mp3Source},
		{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.2", "-c:a", "flac", flacSource},
		{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32", "-frames:v", "1", "-update", "1", cover},
	}
	for _, args := range commands {
		if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("prepare ffmpeg fixture: %v: %s", err, output)
		}
	}
	d := &downloader{ffmpegBin: ffmpeg}
	track := octaveTrack{
		ID: "11", Title: "Track", Artist: octaveArtist{Name: "Artist"},
		Album: octaveAlbumSummary{Title: "Album"}, Position: 1, Year: "2026",
	}
	tests := []struct {
		format, quality, source, cover string
	}{
		{"mp3", "320", mp3Source, cover},
		{"flac", "best", flacSource, cover},
		{"m4a", "best", mp3Source, cover},
		{"ogg", "best", mp3Source, cover},
	}
	for _, test := range tests {
		output := filepath.Join(dir, "output."+test.format)
		if err := d.convertOctaveAudio(context.Background(), test.source, test.cover, output, track, test.format, test.quality); err != nil {
			t.Errorf("convert %s: %v", test.format, err)
			continue
		}
		if info, err := os.Stat(output); err != nil || info.Size() == 0 {
			t.Errorf("convert %s produced invalid output: info=%v err=%v", test.format, info, err)
		}
	}
}
