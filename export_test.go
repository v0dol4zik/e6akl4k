package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestUploadTrackNormalizesTitles(t *testing.T) {
	tests := []struct {
		uploader, title string
		want            exportTrack
	}{
		{"Daft Punk", "Daft Punk - Get Lucky (Official Video)", exportTrack{"Daft Punk", "Get Lucky"}},
		{"Rick Astley", "Never Gonna Give You Up [Official Music Video]", exportTrack{"Rick Astley", "Never Gonna Give You Up"}},
		{"Daft Punk - Topic", "Digital Love", exportTrack{"Daft Punk", "Digital Love"}},
		{"", "Кино — Группа крови (клип)", exportTrack{"Кино", "Группа крови"}},
		{"Artist", "Song (Remix)", exportTrack{"Artist", "Song (Remix)"}},
		{"Channel", "Jay-Z", exportTrack{"Channel", "Jay-Z"}},
		{"Channel", "(Official Video)", exportTrack{"Channel", "(Official Video)"}},
	}
	for _, test := range tests {
		if got := uploadTrack(test.uploader, test.title); got != test.want {
			t.Errorf("uploadTrack(%q, %q)=%#v, want %#v", test.uploader, test.title, got, test.want)
		}
	}
	music := &mediaInfo{Track: "Get Lucky", Artist: "Daft Punk, Pharrell Williams", Title: "Daft Punk - Get Lucky (Official Audio)", Channel: "Daft Punk"}
	if got := trackFromInfo(music).line(); got != "Daft Punk, Pharrell Williams - Get Lucky" {
		t.Fatalf("music metadata line=%q", got)
	}
	topic := &mediaInfo{Title: "Around the World", Channel: "Daft Punk - Topic"}
	if got := trackFromInfo(topic).line(); got != "Daft Punk - Around the World" {
		t.Fatalf("topic line=%q", got)
	}
	for track, want := range map[exportTrack]string{{"A", "T"}: "A - T", {"", "T"}: "T", {"A", ""}: "A", {" ", " "}: ""} {
		if got := track.line(); got != want {
			t.Errorf("line(%#v)=%q, want %q", track, got, want)
		}
	}
}

func TestExportFromInfoCapsAndSkipsUnavailableEntries(t *testing.T) {
	info := &mediaInfo{
		Type:          "playlist",
		Title:         "Album - Discovery",
		Uploader:      "Daft Punk - Topic",
		PlaylistCount: 1500,
		Entries:       []*mediaInfo{{Title: "One More Time", Channel: "Daft Punk - Topic"}, nil, {Title: "Aerodynamic", Channel: "Daft Punk - Topic"}},
	}
	result := exportFromInfo(info)
	if result.Name != "Daft Punk - Discovery" || !result.Playlist || result.Total != 1500 {
		t.Fatalf("result=%#v", result)
	}
	if got := result.lines(); !reflect.DeepEqual(got, []string{"Daft Punk - One More Time", "Daft Punk - Aerodynamic"}) {
		t.Fatalf("lines=%q", got)
	}
	single := exportFromInfo(&mediaInfo{Title: "Artist - Song (Lyrics)"})
	if single.Playlist || single.Name != "Artist - Song" || len(single.Tracks) != 1 {
		t.Fatalf("single=%#v", single)
	}
}

func TestPendingExportHonoursRangeAndBatch(t *testing.T) {
	tracks := []exportTrack{{"A", "1"}, {"A", "2"}, {"A", "3"}, {"A", "4"}, {"A", "5"}}
	playlist := mediaPreview{Title: "Album - Five", Artist: "A - Topic", IsPlaylist: true, TrackCount: 5, Tracks: tracks}
	ranged := pendingExport(pendingURL{Preview: playlist, RangeStart: 2, RangeEnd: 4})
	if ranged.Name != "A - Five" || !reflect.DeepEqual(ranged.lines(), []string{"A - 2", "A - 3", "A - 4"}) || ranged.Total != 0 {
		t.Fatalf("ranged=%#v", ranged)
	}
	clipped := pendingExport(pendingURL{Preview: playlist, RangeStart: 4, RangeEnd: 50})
	if !reflect.DeepEqual(clipped.lines(), []string{"A - 4", "A - 5"}) {
		t.Fatalf("clipped=%q", clipped.lines())
	}
	large := playlist
	large.TrackCount = 1200
	if capped := pendingExport(pendingURL{Preview: large}); capped.Total != 1200 || len(capped.Tracks) != 5 {
		t.Fatalf("capped=%#v", capped)
	}
	single := pendingExport(pendingURL{Preview: mediaPreview{Title: "Song [Official Video]", Artist: "Singer"}})
	if single.Playlist || single.Name != "Singer - Song" {
		t.Fatalf("single=%#v", single)
	}
	batch := pendingExport(pendingURL{
		Batch:         []string{"https://youtu.be/a", "https://youtu.be/b"},
		BatchPreviews: []mediaPreview{{Tracks: []exportTrack{{"X", "One"}}}, {Title: "Y - Two"}},
	})
	if !batch.Playlist || !reflect.DeepEqual(batch.lines(), []string{"X - One", "Y - Two"}) {
		t.Fatalf("batch=%#v", batch)
	}
}

func TestPickCoverPrefersSquareArtAndCropsTopic(t *testing.T) {
	wide := coverFile{path: "track.jpg", width: 1280, height: 720}
	square := coverFile{path: "playlist.jpg", width: 544, height: 544, playlist: true}
	if got, side := pickCover([]coverFile{wide, square}, true); got != square || side != 0 {
		t.Fatalf("square playlist art must win: %#v %d", got, side)
	}
	if got, side := pickCover([]coverFile{wide}, true); got != wide || side != 720 {
		t.Fatalf("topic thumbnail must be cropped to its centre: %#v %d", got, side)
	}
	if got, side := pickCover([]coverFile{wide}, false); got != wide || side != 0 {
		t.Fatalf("a plain upload thumbnail is sent as is: %#v %d", got, side)
	}
	widePlaylist := coverFile{path: "playlist.jpg", width: 1920, height: 1080, playlist: true}
	if got, side := pickCover([]coverFile{wide, widePlaylist}, false); got != widePlaylist || side != 0 {
		t.Fatalf("largest image must win: %#v %d", got, side)
	}
	if !squareImage(1000, 990) || squareImage(1000, 900) || squareImage(0, 0) {
		t.Fatal("unexpected squareImage tolerance")
	}
}

// exportAPIClient serves canned JSON by request path and query and records the requested URLs.
type exportAPIClient struct {
	mu        sync.Mutex
	responses map[string]string
	requested []string
}

func (c *exportAPIClient) install(t *testing.T) {
	t.Helper()
	old := makeResolverClient
	makeResolverClient = func() httpDoer {
		return handlerClient{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c.mu.Lock()
			c.requested = append(c.requested, r.URL.String())
			c.mu.Unlock()
			key := r.URL.Host + r.URL.Path
			if r.URL.RawQuery != "" {
				key += "?" + r.URL.RawQuery
			}
			body, ok := c.responses[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, body)
		})}
	}
	t.Cleanup(func() { makeResolverClient = old })
}

func deezerPage(from, count int, next bool) string {
	items := make([]string, 0, count)
	for i := from; i < from+count; i++ {
		items = append(items, fmt.Sprintf(`{"title":"Song %d","artist":{"name":"Artist %d"}}`, i, i))
	}
	nextURL := ""
	if next {
		nextURL = "https://api.deezer.com/playlist/123/tracks?index=" + strconv.Itoa(from+count)
	}
	return fmt.Sprintf(`{"data":[%s],"total":150,"next":%q}`, strings.Join(items, ","), nextURL)
}

func TestDeezerTracklistPaginatesPlaylists(t *testing.T) {
	client := &exportAPIClient{responses: map[string]string{
		"api.deezer.com/playlist/123":                            `{"title":"Night Mix","picture_xl":"https://e-cdns-images.dzcdn.net/images/playlist/x/1000x1000.jpg","nb_tracks":150}`,
		"api.deezer.com/playlist/123/tracks?index=0&limit=100":   deezerPage(1, 100, true),
		"api.deezer.com/playlist/123/tracks?index=100&limit=100": deezerPage(101, 50, false),
		"api.deezer.com/album/5":                                 `{"title":"Discovery","artist":{"name":"Daft Punk"},"cover_xl":"https://e-cdns-images.dzcdn.net/images/cover/y/1000x1000.jpg"}`,
		"api.deezer.com/track/9":                                 `{"error":{"type":"DataException","message":"no data","code":800}}`,
	}}
	client.install(t)
	result, handled, err := musicServiceTracklist(context.Background(), "https://www.deezer.com/en/playlist/123", true)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	lines := result.lines()
	if result.Name != "Night Mix" || !result.Playlist || len(lines) != 150 || lines[0] != "Artist 1 - Song 1" || lines[149] != "Artist 150 - Song 150" || result.Total != 0 {
		t.Fatalf("result name=%q tracks=%d total=%d first=%q", result.Name, len(lines), result.Total, lines[0])
	}
	if !strings.HasSuffix(result.CoverURL, "/1000x1000.jpg") {
		t.Fatalf("cover=%q", result.CoverURL)
	}
	album, handled, err := musicServiceTracklist(context.Background(), "https://www.deezer.com/album/5", false)
	if err != nil || !handled || album.Name != "Daft Punk - Discovery" || len(album.Tracks) != 0 || album.CoverURL == "" {
		t.Fatalf("album=%#v handled=%v err=%v", album, handled, err)
	}
	if _, _, err := musicServiceTracklist(context.Background(), "https://www.deezer.com/track/9", true); err == nil || !strings.Contains(err.Error(), "no data") {
		t.Fatalf("deezer error body must surface: %v", err)
	}
	for _, requested := range client.requested {
		if !strings.HasPrefix(requested, deezerAPI+"/") {
			t.Fatalf("unexpected request %q", requested)
		}
	}
	if _, handled, _ := musicServiceTracklist(context.Background(), "https://www.deezer.com/en/artist/27", true); handled {
		t.Fatal("unsupported Deezer paths must fall back to yt-dlp")
	}
}

func TestYandexTargetsValidatePaths(t *testing.T) {
	tests := []struct {
		path       string
		withTracks bool
		endpoint   string
		kind       string
	}{
		{"/album/1/track/2", true, "/tracks/2", "track"},
		{"/track/7", true, "/tracks/7", "track"},
		{"/album/1", true, "/albums/1/with-tracks", "album"},
		{"/album/1", false, "/albums/1", "album"},
		{"/users/music-blog/playlists/1044", true, "/users/music-blog/playlists/1044", "playlist"},
		{"/playlists/lk.3e2a0b5c-3b4d", true, "/playlist/lk.3e2a0b5c-3b4d", "playlist"},
		{"/users/../playlists/1", true, "", ""},
		{"/playlists/..", true, "", ""},
		{"/album/x1", true, "", ""},
		{"/artist/1", true, "", ""},
	}
	for _, test := range tests {
		segments := strings.FieldsFunc(test.path, func(r rune) bool { return r == '/' })
		endpoint, kind, ok := yandexTarget(segments, test.withTracks)
		if test.endpoint == "" {
			if ok {
				t.Errorf("%s must be rejected, got %q", test.path, endpoint)
			}
			continue
		}
		if !ok || endpoint != yandexMusicAPI+test.endpoint || kind != test.kind {
			t.Errorf("%s: endpoint=%q kind=%q ok=%v", test.path, endpoint, kind, ok)
		}
	}
}

func TestYandexTracklistParsesPlaylistsAlbumsAndTracks(t *testing.T) {
	client := &exportAPIClient{responses: map[string]string{
		"api.music.yandex.net/users/listener/playlists/3": `{"result":{"title":"Mix","trackCount":3,"cover":{"uri":"avatars.yandex.net/get-music-content/p/%%"},"tracks":[
			{"track":{"title":"A","version":"Remix","artists":[{"name":"X"},{"name":"Y"}]}},
			{"track":null},
			{"track":{"title":"B","artists":[{"name":"Z"}]}}]}}`,
		"api.music.yandex.net/albums/10/with-tracks": `{"result":{"title":"Discovery","artists":[{"name":"Daft Punk"}],"coverUri":"avatars.yandex.net/get-music-content/a/%%","trackCount":2,
			"volumes":[[{"title":"One More Time","artists":[{"name":"Daft Punk"}]}],[{"title":"Aerodynamic","artists":[{"name":"Daft Punk"}]}]]}}`,
		"api.music.yandex.net/tracks/20": `{"result":[{"title":"Get Lucky","artists":[{"name":"Daft Punk"},{"name":"Pharrell Williams"}],"coverUri":"avatars.yandex.net/get-music-content/t/%%"}]}`,
	}}
	client.install(t)
	playlist, handled, err := musicServiceTracklist(context.Background(), "https://music.yandex.ru/users/listener/playlists/3", true)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if playlist.Name != "Mix" || !reflect.DeepEqual(playlist.lines(), []string{"X, Y - A (Remix)", "Z - B"}) || playlist.Total != 3 {
		t.Fatalf("playlist=%#v", playlist)
	}
	if playlist.CoverURL != "https://avatars.yandex.net/get-music-content/p/1000x1000" {
		t.Fatalf("cover=%q", playlist.CoverURL)
	}
	album, _, err := musicServiceTracklist(context.Background(), "https://music.yandex.com/album/10", true)
	if err != nil || album.Name != "Daft Punk - Discovery" || !reflect.DeepEqual(album.lines(), []string{"Daft Punk - One More Time", "Daft Punk - Aerodynamic"}) || album.Total != 0 {
		t.Fatalf("album=%#v err=%v", album, err)
	}
	track, _, err := musicServiceTracklist(context.Background(), "https://music.yandex.ru/album/10/track/20", true)
	if err != nil || track.Playlist || track.Name != "Daft Punk, Pharrell Williams - Get Lucky" {
		t.Fatalf("track=%#v err=%v", track, err)
	}
	if _, _, err := musicServiceTracklist(context.Background(), "https://music.yandex.ru/track/404", true); err == nil {
		t.Fatal("an HTTP error must be returned")
	}
}

func TestStreamingOnlyCollectionsAreUnsupported(t *testing.T) {
	client := &exportAPIClient{responses: map[string]string{}}
	client.install(t)
	for _, link := range []string{"https://open.spotify.com/playlist/37i9dQZF1DX", "https://music.apple.com/us/album/discovery/697194953", "https://tidal.com/browse/album/1"} {
		if _, handled, err := musicServiceTracklist(context.Background(), link, true); !handled || !errors.Is(err, errExportUnsupported) {
			t.Errorf("%s: handled=%v err=%v", link, handled, err)
		}
	}
	application := &app{}
	if _, err := application.coverFor(context.Background(), "https://open.spotify.com/track/abc"); !errors.Is(err, errExportUnsupported) {
		t.Fatalf("spotify cover err=%v", err)
	}
	if len(client.requested) != 0 {
		t.Fatalf("collections must be refused before any request: %q", client.requested)
	}
	if !streamingTrackLink("music.apple.com", mustParseURL(t, "https://music.apple.com/us/album/x/1?i=2"), []string{"us", "album", "x", "1"}) {
		t.Fatal("apple music ?i= links are single tracks")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			picture.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestDownloadCoverImageAllowsOnlyArtworkHosts(t *testing.T) {
	picture := testPNG(t, 4, 4)
	var mu sync.Mutex
	var requested []string
	old := makeResolverClient
	makeResolverClient = func() httpDoer {
		return handlerClient{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requested = append(requested, r.URL.String())
			mu.Unlock()
			if strings.HasSuffix(r.URL.Path, ".png") {
				_, _ = w.Write(picture)
				return
			}
			fmt.Fprint(w, "<html>not an image</html>")
		})}
	}
	defer func() { makeResolverClient = old }()
	for _, rejected := range []string{
		"http://e-cdns-images.dzcdn.net/a.png",
		"https://evil.example/a.png",
		"https://dzcdn.net.evil.example/a.png",
		"https://127.0.0.1/a.png",
	} {
		if _, err := downloadCoverImage(context.Background(), rejected, "x"); err == nil {
			t.Errorf("%s must be rejected", rejected)
		}
	}
	if len(requested) != 0 {
		t.Fatalf("rejected covers must not be requested: %q", requested)
	}
	cover, err := downloadCoverImage(context.Background(), "https://e-cdns-images.dzcdn.net/images/cover/a.png", "Daft Punk - Discovery")
	if err != nil || cover.Ext != "png" || !bytes.Equal(cover.Data, picture) || cover.Name != "Daft Punk - Discovery" {
		t.Fatalf("cover=%#v err=%v", cover, err)
	}
	if _, err := downloadCoverImage(context.Background(), "https://avatars.yandex.net/get-music-content/page.html", "x"); err == nil {
		t.Fatal("non-image bodies must be refused")
	}
}

func TestCoverCandidatesPreferTheLargestArtwork(t *testing.T) {
	for raw, want := range map[string][]string{
		"https://e-cdns-images.dzcdn.net/images/cover/y/1000x1000-000000-80-0-0.jpg": {"https://e-cdns-images.dzcdn.net/images/cover/y/1800x1800-000000-100-0-0.jpg", "https://e-cdns-images.dzcdn.net/images/cover/y/1000x1000-000000-80-0-0.jpg"},
		"https://avatars.yandex.net/get-music-content/a/1000x1000":                   {"https://avatars.yandex.net/get-music-content/a/orig", "https://avatars.yandex.net/get-music-content/a/1000x1000"},
		"https://example.com/cover/1000x1000.jpg":                                    {"https://example.com/cover/1000x1000.jpg"},
	} {
		if got := coverCandidates(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("coverCandidates(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCoverForFallsBackToTheAPISize(t *testing.T) {
	small, large := string(testPNG(t, 4, 4)), string(testPNG(t, 8, 8))
	album := `{"title":"Discovery","artist":{"name":"Daft Punk"},"cover_xl":"https://e-cdns-images.dzcdn.net/images/cover/y/1000x1000-000000-80-0-0.jpg"}`
	for name, test := range map[string]struct {
		responses map[string]string
		want      string
	}{
		"largest": {map[string]string{"e-cdns-images.dzcdn.net/images/cover/y/1800x1800-000000-100-0-0.jpg": large, "e-cdns-images.dzcdn.net/images/cover/y/1000x1000-000000-80-0-0.jpg": small}, large},
		"missing": {map[string]string{"e-cdns-images.dzcdn.net/images/cover/y/1000x1000-000000-80-0-0.jpg": small}, small},
	} {
		t.Run(name, func(t *testing.T) {
			test.responses["api.deezer.com/album/5"] = album
			client := &exportAPIClient{responses: test.responses}
			client.install(t)
			cover, err := (&app{}).coverFor(context.Background(), "https://www.deezer.com/album/5")
			if err != nil || string(cover.Data) != test.want || cover.Name != "Daft Punk - Discovery" {
				t.Fatalf("cover=%d bytes %q err=%v", len(cover.Data), cover.Name, err)
			}
		})
	}
}

func TestPreviewKeyboardsOfferExportAndCover(t *testing.T) {
	for name, markup := range map[string]*tgbotapi.InlineKeyboardMarkup{
		"format": formatKeyboard("abc", "en"),
		"range":  rangeKeyboard("abc", 30, 75, "en"),
	} {
		var export, cover, cancelAfter bool
		for _, row := range markup.InlineKeyboard {
			for _, button := range row {
				data := ""
				if button.CallbackData != nil {
					data = *button.CallbackData
				}
				switch data {
				case "export:abc":
					export = true
				case "cover:abc":
					cover = true
				case "cancel:abc":
					cancelAfter = export && cover
				}
				if len(data) > 64 {
					t.Errorf("%s: callback data too long: %q", name, data)
				}
			}
		}
		if !export || !cover || !cancelAfter {
			t.Errorf("%s keyboard: export=%v cover=%v cancel last=%v", name, export, cover, cancelAfter)
		}
	}
}

func TestFetchCoverPicksSquareArtAndCropsTopicThumbnails(t *testing.T) {
	ffmpeg, ffmpegErr := exec.LookPath("ffmpeg")
	tests := []struct {
		name       string
		files      map[string][]byte
		json       string
		needFFmpeg bool
		wantName   string
		wantExt    string
		wantSize   [2]int
	}{
		{
			name:     "square album art wins",
			files:    map[string][]byte{"track.png": testPNG(t, 160, 90), "playlist.png": testPNG(t, 64, 64)},
			json:     `{"_type":"playlist","title":"Album - Discovery","uploader":"Daft Punk - Topic","entries":[{"title":"One More Time","channel":"Daft Punk - Topic"}]}`,
			wantName: "Daft Punk - Discovery",
			wantExt:  "png",
			wantSize: [2]int{64, 64},
		},
		{
			name:       "topic thumbnail is cropped",
			files:      map[string][]byte{"track.png": testPNG(t, 160, 90)},
			json:       `{"title":"Get Lucky","track":"Get Lucky","artist":"Daft Punk","channel":"Daft Punk - Topic"}`,
			needFFmpeg: true,
			wantName:   "Daft Punk - Get Lucky",
			wantExt:    "png",
			wantSize:   [2]int{90, 90},
		},
		{
			name:     "plain upload keeps its frame",
			files:    map[string][]byte{"track.png": testPNG(t, 160, 90)},
			json:     `{"title":"Artist - Song (Official Video)","channel":"Artist VEVO"}`,
			wantName: "Artist - Song",
			wantExt:  "png",
			wantSize: [2]int{160, 90},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.needFFmpeg && ffmpegErr != nil {
				t.Skip("ffmpeg is not installed")
			}
			dir := t.TempDir()
			fixtures := filepath.Join(dir, "fixtures")
			if err := os.MkdirAll(fixtures, 0o700); err != nil {
				t.Fatal(err)
			}
			copies := ""
			for name, data := range test.files {
				if err := os.WriteFile(filepath.Join(fixtures, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
				copies += `cp "` + filepath.Join(fixtures, name) + `" "$dir/` + name + `"` + "\n"
			}
			bin := filepath.Join(dir, "fake-yt-dlp")
			script := "#!/bin/sh\n" +
				`case " $* " in *" --write-thumbnail "*) ;; *) exit 3 ;; esac` + "\n" +
				`dir=''` + "\n" +
				`while [ "$#" -gt 0 ]; do case "$1" in --paths) dir="$2"; shift 2 ;; *) shift ;; esac; done` + "\n" +
				copies +
				"printf '%s' '" + test.json + "'\n"
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			downloads := filepath.Join(dir, "downloads")
			d := &downloader{bin: bin, ffmpegBin: ffmpeg, downloadDir: downloads}
			cover, err := d.fetchCover(context.Background(), "https://www.youtube.com/watch?v=abc")
			if err != nil {
				t.Fatal(err)
			}
			config, format, err := image.DecodeConfig(bytes.NewReader(cover.Data))
			if err != nil {
				t.Fatal(err)
			}
			if cover.Name != test.wantName || cover.Ext != test.wantExt || [2]int{config.Width, config.Height} != test.wantSize {
				t.Fatalf("cover name=%q ext=%q format=%s size=%dx%d", cover.Name, cover.Ext, format, config.Width, config.Height)
			}
			if entries, _ := os.ReadDir(downloads); len(entries) != 0 {
				t.Fatalf("session directory must be removed, found %d entries", len(entries))
			}
		})
	}
}

// exportTelegram records messages and documents, including captions and file contents.
type exportTelegram struct {
	mu    sync.Mutex
	calls []exportCall
}

type exportCall struct {
	method, chatID, text, file, fileName, markup string
}

func (c *exportTelegram) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		method := filepath.Base(r.URL.Path)
		call := exportCall{method: method}
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"testbot"}}`)
			return
		case "sendDocument":
			_ = r.ParseMultipartForm(1 << 20)
			call.chatID, call.text = r.FormValue("chat_id"), r.FormValue("caption")
			if file, header, err := r.FormFile("document"); err == nil {
				data, _ := io.ReadAll(file)
				file.Close()
				call.file, call.fileName = string(data), header.Filename
			}
		case "sendMessage", "editMessageText":
			_ = r.ParseForm()
			call.chatID, call.text, call.markup = r.FormValue("chat_id"), r.FormValue("text"), r.FormValue("reply_markup")
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		c.mu.Lock()
		c.calls = append(c.calls, call)
		c.mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":2,"date":1,"chat":{"id":10,"type":"private"},"text":"x"}}`)
	})
}

func (c *exportTelegram) snapshot() []exportCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]exportCall(nil), c.calls...)
	c.calls = nil
	return out
}

func newExportTestApp(t *testing.T, telegram *exportTelegram) *app {
	t.Helper()
	application := newCookieTestApp(t, &cookieTestTelegram{}, nil, t.TempDir())
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: telegram.handler()})
	if err != nil {
		t.Fatal(err)
	}
	application.bot = bot
	return application
}

func TestExportCallbackSendsTracklistFileAndKeepsLink(t *testing.T) {
	telegram := &exportTelegram{}
	application := newExportTestApp(t, telegram)
	preview := mediaPreview{Title: "Album - Discovery", Artist: "Daft Punk - Topic", IsPlaylist: true, TrackCount: 3,
		Tracks: []exportTrack{{"Daft Punk", "One More Time"}, {"Daft Punk", "Aerodynamic"}, {"Daft Punk", "Digital Love"}}}
	key, err := application.storeURL(pendingURL{URL: "https://music.youtube.com/playlist?list=OLAK", ChatID: 10, UserID: 10, Preview: preview})
	if err != nil {
		t.Fatal(err)
	}
	callback := &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 10}, Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 10}}, Data: "export:" + key}
	application.handleExportCallback(callback)
	calls := telegram.snapshot()
	if len(calls) != 1 || calls[0].method != "sendDocument" {
		t.Fatalf("calls=%#v", calls)
	}
	if calls[0].file != "Daft Punk - One More Time\nDaft Punk - Aerodynamic\nDaft Punk - Digital Love\n" || calls[0].fileName != "Daft Punk - Discovery.txt" {
		t.Fatalf("file %q=%q", calls[0].fileName, calls[0].file)
	}
	if !strings.Contains(calls[0].text, "Daft Punk - Discovery") || !strings.Contains(calls[0].text, "3") || !strings.Contains(calls[0].text, "@yamusic_export_bot") {
		t.Fatalf("caption=%q", calls[0].text)
	}
	if _, ok := application.getURL(key, 10, 10); !ok {
		t.Fatal("export must keep the link available for downloading")
	}
	callback.From = &tgbotapi.User{ID: 11}
	application.handleExportCallback(callback)
	if calls := telegram.snapshot(); len(calls) != 1 || calls[0].text != tr("action_unavailable", "en") {
		t.Fatalf("another user's button must be refused: %#v", calls)
	}

	single, err := application.storeURL(pendingURL{URL: "https://youtu.be/x", ChatID: 10, UserID: 10, Preview: mediaPreview{Title: "Get Lucky <live>", Artist: "Daft Punk"}})
	if err != nil {
		t.Fatal(err)
	}
	application.handleExportCallback(&tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 10}, Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 10}}, Data: "export:" + single})
	calls = telegram.snapshot()
	if len(calls) != 1 || calls[0].method != "sendMessage" || !strings.Contains(calls[0].text, "<code>Daft Punk - Get Lucky &lt;live&gt;</code>") {
		t.Fatalf("single track export=%#v", calls)
	}
}

func TestExportCommandReadsReplyAndExplainsUnsupportedLinks(t *testing.T) {
	telegram := &exportTelegram{}
	application := newExportTestApp(t, telegram)
	reports := newErrorReporter(-1001)
	application.errorReports = reports
	command := func(text string, reply *tgbotapi.Message) *tgbotapi.Message {
		name := strings.Fields(text)[0]
		return &tgbotapi.Message{
			Text:           text,
			From:           &tgbotapi.User{ID: 10},
			Chat:           &tgbotapi.Chat{ID: 10},
			Entities:       []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: len(name)}},
			ReplyToMessage: reply,
		}
	}
	application.handleExportCommand(command("/export", nil), "en", false)
	if calls := telegram.snapshot(); len(calls) != 1 || calls[0].text != tr("export_usage", "en") {
		t.Fatalf("usage=%#v", calls)
	}
	reply := &tgbotapi.Message{Text: "listen https://open.spotify.com/playlist/37i9dQZF1DX"}
	application.handleExportCommand(command("/export", reply), "en", false)
	calls := telegram.snapshot()
	if len(calls) != 2 || calls[0].text != tr("export_working", "en") || calls[1].method != "editMessageText" || calls[1].text != tr("export_unsupported", "en") {
		t.Fatalf("unsupported playlist=%#v", calls)
	}
	application.handleExportCommand(command("/cover https://open.spotify.com/track/abc", nil), "en", true)
	calls = telegram.snapshot()
	if len(calls) != 2 || calls[1].text != tr("cover_unsupported", "en") {
		t.Fatalf("unsupported cover=%#v", calls)
	}
	if len(reports.queue) != 0 {
		t.Fatal("unsupported links are not operator errors")
	}
}

func TestExportFailureIsReported(t *testing.T) {
	telegram := &exportTelegram{}
	application := newExportTestApp(t, telegram)
	reports := newErrorReporter(-1001)
	application.errorReports = reports
	application.runExportJob(10, 10, "https://www.youtube.com/playlist?list=PL1", "en", false)
	calls := telegram.snapshot()
	if len(calls) != 2 || !strings.HasPrefix(calls[1].text, strings.SplitN(tr("export_error", "en"), "<code>", 2)[0]) {
		t.Fatalf("calls=%#v", calls)
	}
	if len(reports.queue) != 1 {
		t.Fatalf("a real failure must reach the operator chat, queued=%d", len(reports.queue))
	}
	entry := <-reports.queue
	if entry.report.Stage != "export" || entry.report.URL != "https://www.youtube.com/playlist?list=PL1" || entry.report.UserID != 10 {
		t.Fatalf("report=%#v", entry.report)
	}
}
