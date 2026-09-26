package main

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var (
	metaTagPattern  = regexp.MustCompile(`(?is)<meta\s+[^>]*>`)
	metaAttrPattern = regexp.MustCompile(`(?is)([a-zA-Z_:.-]+)\s*=\s*["']([^"']*)["']`)
	titlePattern    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	// errGenericLinkPage marks a link that opened the service's home page instead of a
	// release: its title is a slogan, and searching it would return a random video.
	errGenericLinkPage = errors.New("ссылка открыла общую страницу сервиса")
)

func (a *app) inspectURL(ctx context.Context, rawURL string) (mediaPreview, error) {
	preview, probeErr := a.downloader.preview(ctx, rawURL)
	if probeErr == nil || !requiresMusicResolution(rawURL) {
		return preview, probeErr
	}
	title, artist, err := resolveLinkMetadata(ctx, rawURL)
	if errors.Is(err, errMusicServiceBlocked) || errors.Is(err, errGenericLinkPage) {
		return mediaPreview{}, err
	}
	if err != nil {
		return mediaPreview{}, probeErr
	}
	return mediaPreview{URL: rawURL, Title: title, Artist: artist, TrackCount: 1}, nil
}

func resolveLinkMetadata(ctx context.Context, rawURL string) (string, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", err
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.HasPrefix(host, "music.yandex.") {
		// The page of a geo-blocked visitor is the home page, so the track API is asked first.
		segments := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
		if endpoint, kind, ok := yandexTarget(segments, false); ok && kind == "track" {
			result, err := yandexTracklist(ctx, endpoint, kind)
			if err == nil && len(result.Tracks) > 0 && result.Tracks[0].Title != "" {
				return result.Tracks[0].Title, result.Tracks[0].Artist, nil
			}
			if errors.Is(err, errMusicServiceBlocked) {
				return "", "", err
			}
		}
	}
	if host == "open.spotify.com" || strings.HasSuffix(host, ".spotify.com") {
		if title, artist, err := fetchOEmbed(ctx, "https://open.spotify.com/oembed?url="+url.QueryEscape(rawURL)); err == nil {
			return title, artist, nil
		}
	}
	if host == "deezer.com" || strings.HasSuffix(host, ".deezer.com") {
		if title, artist, err := fetchOEmbed(ctx, "https://api.deezer.com/oembed?url="+url.QueryEscape(rawURL)); err == nil {
			return title, artist, nil
		}
	}
	title, artist, err := fetchOpenGraph(ctx, rawURL)
	if err == nil && genericLandingTitle(rawURL, title) {
		return "", "", errGenericLinkPage
	}
	return title, artist, err
}

// genericLandingTitle recognises a home page title such as "Яндекс Музыка — собираем музыку
// для вас" or "Spotify – Web Player": it starts with the link's own service name, while
// release pages start with the release and name the service at the end, if at all.
func genericLandingTitle(rawURL, title string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	var names []string
	switch {
	case strings.HasPrefix(host, "music.yandex."):
		names = []string{"яндекс музыка", "яндекс.музыка", "yandex music", "yandex.music"}
	case hostWithin(host, "spotify.com"):
		names = []string{"spotify"}
	case hostWithin(host, "music.apple.com"):
		names = []string{"apple music"}
	case hostWithin(host, "deezer.com"):
		names = []string{"deezer"}
	case hostWithin(host, "tidal.com"):
		names = []string{"tidal"}
	}
	title = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(title, "\u00a0", " ")))
	for _, name := range names {
		rest, ok := strings.CutPrefix(title, name)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "" || strings.ContainsAny(rest[:1], "-:|") || strings.HasPrefix(rest, "—") || strings.HasPrefix(rest, "–") {
			return true
		}
	}
	return false
}

func resolverClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: resolverTransport, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedHost(request.URL.Hostname()) {
			return errors.New("небезопасный redirect")
		}
		return nil
	}}
}

// yandexProxy is YANDEX_PROXY, set once at startup: Yandex Music answers HTTP 451 outside the
// CIS, so its site and API are reached through a proxy with a CIS exit.
var yandexProxy atomic.Pointer[url.URL]

// resolverTransport is shared by every resolver client so that connections are pooled.
var resolverTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = resolverProxy
	return transport
}()

// resolverProxy sends only Yandex Music hosts through YANDEX_PROXY; everything else keeps the
// environment proxy settings.
func resolverProxy(request *http.Request) (*url.URL, error) {
	if proxy := yandexProxy.Load(); proxy != nil && yandexMusicHost(request.URL.Hostname()) {
		return proxy, nil
	}
	return http.ProxyFromEnvironment(request)
}

// yandexMusicHost matches the Yandex Music site (music.yandex.ru and its regional domains) and
// its API (api.music.yandex.net), but not the artwork CDN, which is not geo-blocked.
func yandexMusicHost(host string) bool {
	parts := strings.Split(strings.ToLower(strings.TrimSuffix(host, ".")), ".")
	if len(parts) == 4 && parts[0] == "api" {
		parts = parts[1:]
	}
	if len(parts) != 3 || parts[0] != "music" || parts[1] != "yandex" {
		return false
	}
	switch parts[2] {
	case "ru", "by", "kz", "uz", "com", "net":
		return true
	}
	return false
}

var makeResolverClient = func() httpDoer { return resolverClient() }

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func fetchOEmbed(ctx context.Context, endpoint string) (string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	response, err := makeResolverClient().Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", errors.New("oEmbed недоступен")
	}
	var payload struct {
		Title  string `json:"title"`
		Author string `json:"author_name"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(payload.Title) == "" {
		return "", "", errors.New("oEmbed не вернул название")
	}
	return strings.TrimSpace(payload.Title), strings.TrimSpace(payload.Author), nil
}

func fetchOpenGraph(ctx context.Context, rawURL string) (string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("User-Agent", "musicbot-link-preview/1.0")
	response, err := makeResolverClient().Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", errors.New("страница недоступна")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	values := make(map[string]string)
	for _, tag := range metaTagPattern.FindAllString(string(data), -1) {
		attrs := make(map[string]string)
		for _, match := range metaAttrPattern.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(match[1])] = html.UnescapeString(match[2])
		}
		name := strings.ToLower(firstNonEmpty(attrs["property"], attrs["name"]))
		if name != "" {
			values[name] = strings.TrimSpace(attrs["content"])
		}
	}
	title := firstNonEmpty(values["og:title"], values["twitter:title"])
	artist := values["music:musician"]
	if title == "" {
		if match := titlePattern.FindStringSubmatch(string(data)); len(match) == 2 {
			title = strings.TrimSpace(html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(match[1], "")))
		}
	}
	if title == "" {
		return "", "", errors.New("не удалось определить название")
	}
	return title, artist, nil
}
