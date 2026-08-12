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
	"time"
)

var (
	metaTagPattern  = regexp.MustCompile(`(?is)<meta\s+[^>]*>`)
	metaAttrPattern = regexp.MustCompile(`(?is)([a-zA-Z_:.-]+)\s*=\s*["']([^"']*)["']`)
	titlePattern    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

func (a *app) inspectURL(ctx context.Context, rawURL string) (mediaPreview, error) {
	preview, probeErr := a.downloader.preview(ctx, rawURL)
	if probeErr == nil || !requiresMusicResolution(rawURL) {
		return preview, probeErr
	}
	title, artist, err := resolveLinkMetadata(ctx, rawURL)
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
	return fetchOpenGraph(ctx, rawURL)
}

func resolverClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedHost(request.URL.Hostname()) {
			return errors.New("небезопасный redirect")
		}
		return nil
	}}
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
