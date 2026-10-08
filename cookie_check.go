package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	cookieCheckStateKey = "cookie_check"
	cookieCheckVersion  = 1
	cookieCheckMaxBytes = 8 * 1024 * 1024
)

// cookieCheckResult is persisted as one metadata value, so the verdict, time and input identity
// can never come from different checks. Legacy login-only results intentionally do not qualify.
type cookieCheckResult struct {
	Login       cookieLogin `json:"result"`
	Detail      string      `json:"detail"`
	Fingerprint string      `json:"fingerprint"`
	Version     int         `json:"version"`
	CheckedAt   int64       `json:"checked_at"`
}

func (a *app) lastCookieCheck(ctx context.Context) cookieCheckResult {
	var result cookieCheckResult
	if a.store != nil {
		_ = json.Unmarshal([]byte(a.store.metadata(ctx, cookieCheckStateKey)), &result)
	}
	return result
}

func (r cookieCheckResult) matches(snapshot cookieSnapshot) bool {
	return r.Version == cookieCheckVersion && r.CheckedAt > 0 && snapshot.fingerprint != "" && r.Fingerprint == snapshot.fingerprint
}

func (a *app) cookieCheckFreshness() time.Duration {
	if a.cfg.CookieCheckInterval > 0 {
		return a.cfg.CookieCheckInterval
	}
	return 3 * time.Hour
}

func (a *app) recordCookieCheck(ctx context.Context, result cookieCheckResult) {
	if a.store == nil || ctx.Err() != nil {
		return
	}
	result.Version = cookieCheckVersion
	result.CheckedAt = a.cookieAlerts.clock().Unix()
	result.Detail = shortenRunes(strings.ToValidUTF8(redactTraceText(result.Detail), ""), 200)
	data, err := json.Marshal(result)
	if err == nil {
		_ = a.store.setMetadata(ctx, cookieCheckStateKey, string(data))
	}
}

// executeCookieCheck owns a slot previously claimed by startLoginCheck. Every trigger uses the
// same gate, download capacity, deadline and recording path, including the administrator button.
func (a *app) executeCookieCheck(ctx context.Context) cookieCheckResult {
	recordCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, cookieLoginTimeout)
	defer cancel()
	result := cookieCheckResult{Detail: "check unavailable"}
	if a.downloader != nil {
		if snapshot, err := a.downloader.readCookieSnapshot(); err == nil {
			result.Fingerprint = snapshot.fingerprint
		}
	}
	defer func() {
		a.recordCookieCheck(recordCtx, result)
		a.cookieAlerts.finishLoginCheck(result.Login, result.Fingerprint)
	}()
	if a.cookieLoginCheck == nil {
		return result
	}
	_, release, err := a.downloads.acquire(ctx)
	if err != nil {
		result.Detail = "download capacity unavailable; check inconclusive"
		return result
	}
	defer release()
	if a.disk != nil {
		releaseDisk, err := a.disk.acquire(ctx, 2*cookieCheckMaxBytes, nil)
		if err != nil {
			result.Detail = "disk capacity unavailable; check inconclusive"
			return result
		}
		defer releaseDisk()
	}
	result = a.cookieLoginCheck(ctx)
	return result
}

// checkCookieLogin requires a private Watch Later response and a complete, uncached audio
// download with the same cookies and configured signed-in client/PO provider. runOnce deliberately
// bypasses all anonymous retries. Neither playlist contents nor signed media URLs are persisted.
func (d *downloader) checkCookieLogin(ctx context.Context) cookieCheckResult {
	result := cookieCheckResult{Version: cookieCheckVersion}
	snapshot, err := d.readCookieSnapshot()
	if err != nil {
		result.Detail = "cookies file cannot be read"
		return result
	}
	result.Fingerprint = snapshot.fingerprint
	if snapshot.fingerprint == "" {
		result.Login, result.Detail = cookieLoginInvalid, "no cookies file is configured"
		return result
	}
	cookies, cleanup, err := d.isolateCookieSnapshot(snapshot)
	if err != nil {
		result.Detail = "cannot create an isolated cookies copy"
		return result
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(ctx, cookieLoginTimeout)
	defer cancel()
	args := append(d.commonArgs(), "--no-cache-dir", "--socket-timeout", "15", "--retries", "1", "--extractor-retries", "1", "--cookies", cookies)
	stdout, stderr, err := d.runOnceLogged(ctx, append(append([]string(nil), args...),
		"--flat-playlist", "--playlist-end", "1", "--dump-single-json", "--", ":ytwatchlater"), false)
	result.Login, result.Detail = classifyCookieLogin(stdout, stderr, err)
	if result.Login != cookieLoginValid {
		return result
	}
	work, err := os.MkdirTemp(d.downloadDir, ".cookie-check-*")
	if err != nil {
		result.Login, result.Detail = cookieLoginUnknown, "cannot create an audio check directory"
		return result
	}
	defer os.RemoveAll(work)
	mediaArgs := append(append([]string(nil), args...),
		"--no-playlist", "--no-simulate", "--format", "bestaudio", "--max-filesize", "8M",
		"--output", filepath.Join(work, "audio.%(ext)s"),
		"--print", `after_move:{"id":%(id)j,"path":%(filepath)j}`)
	mediaArgs = append(mediaArgs, youtubeClientArgs(firstNonEmpty(d.youtubeCookieClients, "mweb"))...)
	stdout, stderr, err = d.runOnceLogged(ctx, append(mediaArgs, "--", cookieCheckURL), false)
	result.Login, result.Detail = classifyCookieMedia(stderr, err)
	if result.Login != cookieLoginValid {
		return result
	}
	var media struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	if json.Unmarshal(stdout, &media) != nil || media.ID != "dQw4w9WgXcQ" || filepath.Dir(media.Path) != work {
		result.Login, result.Detail = cookieLoginUnknown, "complete audio download was not confirmed"
		return result
	}
	info, err := os.Lstat(media.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > cookieCheckMaxBytes || strings.HasSuffix(media.Path, ".part") {
		result.Login, result.Detail = cookieLoginUnknown, "complete audio file was not produced"
		return result
	}
	result.Detail = "authenticated session and full audio download confirmed"
	return result
}

func classifyCookieLogin(stdout []byte, stderr string, err error) (cookieLogin, string) {
	if result, detail := cookieCheckFailure(stderr, err); result != cookieLoginValid {
		return result, detail
	}
	var playlist struct {
		Type         string `json:"_type"`
		ID           string `json:"id"`
		Availability string `json:"availability"`
		Extractor    string `json:"extractor_key"`
	}
	if json.Unmarshal(stdout, &playlist) != nil || playlist.Type != "playlist" || playlist.ID != "WL" || playlist.Availability != "private" || playlist.Extractor != "YoutubeTab" {
		return cookieLoginUnknown, "authenticated Watch Later response was not confirmed"
	}
	return cookieLoginValid, "authenticated Watch Later response confirmed"
}

func classifyCookieMedia(stderr string, err error) (cookieLogin, string) {
	result, detail := cookieCheckFailure(stderr, err)
	if detail == "rate limit or PO token provider failure; check inconclusive" {
		return result, detail
	}
	if result == cookieLoginInvalid && !strings.Contains(strings.ToLower(stderr), "cookies are no longer valid") {
		return cookieLoginDegraded, "login works, but YouTube blocked the signed-in audio download"
	}
	if result == cookieLoginUnknown && err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		low := strings.ToLower(stderr)
		if isForbiddenFailure(low) || strings.Contains(low, "no video formats") || strings.Contains(low, "requested format is not available") {
			return cookieLoginDegraded, "login works, but the signed-in audio download failed (403 or no audio formats)"
		}
	}
	return result, detail
}

// Only explicit authentication failures mean the cookies need replacing. Transport, rate limit
// and token-provider failures are inconclusive. Details are fixed strings, never remote output.
func cookieCheckFailure(stderr string, err error) (cookieLogin, string) {
	if errors.Is(err, context.Canceled) {
		return cookieLoginUnknown, "check canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return cookieLoginUnknown, "check timed out"
	}
	low := strings.ToLower(stderr)
	if strings.Contains(low, "cookies are no longer valid") {
		return cookieLoginInvalid, "YouTube cookies are no longer valid"
	}
	if strings.Contains(low, "http error 429") || strings.Contains(low, "too many requests") || strings.Contains(low, "failed to generate") || strings.Contains(low, "po token provider") && err != nil {
		return cookieLoginUnknown, "rate limit or PO token provider failure; check inconclusive"
	}
	for _, marker := range []string{"playlist does not exist", "sign in", "login required", "log in", "authentication required"} {
		if strings.Contains(low, marker) {
			return cookieLoginInvalid, "YouTube did not accept the authenticated session"
		}
	}
	if err != nil {
		return cookieLoginUnknown, "YouTube request failed; check inconclusive"
	}
	return cookieLoginValid, ""
}
