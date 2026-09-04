package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	octaveAPIBase       = "https://api.octavestreaming.com"
	octaveMusicHost     = "music.octavestreaming.com"
	octaveAPIHost       = "api.octavestreaming.com"
	octaveResponseLimit = 8 << 20
)

type octaveArtist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type octaveAlbumSummary struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	CoverSmall  string `json:"cover_small"`
	CoverMedium string `json:"cover_medium"`
	CoverBig    string `json:"cover_big"`
	CoverXL     string `json:"cover_xl"`
}

func (a octaveAlbumSummary) coverURL() string {
	return firstNonEmpty(a.CoverXL, a.CoverBig, a.CoverMedium, a.CoverSmall)
}

type octaveTrack struct {
	ID         string             `json:"id"`
	Title      string             `json:"title"`
	Artist     octaveArtist       `json:"artist"`
	Album      octaveAlbumSummary `json:"album"`
	Duration   int                `json:"duration"`
	PreviewURL string             `json:"previewUrl"`
	Explicit   bool               `json:"explicit"`
	Rank       int                `json:"rank"`
	Position   int                `json:"-"`
	Genre      string             `json:"-"`
	Year       string             `json:"-"`
	Covers     octaveAlbumSummary `json:"-"`
}

func (t octaveTrack) coverURL() string {
	if cover := t.Album.coverURL(); cover != "" {
		return cover
	}
	return t.Covers.coverURL()
}

type octaveAlbum struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Artist      octaveArtist  `json:"artist"`
	CoverSmall  string        `json:"cover_small"`
	CoverMedium string        `json:"cover_medium"`
	CoverBig    string        `json:"cover_big"`
	CoverXL     string        `json:"cover_xl"`
	ReleaseDate string        `json:"releaseDate"`
	NBTracks    int           `json:"nbTracks"`
	RecordType  string        `json:"recordType"`
	Tracks      []octaveTrack `json:"tracks"`
}

func (a octaveAlbum) summary() octaveAlbumSummary {
	return octaveAlbumSummary{
		ID: a.ID, Title: a.Title, CoverSmall: a.CoverSmall, CoverMedium: a.CoverMedium,
		CoverBig: a.CoverBig, CoverXL: a.CoverXL,
	}
}

func (a *octaveAlbum) normalizeTracks() {
	cover := a.summary()
	year := ""
	if len(a.ReleaseDate) >= 4 {
		year = a.ReleaseDate[:4]
	}
	for i := range a.Tracks {
		a.Tracks[i].Position = i + 1
		a.Tracks[i].Year = year
		if a.Tracks[i].Artist.ID == "" {
			a.Tracks[i].Artist.ID = a.Artist.ID
		}
		if a.Tracks[i].Artist.Name == "" {
			a.Tracks[i].Artist.Name = a.Artist.Name
		}
		if a.Tracks[i].Album.ID == "" {
			a.Tracks[i].Album.ID = cover.ID
		}
		if a.Tracks[i].Album.Title == "" {
			a.Tracks[i].Album.Title = cover.Title
		}
		if a.Tracks[i].Album.CoverSmall == "" {
			a.Tracks[i].Album.CoverSmall = cover.CoverSmall
		}
		if a.Tracks[i].Album.CoverMedium == "" {
			a.Tracks[i].Album.CoverMedium = cover.CoverMedium
		}
		if a.Tracks[i].Album.CoverBig == "" {
			a.Tracks[i].Album.CoverBig = cover.CoverBig
		}
		if a.Tracks[i].Album.CoverXL == "" {
			a.Tracks[i].Album.CoverXL = cover.CoverXL
		}
		a.Tracks[i].Covers = cover
	}
}

type octaveReference struct {
	AlbumID string
	TrackID string
}

func parseOctaveURL(raw string) (octaveReference, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Scheme != "https" {
		return octaveReference{}, false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != octaveMusicHost && host != octaveAPIHost {
		return octaveReference{}, false
	}
	cleanPath := path.Clean("/" + strings.TrimSpace(parsed.EscapedPath()))
	parts := strings.Split(strings.Trim(cleanPath, "/"), "/")
	ref := octaveReference{}
	switch {
	case host == octaveMusicHost && len(parts) == 2 && parts[0] == "album":
		ref.AlbumID = parts[1]
		ref.TrackID = parsed.Query().Get("t")
	case host == octaveMusicHost && len(parts) == 2 && parts[0] == "track":
		ref.TrackID = parts[1]
	case host == octaveAPIHost && len(parts) == 3 && parts[0] == "api" && parts[1] == "album":
		ref.AlbumID = parts[2]
		ref.TrackID = parsed.Query().Get("t")
	case host == octaveAPIHost && len(parts) == 3 && parts[0] == "api" && parts[1] == "track":
		ref.TrackID = parts[2]
	default:
		return octaveReference{}, false
	}
	if (ref.AlbumID != "" && !numericOctaveID(ref.AlbumID)) || (ref.TrackID != "" && !numericOctaveID(ref.TrackID)) {
		return octaveReference{}, false
	}
	if ref.AlbumID == "" && ref.TrackID == "" {
		return octaveReference{}, false
	}
	return ref, true
}

func numericOctaveID(value string) bool {
	if value == "" || len(value) > 24 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func octaveTrackURL(track octaveTrack) string {
	if track.Album.ID != "" {
		return "https://" + octaveMusicHost + "/album/" + track.Album.ID + "?t=" + track.ID
	}
	return "https://" + octaveMusicHost + "/track/" + track.ID
}

type octaveClient struct {
	baseURL     string
	client      httpDoer
	coverClient httpDoer

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

func newOctaveClient() *octaveClient {
	return &octaveClient{
		baseURL: octaveAPIBase, client: newOctaveHTTPClient(octaveAPIHost),
		coverClient: newOctaveHTTPClient("cdn-images.dzcdn.net"),
	}
}

func newOctaveHTTPClient(allowedHost string) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 || !strings.EqualFold(request.URL.Hostname(), allowedHost) {
				return errors.New("небезопасный redirect Octave")
			}
			return nil
		},
	}
}

func (c *octaveClient) endpoint(parts ...string) string {
	return strings.TrimRight(c.baseURL, "/") + "/" + strings.Join(parts, "/")
}

func (c *octaveClient) getJSON(ctx context.Context, endpoint string, target any) error {
	if c.client == nil {
		return errors.New("Octave HTTP client не настроен")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("Octave API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Octave API вернул HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, octaveResponseLimit+1))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("разобрать ответ Octave: %w", err)
	}
	return nil
}

func (c *octaveClient) searchTracks(ctx context.Context, query string, limit, offset int) ([]octaveTrack, error) {
	if limit <= 0 || limit > 25 {
		limit = 5
	}
	if offset < 0 {
		offset = 0
	}
	endpoint := c.endpoint("api", "search", "tracks") + "?query=" + url.QueryEscape(strings.TrimSpace(query)) + "&limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(offset)
	var result struct {
		Results []octaveTrack `json:"results"`
	}
	if err := c.getJSON(ctx, endpoint, &result); err != nil {
		return nil, err
	}
	return result.Results, nil
}

func (c *octaveClient) getAlbum(ctx context.Context, id string) (octaveAlbum, error) {
	if !numericOctaveID(id) {
		return octaveAlbum{}, errors.New("некорректный ID альбома Octave")
	}
	var result struct {
		Album octaveAlbum `json:"album"`
	}
	if err := c.getJSON(ctx, c.endpoint("api", "album", id), &result); err != nil {
		return octaveAlbum{}, err
	}
	if result.Album.ID == "" {
		return octaveAlbum{}, errors.New("Octave не вернул альбом")
	}
	result.Album.normalizeTracks()
	return result.Album, nil
}

func (c *octaveClient) playbackToken(ctx context.Context) (string, error) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && now.Before(c.tokenExpiry.Add(-time.Minute)) {
		return c.token, nil
	}
	var result struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if err := c.getJSON(ctx, c.endpoint("api", "playback-token"), &result); err != nil {
		return "", err
	}
	if !strings.HasPrefix(result.Token, "octk_") {
		return "", errors.New("Octave вернул некорректный playback-токен")
	}
	if result.ExpiresIn <= 0 || result.ExpiresIn > 24*60*60 {
		result.ExpiresIn = 2 * 60 * 60
	}
	c.token = result.Token
	c.tokenExpiry = now.Add(time.Duration(result.ExpiresIn) * time.Second)
	return c.token, nil
}

func (c *octaveClient) invalidatePlaybackToken() {
	c.mu.Lock()
	c.token = ""
	c.tokenExpiry = time.Time{}
	c.mu.Unlock()
}

func (c *octaveClient) audioURL(trackID, quality, token string) (string, error) {
	if !numericOctaveID(trackID) || !strings.HasPrefix(token, "octk_") {
		return "", errors.New("некорректные параметры аудио Octave")
	}
	switch quality {
	case "128", "320", "lossless":
	default:
		return "", errors.New("неподдерживаемое качество Octave")
	}
	return c.endpoint("audio", quality) + "?track=" + url.QueryEscape(trackID) + "&k=" + url.QueryEscape(token), nil
}
