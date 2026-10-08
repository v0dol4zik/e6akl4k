package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The fixture uses the metadata fields returned by yt-dlp's Newgrounds extractor,
// and rejects any target other than the original audio page (including ytsearch).
func TestNewgroundsDirectTrackFlow(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-yt-dlp")
	script := `#!/bin/sh
for arg in "$@"; do target="$arg"; done
[ "$target" = 'https://www.newgrounds.com/audio/listen/549479' ] || exit 91
case " $* " in
  *" --simulate "*)
    printf '%s' '{"id":"549479","title":"B7 - BusMode","uploader":"Burn7","duration":143,"extractor":"Newgrounds","extractor_key":"Newgrounds","webpage_url":"https://www.newgrounds.com/audio/listen/549479","url":"https://audio.ngfiles.com/549479.mp3","thumbnail":"https://aicon.ngfiles.com/549/549479.png"}'
    exit 0 ;;
esac
dir=''; manifest=''; format=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --paths) dir="$2"; shift 2 ;;
    --audio-format) format="$2"; shift 2 ;;
    --print-to-file)
      case "$2" in after_move:*) manifest="$3" ;; esac
      shift 3 ;;
    *) shift ;;
  esac
done
case "$format" in vorbis) ext=ogg ;; mp3|m4a|flac) ext="$format" ;; *) exit 92 ;; esac
path="$dir/000001_549479.$ext"
printf 'audio' > "$path"
printf '{"id":"549479","title":"B7 - BusMode","uploader":"Burn7","duration":143,"extractor":"Newgrounds","webpage_url":"https://www.newgrounds.com/audio/listen/549479","filepath":"%s"}\n' "$path" > "$manifest"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	dl := &downloader{bin: bin, downloadDir: dir, maxFileSize: maxFileSize}
	application := &app{downloader: dl}
	ctx := context.Background()
	link := detectURL("http://newgrounds.com/audio/listen/549479/?ref=share")
	preview, err := application.inspectURL(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	if preview.URL != link || preview.Title != "B7 - BusMode" || preview.Artist != "Burn7" || preview.Duration != "2:23" || preview.DurationSeconds != 143 || preview.IsPlaylist || preview.TrackCount != 1 || preview.SourceID != "549479" || preview.Extractor != "Newgrounds" || preview.Estimated320 <= 0 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	candidates, err := application.inlineLinkCandidates(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("inline candidates = %+v", candidates)
	}
	candidate := candidates[0]
	if candidate.URL != link || candidate.CacheKey != "newgrounds:549479:mp3:320" || candidate.Title != preview.Title || candidate.Artist != preview.Artist || candidate.Duration != preview.Duration || candidate.Thumbnail != preview.Thumbnail {
		t.Fatalf("unexpected inline candidate: %+v", candidate)
	}
	for _, option := range []struct{ format, quality string }{
		{"mp3", "128"}, {"mp3", "320"}, {"mp3", "best"},
		{"m4a", "best"}, {"flac", "best"}, {"ogg", "best"},
	} {
		t.Run(option.format+"/"+option.quality, func(t *testing.T) {
			results, err := dl.download(ctx, link, option.format, option.quality, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 {
				t.Fatalf("results = %+v", results)
			}
			result := results[0]
			defer os.RemoveAll(filepath.Join(dir, result.Session))
			if result.Error != "" || result.TooLarge || result.Title != preview.Title || result.Artist != preview.Artist || result.DurationSeconds != 143 || result.URL != link || result.CacheKey != "newgrounds:549479:"+option.format+":"+option.quality || filepath.Ext(result.FilePath) != "."+option.format {
				t.Fatalf("unexpected download: %+v", result)
			}
			if _, err := os.Stat(result.FilePath); err != nil {
				t.Fatal(err)
			}
		})
	}
}
