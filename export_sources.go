package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	deezerAPI      = "https://api.deezer.com"
	yandexMusicAPI = "https://api.music.yandex.net"
	// deezerPageSize is the largest page Deezer returns for playlist and album tracks.
	deezerPageSize = 100
	// maxExportResponse bounds one API response; a 1000-track Yandex playlist is about 3 MiB.
	maxExportResponse = 8 << 20
)

var (
	exportNumericID = regexp.MustCompile(`^[0-9]{1,20}$`)
	// exportSlug is a Yandex login or playlist UUID; a leading dot is refused so that "." and
	// ".." cannot climb the API path.
	exportSlug = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,99}$`)
	// errMusicServiceBlocked marks HTTP 451: the service refuses requests from the server's
	// country (Yandex Music does so outside the CIS) and returns no metadata at all.
	errMusicServiceBlocked = errors.New("сервис недоступен из страны сервера")
)

// musicServiceTracklist exports Deezer and Yandex Music links through their public APIs and
// single tracks of Spotify, Apple Music and Tidal through the link metadata. handled is false
// for links (and unrecognised paths) that yt-dlp should inspect instead. Without withTracks
// only the name and the cover are fetched.
func musicServiceTracklist(ctx context.Context, rawURL string, withTracks bool) (exportResult, bool, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return exportResult{}, false, nil
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	segments := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
	switch {
	case host == "link.deezer.com":
		if final := expandMusicShortLink(ctx, rawURL); final != rawURL {
			return musicServiceTracklist(ctx, final, withTracks)
		}
	case hostWithin(host, "deezer.com"):
		if kind, id, ok := deezerTarget(segments); ok {
			result, err := deezerTracklist(ctx, kind, id, withTracks)
			return result, true, err
		}
	case strings.HasPrefix(host, "music.yandex."):
		if endpoint, kind, ok := yandexTarget(segments, withTracks); ok {
			result, err := yandexTracklist(ctx, endpoint, kind)
			return result, true, err
		}
	case streamingOnlyLink(rawURL):
		if !streamingTrackLink(host, parsed, segments) {
			return exportResult{}, true, errExportUnsupported
		}
		title, artist, err := resolveLinkMetadata(ctx, rawURL)
		if err != nil {
			return exportResult{}, true, err
		}
		if strings.HasPrefix(artist, "http") {
			artist = ""
		}
		track := exportTrack{Artist: artist, Title: title}
		return exportResult{Name: track.line(), Tracks: []exportTrack{track}}, true, nil
	}
	return exportResult{}, false, nil
}

func hostWithin(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// streamingOnlyLink reports services that yt-dlp cannot read: only their public link metadata
// is available, so collections cannot be exported and covers are not fetched.
func streamingOnlyLink(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	return hostWithin(host, "spotify.com") || hostWithin(host, "music.apple.com") || hostWithin(host, "tidal.com")
}

func streamingTrackLink(host string, parsed *url.URL, segments []string) bool {
	for _, segment := range segments {
		if segment == "track" || (hostWithin(host, "music.apple.com") && segment == "song") {
			return true
		}
	}
	return hostWithin(host, "music.apple.com") && parsed.Query().Get("i") != ""
}

// musicCollectionKind names the playlist, album or artist page behind a music-service link
// ("playlist", "album", "artist"). It is empty for tracks and unrecognised paths, which are
// searched on YouTube as one track.
func musicCollectionKind(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || !requiresMusicResolution(rawURL) {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	segments := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
	if strings.HasPrefix(host, "music.yandex.") {
		switch {
		case len(segments) == 0:
		case segments[0] == "album" && (len(segments) < 3 || segments[2] != "track"):
			return "album"
		case segments[0] == "playlists" || (len(segments) >= 3 && segments[0] == "users" && segments[2] == "playlists"):
			return "playlist"
		case segments[0] == "artist":
			return "artist"
		}
		return ""
	}
	if streamingTrackLink(host, parsed, segments) {
		return ""
	}
	for _, segment := range segments {
		switch segment {
		case "playlist", "mix":
			return "playlist"
		case "album":
			return "album"
		case "artist":
			return "artist"
		}
	}
	return ""
}

// musicServiceName names the service of a metadata link as it follows a noun, so Yandex Music
// is genitive in Russian: "плейлист Яндекс Музыки".
func musicServiceName(rawURL, lang string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	switch {
	case hostWithin(host, "spotify.com"):
		return "Spotify"
	case hostWithin(host, "music.apple.com"):
		return "Apple Music"
	case hostWithin(host, "deezer.com"):
		return "Deezer"
	case hostWithin(host, "tidal.com"):
		return "Tidal"
	case strings.HasPrefix(host, "music.yandex.") && lang == "ru":
		return "Яндекс Музыки"
	case strings.HasPrefix(host, "music.yandex."):
		return "Yandex Music"
	}
	return host
}

// expandMusicShortLink follows a Deezer share link (link.deezer.com/s/...) to the release it
// names, so that playlists and albums are recognised; other links are returned unchanged.
func expandMusicShortLink(ctx context.Context, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "link.deezer.com") {
		return rawURL
	}
	if final := resolveShortLink(ctx, rawURL); final != "" && !strings.Contains(final, "link.deezer.com") {
		return final
	}
	return rawURL
}

// resolveShortLink follows the redirects of a short share link through the resolver client,
// whose redirect policy keeps them on supported hosts.
func resolveShortLink(ctx context.Context, rawURL string) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	response, err := makeResolverClient().Do(request)
	if err != nil {
		return ""
	}
	response.Body.Close()
	if response.Request == nil || response.Request.URL == nil {
		return ""
	}
	return response.Request.URL.String()
}

// fetchExportJSON decodes one bounded JSON response of a fixed music API endpoint.
func fetchExportJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := makeResolverClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnavailableForLegalReasons {
		return fmt.Errorf("%w (HTTP 451)", errMusicServiceBlocked)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("сервис ответил HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxExportResponse)).Decode(target)
}

// deezerTarget finds "album|playlist|track/<id>" in a Deezer path such as /en/album/302127.
func deezerTarget(segments []string) (string, string, bool) {
	for i := 0; i+1 < len(segments); i++ {
		switch segments[i] {
		case "album", "playlist", "track":
			if exportNumericID.MatchString(segments[i+1]) {
				return segments[i], segments[i+1], true
			}
		}
	}
	return "", "", false
}

type deezerError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *deezerError) Error() string {
	return "Deezer: " + firstNonEmpty(e.Message, e.Type, "ошибка API")
}

type deezerTrack struct {
	Title  string `json:"title"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		CoverXL string `json:"cover_xl"`
	} `json:"album"`
}

func deezerTracklist(ctx context.Context, kind, id string, withTracks bool) (exportResult, error) {
	var object struct {
		deezerTrack
		CoverXL   string       `json:"cover_xl"`
		PictureXL string       `json:"picture_xl"`
		NbTracks  int          `json:"nb_tracks"`
		Error     *deezerError `json:"error"`
	}
	if err := fetchExportJSON(ctx, deezerAPI+"/"+kind+"/"+id, &object); err != nil {
		return exportResult{}, err
	}
	if object.Error != nil {
		return exportResult{}, object.Error
	}
	if kind == "track" {
		track := exportTrack{Artist: object.Artist.Name, Title: object.Title}
		return exportResult{Name: track.line(), Tracks: []exportTrack{track}, CoverURL: object.Album.CoverXL}, nil
	}
	result := exportResult{Name: object.Title, Playlist: true, CoverURL: firstNonEmpty(object.CoverXL, object.PictureXL)}
	if kind == "album" {
		result.Name = exportTrack{Artist: object.Artist.Name, Title: object.Title}.line()
	}
	if !withTracks {
		return result, nil
	}
	total := object.NbTracks
	for index := 0; len(result.Tracks) < maxExportTracks; {
		var page struct {
			Data  []deezerTrack `json:"data"`
			Total int           `json:"total"`
			Next  string        `json:"next"`
			Error *deezerError  `json:"error"`
		}
		endpoint := deezerAPI + "/" + kind + "/" + id + "/tracks?index=" + strconv.Itoa(index) + "&limit=" + strconv.Itoa(deezerPageSize)
		if err := fetchExportJSON(ctx, endpoint, &page); err != nil {
			return exportResult{}, err
		}
		if page.Error != nil {
			return exportResult{}, page.Error
		}
		for _, track := range page.Data[:min(len(page.Data), maxExportTracks-len(result.Tracks))] {
			result.Tracks = append(result.Tracks, exportTrack{Artist: track.Artist.Name, Title: track.Title})
		}
		total = max(total, page.Total)
		if len(page.Data) == 0 || page.Next == "" {
			break
		}
		index += len(page.Data)
	}
	if total > len(result.Tracks) {
		result.Total = total
	}
	return result, nil
}

// yandexTarget maps a Yandex Music path to its public API endpoint: albums, tracks, user
// playlists (/users/<login>/playlists/<kind>) and shared playlists (/playlists/<uuid>).
func yandexTarget(segments []string, withTracks bool) (string, string, bool) {
	switch {
	case len(segments) >= 4 && segments[0] == "album" && segments[2] == "track" && exportNumericID.MatchString(segments[3]):
		return yandexMusicAPI + "/tracks/" + segments[3], "track", true
	case len(segments) >= 2 && segments[0] == "track" && exportNumericID.MatchString(segments[1]):
		return yandexMusicAPI + "/tracks/" + segments[1], "track", true
	case len(segments) >= 2 && segments[0] == "album" && exportNumericID.MatchString(segments[1]):
		if !withTracks {
			return yandexMusicAPI + "/albums/" + segments[1], "album", true
		}
		return yandexMusicAPI + "/albums/" + segments[1] + "/with-tracks", "album", true
	case len(segments) >= 4 && segments[0] == "users" && segments[2] == "playlists" && exportSlug.MatchString(segments[1]) && exportNumericID.MatchString(segments[3]):
		return yandexMusicAPI + "/users/" + url.PathEscape(segments[1]) + "/playlists/" + segments[3], "playlist", true
	case len(segments) >= 2 && segments[0] == "playlists" && exportSlug.MatchString(segments[1]):
		return yandexMusicAPI + "/playlist/" + url.PathEscape(segments[1]), "playlist", true
	}
	return "", "", false
}

type yandexArtist struct {
	Name string `json:"name"`
}

type yandexTrack struct {
	Title    string         `json:"title"`
	Version  string         `json:"version"`
	Artists  []yandexArtist `json:"artists"`
	CoverURI string         `json:"coverUri"`
}

// export joins every credited artist and appends the version, as Yandex Music shows it:
// "Artist1, Artist2 - Title (Remix)".
func (t yandexTrack) export() exportTrack {
	title := strings.TrimSpace(t.Title)
	if version := strings.TrimSpace(t.Version); version != "" && title != "" {
		title += " (" + version + ")"
	}
	return exportTrack{Artist: yandexArtists(t.Artists), Title: title}
}

func yandexArtists(artists []yandexArtist) string {
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		if name := strings.TrimSpace(artist.Name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}

// yandexCover expands a Yandex "avatars.yandex.net/.../%%" template to a 1000×1000 image URL.
func yandexCover(uri string) string {
	uri = strings.TrimPrefix(strings.TrimSpace(uri), "//")
	if uri == "" {
		return ""
	}
	return "https://" + strings.ReplaceAll(uri, "%%", "1000x1000")
}

func yandexTracklist(ctx context.Context, endpoint, kind string) (exportResult, error) {
	switch kind {
	case "track":
		var payload struct {
			Result []yandexTrack `json:"result"`
		}
		if err := fetchExportJSON(ctx, endpoint, &payload); err != nil {
			return exportResult{}, err
		}
		if len(payload.Result) == 0 {
			return exportResult{}, errExportEmpty
		}
		track := payload.Result[0].export()
		return exportResult{Name: track.line(), Tracks: []exportTrack{track}, CoverURL: yandexCover(payload.Result[0].CoverURI)}, nil
	case "album":
		var payload struct {
			Result struct {
				yandexTrack
				TrackCount int             `json:"trackCount"`
				Volumes    [][]yandexTrack `json:"volumes"`
			} `json:"result"`
		}
		if err := fetchExportJSON(ctx, endpoint, &payload); err != nil {
			return exportResult{}, err
		}
		album := payload.Result
		result := exportResult{Name: album.export().line(), Playlist: true, CoverURL: yandexCover(album.CoverURI)}
		for _, volume := range album.Volumes {
			for _, track := range volume {
				if len(result.Tracks) < maxExportTracks {
					result.Tracks = append(result.Tracks, track.export())
				}
			}
		}
		if album.TrackCount > len(result.Tracks) && len(album.Volumes) > 0 {
			result.Total = album.TrackCount
		}
		return result, nil
	default:
		var payload struct {
			Result struct {
				Title      string `json:"title"`
				TrackCount int    `json:"trackCount"`
				OGImage    string `json:"ogImage"`
				Cover      struct {
					URI string `json:"uri"`
				} `json:"cover"`
				Tracks []struct {
					Track *yandexTrack `json:"track"`
				} `json:"tracks"`
			} `json:"result"`
		}
		if err := fetchExportJSON(ctx, endpoint, &payload); err != nil {
			return exportResult{}, err
		}
		playlist := payload.Result
		result := exportResult{Name: strings.TrimSpace(playlist.Title), Playlist: true, CoverURL: yandexCover(firstNonEmpty(playlist.Cover.URI, playlist.OGImage))}
		for _, item := range playlist.Tracks {
			if item.Track != nil && len(result.Tracks) < maxExportTracks {
				result.Tracks = append(result.Tracks, item.Track.export())
			}
		}
		if playlist.TrackCount > len(result.Tracks) {
			result.Total = playlist.TrackCount
		}
		return result, nil
	}
}
