package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestPastedTracklist(t *testing.T) {
	got := pastedTracklist("1. Daft Punk - One More Time\n2) Air — Playground Love\n\n  Justice – D.A.N.C.E  \nA — B - C\nJust A Title")
	want := []exportTrack{
		{Artist: "Daft Punk", Title: "One More Time"},
		{Artist: "Air", Title: "Playground Love"},
		{Artist: "Justice", Title: "D.A.N.C.E"},
		{Artist: "A", Title: "B - C"},
		{Title: "Just A Title"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tracks=%#v", got)
	}
	for _, text := range []string{
		"Daft Punk — Get Lucky",
		"привет\nкак скачать плейлист?",
		"Artist - Title\nsome words\nmore words\nand more",
		"- Title\nArtist -\n - ",
	} {
		if tracks := pastedTracklist(text); tracks != nil {
			t.Errorf("%q is not a tracklist: %#v", text, tracks)
		}
	}
}

func TestPastedTracklistOffersRangesWithoutExport(t *testing.T) {
	telegram := &exportTelegram{}
	application := newExportTestApp(t, telegram)
	application.cfg.MaxPlaylistTracks = maxPlaylistTracks
	application.presentPastedTracklist(musicLinkMessage("A - 1\nB - 2"), []exportTrack{{Artist: "A", Title: "1"}, {Artist: "B", Title: "2"}}, "en")
	calls := telegram.snapshot()
	if len(calls) != 1 || calls[0].method != "sendMessage" {
		t.Fatalf("calls=%#v", calls)
	}
	text, markup := calls[0].text, calls[0].markup
	if !strings.Contains(text, tr("pasted_tracklist_title", "en")) || !strings.Contains(text, tr("pasted_tracklist", "en")) || !strings.Contains(text, tr("preview_tracks", "en", "count", "2")) {
		t.Fatalf("text=%q", text)
	}
	if !strings.Contains(markup, `"range:all:`) || !strings.Contains(markup, `"cancel:`) || strings.Contains(markup, `"export:`) || strings.Contains(markup, `"cover:`) {
		t.Fatalf("markup=%s", markup)
	}
	key := strings.SplitN(strings.SplitN(markup, `"range:all:`, 2)[1], `"`, 2)[0]
	pending, ok := application.getURL(key, 10, 10)
	if !ok || !searchedTracklist(pending.Preview) || pending.URL != "" || !batchedPlaylistDelivery(pending) {
		t.Fatalf("pending=%#v ok=%v", pending, ok)
	}
}

// fakeSearchYTDLP writes a fake yt-dlp that answers a YouTube search with one video named after
// the query (nothing for a query with "Missing"), and probes and downloads single videos. Every
// search query is appended to the returned log.
func fakeSearchYTDLP(t *testing.T) (bin, queries string) {
	t.Helper()
	directory := t.TempDir()
	queries = filepath.Join(directory, "queries.log")
	bin = filepath.Join(directory, "fake-yt-dlp")
	script := `#!/bin/sh
for last do :; done
case "$last" in
  ytsearch*)
    query="${last#*:}"
    printf '%s\n' "$query" >> ` + queries + `
    case "$query" in *Missing*) printf '%s' '{"_type":"playlist","entries":[]}' ; exit 0 ;; esac
    id=$(printf '%s' "$query" | tr -cd 'A-Za-z0-9')
    printf '{"_type":"playlist","entries":[{"id":"%s","title":"%s","channel":"Chan","duration":200,"url":"https://www.youtube.com/watch?v=%s"}]}' "$id" "$query" "$id"
    exit 0 ;;
esac
id="${last##*=}"
case " $* " in
  *" --simulate "*) printf '{"id":"%s","title":"Video %s","uploader":"Chan","duration":200,"extractor":"youtube"}' "$id" "$id" ; exit 0 ;;
esac
dir=''; manifest=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --paths) dir="$2"; shift 2 ;;
    --print-to-file)
      case "$2" in after_move:*) manifest="$3" ;; esac
      shift 3 ;;
    *) shift ;;
  esac
done
path="$dir/$id.mp3"
printf 'audio %s' "$id" > "$path"
printf '{"id":"%s","title":"Video %s","uploader":"Chan","duration":200,"filepath":"%s","ext":"mp3","extractor":"youtube"}\n' "$id" "$id" "$path" >> "$manifest"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, queries
}

func TestSearchedTracklistDownloadsEveryFoundTrack(t *testing.T) {
	bin, queries := fakeSearchYTDLP(t)
	telegram := &exportTelegram{}
	application := newExportTestApp(t, telegram)
	downloadDir := t.TempDir()
	application.downloader = &downloader{downloadDir: downloadDir, bin: bin, maxFileSize: maxFileSize, maxPlaylistTracks: maxPlaylistTracks}
	application.cfg.MaxFileSize, application.cfg.MaxPlaylistTracks = maxFileSize, maxPlaylistTracks
	tracks := []exportTrack{
		{Artist: "Daft Punk", Title: "One More Time", Seconds: 320},
		{Artist: "Missing", Title: "Nowhere"},
		{Artist: "Air", Title: "Playground Love"},
		{Title: "Only Title"},
	}
	pending := pendingURL{ChatID: 10, UserID: 10, Delivery: "zip", Preview: mediaPreview{IsPlaylist: true, TrackCount: len(tracks), Tracks: tracks, Extractor: tracklistExtractor}}
	report, err := application.downloadAndSendPlaylistZIPs(context.Background(), 10, pending, "mp3", "320", "en", nil)
	if err != nil || report.Delivered != 3 || report.Failed != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	logged, err := os.ReadFile(queries)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"Daft Punk One More Time", "Air Playground Love", "Only Title"} {
		if !strings.Contains(string(logged), query+"\n") {
			t.Fatalf("query %q was not searched: %q", query, logged)
		}
	}
	var archive []byte
	var summary string
	for _, call := range telegram.snapshot() {
		switch call.method {
		case "sendDocument":
			archive = []byte(call.file)
		case "sendMessage":
			summary = call.text
		}
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	for _, file := range reader.File {
		entry, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		_, _ = data.ReadFrom(entry)
		entry.Close()
		contents = append(contents, data.String())
	}
	sort.Strings(contents)
	if want := []string{"audio AirPlaygroundLove", "audio DaftPunkOneMoreTime", "audio OnlyTitle"}; !reflect.DeepEqual(contents, want) {
		t.Fatalf("archive=%q", contents)
	}
	if !strings.Contains(summary, "3/4") {
		t.Fatalf("summary=%q", summary)
	}
	if left, err := os.ReadDir(downloadDir); err != nil || len(left) != 0 {
		t.Fatalf("sessions left on disk: %v %v", left, err)
	}
}
