package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDownloadRangeSelectsOnlyRequestedPlaylistItems(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-yt-dlp")
	entries := make([]string, 30)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"_type":"url","ie_key":"Youtube","id":"id%d","url":"https://www.youtube.com/watch?v=id%d","title":"Track %d","duration":60}`, i+1, i+1, i+1)
	}
	// A full probe of the playlist would open every video; only the flat listing is answered.
	script := `#!/bin/sh
case " $* " in
  *" --simulate "*" --flat-playlist "*|*" --flat-playlist "*" --simulate "*) printf '%s' '{"_type":"playlist","title":"List","entries":[` + strings.Join(entries, ",") + `]}' ; exit 0 ;;
  *" --simulate "*) exit 1 ;;
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
  printf '{"id":"id%s","title":"Track %s","uploader":"Artist %s","duration":61,"playlist_index":%s,"filepath":"%s","extractor":"youtube","webpage_url":"https://www.youtube.com/watch?v=id%s"}\n' "$item" "$item" "$item" "$item" "$path" "$item" >> "$manifest"
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
	// The flat entry has no artist; the downloaded track's metadata replaces it.
	if results[0].Artist != "Artist 11" || results[0].DurationSeconds != 61 || results[0].URL != "https://www.youtube.com/watch?v=id11" {
		t.Fatalf("metadata=%+v", results[0])
	}
}

func TestPreviewListsPlaylistFlat(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-yt-dlp")
	script := `#!/bin/sh
case " $* " in
  *" --flat-playlist "*) printf '%s' '{"_type":"playlist","title":"List","entries":[{"_type":"url","ie_key":"Youtube","id":"a","title":"A","duration":100},{"_type":"url","ie_key":"Youtube","id":"b","title":"B","duration":50}]}' ;;
  *) sleep 5 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d := downloader{bin: bin, downloadDir: dir, maxFileSize: maxFileSize}
	preview, err := d.preview(context.Background(), "https://www.youtube.com/playlist?list=x")
	if err != nil {
		t.Fatal(err)
	}
	if !preview.IsPlaylist || preview.TrackCount != 2 || preview.DurationSeconds != 150 || len(preview.Tracks) != 2 {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestMaxDurationFor(t *testing.T) {
	d := downloader{maxFileSize: maxFileSize}
	tests := map[string]int{
		// Constant-bitrate MP3 is checked exactly, the rest with sizePrecheckSlack.
		"mp3:128":   3125,
		"mp3:320":   1250,
		"mp3:best":  1963,
		"m4a:best":  1209,
		"ogg:best":  3318,
		"flac:best": 520,
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
		{"flac", "best", []string{"--audio-format", "flac", "--postprocessor-args", "ExtractAudio:-sample_fmt s16"}},
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

func TestSearchLookupUsesYouTube(t *testing.T) {
	youtubeBin := filepath.Join(t.TempDir(), "fake-yt-dlp")
	script := `#!/bin/sh
printf '%s' '{"entries":[{"id":"youtube-id","title":"Track","uploader":"Artist","duration":185,"url":"youtube-id"}]}'
`
	if err := os.WriteFile(youtubeBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d := &downloader{bin: youtubeBin}
	candidates, err := d.searchLookup(context.Background(), "Artist Track", 185)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates=%#v", candidates)
	}
	got := candidates[0]
	if got.Extractor != "youtube" || got.SourceID != "youtube-id" || got.URL != "https://www.youtube.com/watch?v=youtube-id" || got.CacheKey != "youtube:youtube-id:mp3:320" || got.Duration != "3:05" {
		t.Fatalf("candidate=%#v", got)
	}
	if got.Title != "Track" || got.Artist != "Artist" || got.Match != "exact" {
		t.Fatalf("candidate ranking=%#v", got)
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

}

// TestYouTubeDownloadOrder covers runDownload: a YouTube download starts without cookies, retries
// only the failed items, once without cookies after a 403 and then with cookies when YouTube asks
// to sign in.
func TestYouTubeDownloadOrder(t *testing.T) {
	const (
		forbidden = "ERROR: unable to download video data: HTTP Error 403: Forbidden"
		signIn    = "ERROR: [youtube] abc: Sign in to confirm your age. Use --cookies for the authentication."
	)
	cases := []struct {
		name string
		// rules lines are "<cookies|anonymous> <attempt|any> <item> <stderr>": the item fails with
		// that stderr on the matching call; every other item downloads.
		rules      []string
		cookies    bool
		target     string
		selected   []int
		wantCalls  []string // "<cookies|anonymous> <items>" per call
		wantFailed bool
	}{
		{"anonymous works", nil, true, "https://youtu.be/abc", []int{1}, []string{"anonymous 1"}, false},
		{"sign in uses cookies", []string{"anonymous any 1 " + signIn}, true, "https://youtu.be/abc", []int{1}, []string{"anonymous 1", "cookies 1"}, false},
		{"sign in without cookies", []string{"anonymous any 1 " + signIn}, false, "https://youtu.be/abc", []int{1}, []string{"anonymous 1"}, true},
		{"anonymous 403 retried", []string{"anonymous 1 1 " + forbidden}, true, "https://www.youtube.com/watch?v=abc", []int{1}, []string{"anonymous 1", "anonymous 1"}, false},
		{"anonymous 403 twice", []string{"anonymous any 1 " + forbidden}, true, "https://youtu.be/abc", []int{1}, []string{"anonymous 1", "anonymous 1"}, true},
		{"other error", []string{"anonymous any 1 ERROR: [youtube] abc: Video unavailable"}, true, "https://youtu.be/abc", []int{1}, []string{"anonymous 1"}, true},
		{"other site keeps cookies", nil, true, "https://soundcloud.com/a/b", []int{1}, []string{"cookies 1"}, false},
		{
			"playlist retries only failed items",
			[]string{"anonymous 1 3 " + forbidden, "anonymous any 5 " + signIn},
			true, "https://www.youtube.com/playlist?list=x", []int{2, 3, 5},
			[]string{"anonymous 2,3,5", "anonymous 3,5", "cookies 5"}, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls.log")
			rules := filepath.Join(dir, "rules")
			if err := os.WriteFile(rules, []byte(strings.Join(tc.rules, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
mode=anonymous; dir=''; manifest=''; progress=''; items=1
while [ "$#" -gt 0 ]; do
  case "$1" in
    --cookies) mode=cookies; shift 2 ;;
    --paths) dir="$2"; shift 2 ;;
    --playlist-items) items="$2"; shift 2 ;;
    --print-to-file)
      case "$2" in after_move:*) manifest="$3" ;; before_dl:*) progress="$3" ;; esac
      shift 3 ;;
    *) shift ;;
  esac
done
echo "$mode $items" >> ` + calls + `
n=$(grep -c "^$mode " ` + calls + `)
status=0
oldifs="$IFS"; IFS=,
for item in $items; do
  printf '%s\n' "$item" >> "$progress"
  message=$(grep -E "^$mode ($n|any) $item " ` + rules + ` | cut -d' ' -f4-)
  if [ -n "$message" ]; then echo "$message" >&2; status=1; continue; fi
  path="$dir/$item.mp3"
  printf 'audio' > "$path"
  printf '{"id":"id%s","playlist_index":%s,"filepath":"%s"}\n' "$item" "$item" "$path" >> "$manifest"
done
IFS="$oldifs"
exit $status
`
			fake := filepath.Join(dir, "yt-dlp")
			if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			d := &downloader{bin: fake, downloadDir: dir}
			if tc.cookies {
				d.cookiesFile = filepath.Join(dir, "cookies.txt")
				if err := os.WriteFile(d.cookiesFile, []byte("# Netscape HTTP Cookie File\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := d.refreshCookieSnapshot(); err != nil {
					t.Fatal(err)
				}
			}
			session := filepath.Join(dir, "session")
			if err := os.Mkdir(session, 0o700); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(session, "manifest.jsonl")
			playlist := len(tc.selected) > 1
			var reported []int
			progress := func(completed, total int) { reported = append(reported, completed) }
			_, err := d.runDownload(context.Background(), tc.target, "mp3", "best", session, manifest, tc.selected, playlist, progress, len(tc.selected))
			if (err != nil) != tc.wantFailed {
				t.Fatalf("err=%v, want failed=%v", err, tc.wantFailed)
			}
			data, _ := os.ReadFile(calls)
			if got := strings.Split(strings.TrimSpace(string(data)), "\n"); !reflect.DeepEqual(got, tc.wantCalls) {
				t.Fatalf("calls=%q, want %q", got, tc.wantCalls)
			}
			downloaded, err := readManifest(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantFailed && len(downloaded) != len(tc.selected) {
				t.Fatalf("downloaded=%d, want %d", len(downloaded), len(tc.selected))
			}
			for _, completed := range reported {
				if completed > len(tc.selected) {
					t.Fatalf("progress=%v exceeds %d", reported, len(tc.selected))
				}
			}
			if playlist && (len(reported) == 0 || reported[len(reported)-1] != len(tc.selected)) {
				t.Fatalf("progress=%v, want it to end at %d", reported, len(tc.selected))
			}
		})
	}
}

func TestMissingItems(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "manifest.jsonl")
	if got := missingItems(manifest, []int{1}, false); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("no manifest: missing=%v", got)
	}
	if err := os.WriteFile(manifest, []byte(`{"id":"a","playlist_index":4}`+"\n"+`{"id":"b","playlist_index":9}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := missingItems(manifest, []int{1}, false); got != nil {
		t.Fatalf("single video downloaded: missing=%v", got)
	}
	if got := missingItems(manifest, []int{4, 6, 9, 11}, true); !reflect.DeepEqual(got, []int{6, 11}) {
		t.Fatalf("playlist: missing=%v", got)
	}
}

func TestYouTubeSignInRequired(t *testing.T) {
	for stderr, want := range map[string]bool{
		"ERROR: [youtube] abc: Sign in to confirm you're not a bot. Use --cookies-from-browser or --cookies for the authentication.": true,
		"ERROR: [youtube] abc: This video may be inappropriate for some users.":                                                      true,
		"ERROR: [youtube] abc: Join this channel to get access to members-only content like this video.":                             true,
		"ERROR: [youtube] abc: Private video. Sign in if you've been granted access to this video":                                   true,
		"ERROR: unable to download video data: HTTP Error 403: Forbidden":                                                            false,
		"ERROR: [youtube] abc: Video unavailable":                                                                                    false,
	} {
		if got := youtubeSignInRequired(stderr); got != want {
			t.Errorf("youtubeSignInRequired(%q)=%v, want %v", stderr, got, want)
		}
	}
}
