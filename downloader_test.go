package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDownloadRangeSelectsOnlyRequestedPlaylistItems(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-yt-dlp")
	entries := make([]string, 30)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"id":"id%d","title":"Track %d","duration":60,"playlist_index":%d,"extractor":"youtube"}`, i+1, i+1, i+1)
	}
	script := `#!/bin/sh
case " $* " in
  *" --simulate "*) printf '%s' '{"_type":"playlist","title":"List","entries":[` + strings.Join(entries, ",") + `]}' ; exit 0 ;;
esac
dir=''; manifest=''; progress=''; items=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --paths) dir="$2"; shift 2 ;;
    --playlist-items) items="$2"; shift 2 ;;
    --print-to-file)
      template="$2"; output="$3"
      case "$template" in after_move:*) manifest="$output" ;; before_dl:*) progress="$output" ;; esac
      shift 3 ;;
    *) shift ;;
  esac
done
oldifs="$IFS"; IFS=,
for item in $items; do
  path="$dir/$(printf '%06d' "$item")_id$item.mp3"
  printf 'audio' > "$path"
  printf '%s\n' "$item" >> "$progress"
  printf '{"id":"id%s","title":"Track %s","duration":60,"playlist_index":%s,"filepath":"%s","extractor":"youtube"}\n' "$item" "$item" "$item" "$path" >> "$manifest"
done
IFS="$oldifs"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d := downloader{bin: bin, downloadDir: dir, maxFileSize: maxFileSize, maxPlaylistTracks: 75}
	results, err := d.downloadRange(context.Background(), "https://youtube.com/playlist?list=x", "mp3", "320", 11, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if len(results) > 0 {
			d.clearSession(results[0].Session)
		}
	}()
	if len(results) != 10 {
		t.Fatalf("results=%d", len(results))
	}
	if results[0].Title != "Track 11" || results[9].Title != "Track 20" {
		t.Fatalf("range=%q..%q", results[0].Title, results[9].Title)
	}
	if results[0].CacheKey != "youtube:id11:mp3:320" {
		t.Fatalf("cache key=%q", results[0].CacheKey)
	}
}

func TestMaxDurationFor(t *testing.T) {
	d := downloader{maxFileSize: maxFileSize}
	tests := map[string]int{
		"mp3:128":   2875,
		"mp3:320":   1150,
		"mp3:best":  1445,
		"m4a:best":  890,
		"ogg:best":  2442,
		"flac:best": 217,
	}
	for key, want := range tests {
		parts := strings.SplitN(key, ":", 2)
		if got := d.maxDurationFor(parts[0], parts[1]); got != want {
			t.Errorf("maxDurationFor(%q, %q) = %d, want %d", parts[0], parts[1], got, want)
		}
	}
}

func TestAudioFormatArgs(t *testing.T) {
	tests := []struct {
		format  string
		quality string
		want    []string
	}{
		{"mp3", "best", []string{"--audio-format", "mp3", "--audio-quality", "0"}},
		{"mp3", "320", []string{"--audio-format", "mp3", "--audio-quality", "320K"}},
		{"flac", "best", []string{"--audio-format", "flac"}},
		{"ogg", "best", []string{"--audio-format", "vorbis", "--audio-quality", "5"}},
	}
	for _, test := range tests {
		if got := audioFormatArgs(test.format, test.quality); !reflect.DeepEqual(got, test.want) {
			t.Errorf("audioFormatArgs(%q, %q) = %#v, want %#v", test.format, test.quality, got, test.want)
		}
	}
}

func TestYTDLPNetworkArgsAreConfigurable(t *testing.T) {
	d := downloader{}
	defaults := d.commonArgs()
	if !containsArgPair(defaults, "--concurrent-fragments", "4") || containsArg(defaults, "--sleep-requests") {
		t.Fatalf("default args=%q", defaults)
	}
	d.ytdlpFragments = 8
	d.ytdlpSleepRequests = 2
	configured := d.commonArgs()
	if !containsArgPair(configured, "--concurrent-fragments", "8") || !containsArgPair(configured, "--sleep-requests", "2") {
		t.Fatalf("configured args=%q", configured)
	}
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestBestErrorLine(t *testing.T) {
	message := "WARNING: transient warning\nERROR: actual failure"
	if got := bestErrorLine(message); got != "ERROR: actual failure" {
		t.Fatalf("bestErrorLine() = %q", got)
	}
}

func TestValidAudioPathStaysInsideSession(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "session")
	if err := os.Mkdir(session, 0o700); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(session, "track.mp3")
	outside := filepath.Join(root, "outside.mp3")
	for _, path := range []string{audio, outside} {
		if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := downloader{}
	if _, ok := d.validAudioPath(session, audio); !ok {
		t.Fatal("audio inside the session was rejected")
	}
	if _, ok := d.validAudioPath(session, outside); ok {
		t.Fatal("audio outside the session was accepted")
	}
}

func TestManifestLineCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.jsonl")
	content := "{\"id\":\"one\"}\nnot-json\n{\"id\":\"two\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := manifestLineCount(path); got != 2 {
		t.Fatalf("manifestLineCount() = %d, want 2", got)
	}
}

func TestOperationsUseIsolatedCookieSnapshot(t *testing.T) {
	dir := t.TempDir()
	cookies := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(cookies, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &downloader{downloadDir: dir, cookiesFile: cookies}
	if err := d.refreshCookieSnapshot(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookies, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := d.isolatedCookieFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" || snapshot == cookies {
		t.Fatalf("snapshot=%q content=%q", snapshot, data)
	}
}

func TestArgsBeforeSeparator(t *testing.T) {
	got := argsBeforeSeparator([]string{"--format", "audio", "--", "https://example.test"}, "--cookies", "snapshot.txt")
	want := []string{"--format", "audio", "--cookies", "snapshot.txt", "--", "https://example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%#v, want %#v", got, want)
	}
}

func TestTextSearchPrefersOctaveAndReportsFallback(t *testing.T) {
	youtubeBin := filepath.Join(t.TempDir(), "fake-yt-dlp")
	script := `#!/bin/sh
printf '%s' '{"entries":[{"id":"youtube-id","title":"Track","uploader":"Artist","duration":185,"url":"youtube-id"}]}'
`
	if err := os.WriteFile(youtubeBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	octaveJSON := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/search/tracks" || r.URL.Query().Get("query") != "Artist Track" {
				t.Errorf("unexpected Octave request %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		})
	}
	tests := []struct {
		name          string
		octave        http.Handler
		wantOutcome   searchOutcome
		wantExtractor string
		wantSourceID  string
		wantURL       string
		wantCacheKey  string
		wantDuration  string
	}{
		{
			name:          "octave hit",
			octave:        octaveJSON(`{"results":[{"id":"11","title":"Track","artist":{"id":"2","name":"Artist"},"album":{"id":"3","title":"Album"},"duration":185}]}`),
			wantOutcome:   searchOutcome{Source: "octave"},
			wantExtractor: "octave", wantSourceID: "11",
			wantURL:      "https://music.octavestreaming.com/album/3?t=11",
			wantCacheKey: "octave:11:mp3:320", wantDuration: "3:05",
		},
		{
			name:          "octave empty",
			octave:        octaveJSON(`{"results":[]}`),
			wantOutcome:   searchOutcome{Source: "youtube", FallbackReason: "no_results"},
			wantExtractor: "youtube", wantSourceID: "youtube-id",
			wantURL:      "https://www.youtube.com/watch?v=youtube-id",
			wantCacheKey: "youtube:youtube-id:mp3:320", wantDuration: "3:05",
		},
		{
			name: "octave error",
			octave: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			}),
			wantOutcome:   searchOutcome{Source: "youtube", FallbackReason: "api_error"},
			wantExtractor: "youtube", wantSourceID: "youtube-id",
			wantURL:      "https://www.youtube.com/watch?v=youtube-id",
			wantCacheKey: "youtube:youtube-id:mp3:320", wantDuration: "3:05",
		},
		{
			name:          "octave disabled",
			octave:        nil,
			wantOutcome:   searchOutcome{Source: "youtube"},
			wantExtractor: "youtube", wantSourceID: "youtube-id",
			wantURL:      "https://www.youtube.com/watch?v=youtube-id",
			wantCacheKey: "youtube:youtube-id:mp3:320", wantDuration: "3:05",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &downloader{bin: youtubeBin}
			if tc.octave != nil {
				d.octave = testOctaveClient(tc.octave)
			}
			candidates, outcome, err := d.textSearch(context.Background(), "Artist Track", 185)
			if err != nil {
				t.Fatal(err)
			}
			if outcome != tc.wantOutcome {
				t.Fatalf("outcome=%#v, want %#v", outcome, tc.wantOutcome)
			}
			if len(candidates) != 1 {
				t.Fatalf("candidates=%#v", candidates)
			}
			got := candidates[0]
			if got.Extractor != tc.wantExtractor || got.SourceID != tc.wantSourceID || got.URL != tc.wantURL || got.CacheKey != tc.wantCacheKey || got.Duration != tc.wantDuration {
				t.Fatalf("candidate=%#v", got)
			}
			if got.Title != "Track" || got.Artist != "Artist" || got.Match != "exact" {
				t.Fatalf("candidate ranking=%#v", got)
			}
		})
	}
}

func TestTextSearchKeepsDirectLinksWithoutSearching(t *testing.T) {
	var octaveCalls atomic.Int32
	d := &downloader{bin: "/does/not/exist", octave: testOctaveClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		octaveCalls.Add(1)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))}
	candidates, outcome, err := d.textSearch(context.Background(), "https://youtu.be/video-id", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].URL != "https://youtu.be/video-id" || outcome != (searchOutcome{Source: "youtube"}) || octaveCalls.Load() != 0 {
		t.Fatalf("candidates=%#v outcome=%#v octave calls=%d", candidates, outcome, octaveCalls.Load())
	}
}

func TestCookieForbiddenRetryPredicate(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	failed := errors.New("exit status 1")
	cases := []struct {
		name   string
		args   []string
		stderr string
		err    error
		ctx    context.Context
		want   bool
	}{
		{"youtube 403", []string{"-f", "best", "--", "https://www.youtube.com/watch?v=abc"}, "ERROR: unable to download video data: HTTP Error 403: Forbidden", failed, ctx, true},
		{"youtu.be 403", []string{"--", "https://youtu.be/abc"}, "HTTP Error 403: Forbidden", failed, ctx, true},
		{"ytsearch 403", []string{"--", "ytsearch5:test"}, "HTTP Error 403: Forbidden", failed, ctx, true},
		{"soundcloud 403", []string{"--", "https://soundcloud.com/a/b"}, "HTTP Error 403: Forbidden", failed, ctx, false},
		{"youtube other error", []string{"--", "https://youtu.be/abc"}, "ERROR: Video unavailable", failed, ctx, false},
		{"no error", []string{"--", "https://youtu.be/abc"}, "HTTP Error 403: Forbidden", nil, ctx, false},
		{"cancelled", []string{"--", "https://youtu.be/abc"}, "HTTP Error 403: Forbidden", failed, cancelled, false},
		{"no separator", []string{"https://youtu.be/abc"}, "HTTP Error 403: Forbidden", failed, ctx, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cookieForbiddenRetry(tc.args, tc.stderr, tc.err, tc.ctx); got != tc.want {
				t.Fatalf("cookieForbiddenRetry=%v, want %v", got, tc.want)
			}
		})
	}
}

// TestYouTube403RetriesWithoutCookies uses a fake yt-dlp that fails with 403 whenever --cookies is
// passed and succeeds otherwise, mirroring a SABR-bound cookie session.
func TestYouTube403RetriesWithoutCookies(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "yt-dlp")
	calls := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + calls + "\n" +
		"for a in \"$@\"; do if [ \"$a\" = \"--cookies\" ]; then echo 'ERROR: unable to download video data: HTTP Error 403: Forbidden' >&2; exit 1; fi; done\n" +
		"echo '{\"id\":\"abc\",\"title\":\"ok\"}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cookies := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(cookies, []byte("# Netscape HTTP Cookie File\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &downloader{bin: fake, downloadDir: dir, cookiesFile: cookies}
	if err := d.refreshCookieSnapshot(); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := d.run(context.Background(), "--dump-single-json", "--", "https://www.youtube.com/watch?v=abc")
	if err != nil {
		t.Fatalf("run must succeed after the no-cookies retry: %v", err)
	}
	if !strings.Contains(string(stdout), `"id":"abc"`) {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "--cookies") || strings.Contains(lines[1], "--cookies") {
		t.Fatalf("expected one call with cookies then one without, got %q", lines)
	}

	// A non-YouTube 403 must not trigger the retry.
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.run(context.Background(), "--", "https://soundcloud.com/a/b"); err == nil {
		t.Fatal("soundcloud 403 must stay an error")
	}
	data, _ = os.ReadFile(calls)
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 1 {
		t.Fatalf("non-YouTube 403 must not retry, calls=%d", got)
	}

	// runWithProgress follows the same rule.
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "progress.log")
	if _, _, err := d.runWithProgress(context.Background(), []string{"--", "https://youtu.be/abc"}, manifest, nil, 1); err != nil {
		t.Fatalf("runWithProgress must succeed after retry: %v", err)
	}
	data, _ = os.ReadFile(calls)
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 2 {
		t.Fatalf("runWithProgress retry calls=%d, want 2", got)
	}
}
