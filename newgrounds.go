package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathbits "math/bits"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

//go:embed newgrounds_extractor.py
var newgroundsExtractor []byte

// newgroundsExtractorArgs installs the audio-page extractor only for this process.
// Older yt-dlp releases still expect Newgrounds' retired embedController markup.
func (d *downloader) newgroundsExtractorArgs(args []string) ([]string, func(), error) {
	if newgroundsTarget(args) == "" {
		return args, func() {}, nil
	}
	dir, err := os.MkdirTemp(d.downloadDir, ".newgrounds-extractor-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "e6akl4k", "yt_dlp_plugins", "extractor")
	if err := os.MkdirAll(path, 0o700); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if err := os.WriteFile(filepath.Join(path, "newgrounds_audio.py"), newgroundsExtractor, 0o600); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return argsBeforeSeparator(args, "--plugin-dirs", dir), cleanup, nil
}

const (
	newgroundsOrigin         = "https://www.newgrounds.com"
	newgroundsUserAgent      = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	newgroundsGuardTimeout   = 25 * time.Second
	newgroundsSolveTimeout   = 10 * time.Second
	newgroundsGuardBodyLimit = 64 << 10
)

type newgroundsChallenge struct {
	Algo    string                  `json:"algo"`
	Bits    int                     `json:"bits"`
	Payload string                  `json:"payload"`
	Sig     string                  `json:"sig"`
	Params  *newgroundsArgon2Params `json:"params,omitempty"`
}

type newgroundsArgon2Params struct {
	HashLength  uint32 `json:"hashLength"`
	Iterations  uint32 `json:"iterations"`
	MemorySize  uint32 `json:"memorySize"`
	Parallelism uint8  `json:"parallelism"`
}

func newgroundsTarget(args []string) string {
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			canonical := normalizeDetectedURL(args[i+1])
			if strings.HasPrefix(canonical, newgroundsOrigin+"/audio/listen/") {
				return canonical
			}
			break
		}
	}
	return ""
}

// newgroundsGuardArgs answers NG Guard's public browser proof of work after a 403.
// It never executes remote JavaScript or changes the configured cookies file.
func (d *downloader) newgroundsGuardArgs(ctx context.Context, args []string, baseCookies string) ([]string, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, newgroundsGuardTimeout)
	defer cancel()
	client := &http.Client{Transport: resolverTransport, Timeout: 10 * time.Second}
	if d.newgroundsHTTPClient != nil {
		copy := *d.newgroundsHTTPClient
		client = &copy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("Newgrounds browser check redirect rejected")
	}
	client.Jar, _ = cookiejar.New(nil)
	cookies, err := newgroundsGuardCookies(ctx, client, newgroundsTarget(args))
	if err != nil {
		return nil, func() {}, err
	}
	var content bytes.Buffer
	if baseCookies != "" {
		data, err := os.ReadFile(baseCookies)
		if err != nil {
			return nil, func() {}, err
		}
		content.Write(data)
		content.WriteByte('\n')
	} else {
		content.WriteString("# Netscape HTTP Cookie File\n")
	}
	for _, cookie := range cookies {
		if cookie.Valid() != nil || strings.ContainsAny(cookie.Name+cookie.Value, "\t\r\n") {
			return nil, func() {}, errors.New("invalid Newgrounds browser cookie")
		}
		fmt.Fprintf(&content, "www.newgrounds.com\tFALSE\t/\tTRUE\t0\t%s\t%s\n", cookie.Name, cookie.Value)
	}
	file, err := os.CreateTemp(d.downloadDir, ".cookies-newgrounds-*.txt")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	_, writeErr := file.Write(content.Bytes())
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return argsBeforeSeparator(args, "--cookies", file.Name(), "--user-agent", newgroundsUserAgent), cleanup, nil
}

func newgroundsGuardCookies(ctx context.Context, client *http.Client, rawURL string) ([]*http.Cookie, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || rawURL == "" || rawURL != normalizeDetectedURL(rawURL) || parsed.Host != "www.newgrounds.com" {
		return nil, errors.New("invalid Newgrounds audio page")
	}
	status, page, err := newgroundsGuardRequest(ctx, client, http.MethodGet, parsed.Path, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusForbidden || !bytes.Contains(page, []byte("<title>NG Guard</title>")) {
		return nil, errors.New("Newgrounds did not request a browser check")
	}
	status, body, err := newgroundsGuardRequest(ctx, client, http.MethodGet, "/_guard/api/v1/challenge", nil)
	if err != nil {
		return nil, err
	}
	var challenge newgroundsChallenge
	if status != http.StatusOK || json.Unmarshal(body, &challenge) != nil {
		return nil, errors.New("invalid Newgrounds browser challenge")
	}
	started := time.Now()
	nonce, err := solveNewgroundsChallenge(ctx, challenge)
	if err != nil {
		return nil, err
	}
	solution, err := json.Marshal(struct {
		newgroundsChallenge
		Nonce       string `json:"nonce"`
		SolveTimeMS int64  `json:"solveTimeMs"`
		Demo        bool   `json:"demo"`
	}{challenge, nonce, max(1, time.Since(started).Milliseconds()), false})
	if err != nil {
		return nil, err
	}
	status, body, err = newgroundsGuardRequest(ctx, client, http.MethodPost, "/_guard/api/v1/verify", solution)
	if err != nil {
		return nil, err
	}
	var verified struct {
		OK bool `json:"ok"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &verified) != nil || !verified.OK {
		return nil, errors.New("Newgrounds browser verification failed")
	}
	cookies := client.Jar.Cookies(parsed)
	// Verification can succeed without a cookie; the browser reloads the page
	// after an ok response either way.
	if len(cookies) > 20 {
		return nil, errors.New("Newgrounds browser verification returned too many cookies")
	}
	return cookies, nil
}

func newgroundsGuardRequest(ctx context.Context, client *http.Client, method, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, newgroundsOrigin+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", newgroundsUserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", newgroundsOrigin)
		req.Header.Set("Referer", newgroundsOrigin+"/")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, newgroundsGuardBodyLimit+1))
	if err != nil {
		return 0, nil, err
	}
	if len(data) > newgroundsGuardBodyLimit {
		return 0, nil, errors.New("Newgrounds browser response exceeds limit")
	}
	return resp.StatusCode, data, nil
}

func solveNewgroundsChallenge(ctx context.Context, challenge newgroundsChallenge) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, newgroundsSolveTimeout)
	defer cancel()
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(challenge.Payload, "="))
	if err != nil || len(payload) == 0 || len(payload) > 4096 || len(challenge.Sig) == 0 || len(challenge.Sig) > 1024 || challenge.Bits < 0 {
		return "", errors.New("invalid Newgrounds proof parameters")
	}
	maxAttempts := 1 << 22
	switch challenge.Algo {
	case "sha256":
		if challenge.Bits > 22 {
			return "", errors.New("Newgrounds proof difficulty exceeds limit")
		}
	case "argon2id":
		p := challenge.Params
		if p == nil || p.HashLength != 32 || p.Iterations < 1 || p.Iterations > 2 || p.Parallelism < 1 || p.Parallelism > 2 || p.MemorySize < 8*uint32(p.Parallelism) || p.MemorySize > 16*1024 || challenge.Bits > 12 {
			return "", errors.New("Newgrounds proof resources exceed limit")
		}
		maxAttempts = 4096
	default:
		return "", errors.New("unsupported Newgrounds proof algorithm")
	}
	prefix := append(payload, ':')
	input := make([]byte, len(prefix), len(prefix)+20)
	copy(input, prefix)
	for nonce := 0; nonce < maxAttempts; nonce++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		input = strconv.AppendInt(input[:len(prefix)], int64(nonce), 10)
		var hash []byte
		if challenge.Algo == "sha256" {
			sum := sha256.Sum256(input)
			hash = sum[:]
		} else {
			p := challenge.Params
			hash = argon2.IDKey(input, make([]byte, 8), p.Iterations, p.MemorySize, p.Parallelism, p.HashLength)
		}
		zeros := 0
		for _, b := range hash {
			zeros += mathbits.LeadingZeros8(b)
			if b != 0 {
				break
			}
		}
		if zeros >= challenge.Bits {
			return strconv.Itoa(nonce), nil
		}
	}
	return "", errors.New("Newgrounds proof attempts exceeded limit")
}
