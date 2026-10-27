package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type vkMusicTarget struct {
	OwnerID, ID, AccessKey string
	Playlist               bool
}

var vkMusicIDPattern = regexp.MustCompile(`^(-?[1-9][0-9]{0,18})_([1-9][0-9]{0,18})(?:_([A-Za-z0-9-]{1,128}))?$`)

var errVKMusicSessionRequired = errors.New("Для VK Музыки нужна действующая сессия VK. Администратору нужно подключить cookies VK.")

//go:embed vk_music_extractor.py
var vkMusicExtractor []byte

var vkMusicOwnerPattern = regexp.MustCompile(`^/audios-?[1-9][0-9]{0,18}$`)

type vkMusicGate struct {
	once sync.Once
	slot chan struct{}
}

func (g *vkMusicGate) acquire(ctx context.Context) (func(), error) {
	g.once.Do(func() { g.slot = make(chan struct{}, 1) })
	select {
	case g.slot <- struct{}{}:
		return func() { <-g.slot }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}

func vkMusicHost(host string) bool {
	switch strings.ToLower(host) {
	case "vk.com", "www.vk.com", "m.vk.com", "music.vk.com", "vk.ru", "www.vk.ru", "m.vk.ru", "music.vk.ru":
		return true
	}
	return false
}

func parseVKMusicURL(rawURL string) (vkMusicTarget, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Host != u.Hostname() || !vkMusicHost(u.Hostname()) || (u.Scheme != "https" && u.Scheme != "http") {
		return vkMusicTarget{}, false
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return vkMusicTarget{}, false
	}
	value := ""
	playlist := false
	switch {
	case strings.HasPrefix(path, "/audio_playlist"):
		value, playlist = strings.TrimPrefix(path, "/audio_playlist"), true
	case strings.HasPrefix(path, "/music/album/"):
		value, playlist = strings.TrimPrefix(path, "/music/album/"), true
	case strings.HasPrefix(path, "/audio") && path != "/audio" && !strings.HasPrefix(path, "/audios"):
		value = strings.TrimPrefix(path, "/audio")
	default:
		// Music links can wrap the object in a z/act parameter. Never let a
		// query parameter change the meaning of a video or wall URL.
		if path != "/audio" && path != "/music" && !vkMusicOwnerPattern.MatchString(path) {
			return vkMusicTarget{}, false
		}
		for _, key := range []string{"z", "act"} {
			values := query[key]
			if len(values) > 1 {
				return vkMusicTarget{}, false
			}
			if len(values) == 0 || values[0] == "" {
				continue
			}
			candidate := values[0]
			if !strings.HasPrefix(candidate, "audio_playlist") {
				return vkMusicTarget{}, false
			}
			if value != "" {
				return vkMusicTarget{}, false
			}
			value, playlist = strings.TrimPrefix(candidate, "audio_playlist"), true
		}
	}
	parts := vkMusicIDPattern.FindStringSubmatch(value)
	if len(parts) != 4 {
		return vkMusicTarget{}, false
	}
	if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
		return vkMusicTarget{}, false
	}
	if _, err := strconv.ParseInt(parts[2], 10, 64); err != nil {
		return vkMusicTarget{}, false
	}
	target := vkMusicTarget{OwnerID: parts[1], ID: parts[2], AccessKey: parts[3], Playlist: playlist}
	// The older mobile playlist form keeps its access key in a query field.
	for _, key := range []string{"access_hash", "access_key"} {
		values := query[key]
		if len(values) > 1 {
			return vkMusicTarget{}, false
		}
		if len(values) == 1 && values[0] != "" {
			if target.AccessKey != "" && target.AccessKey != values[0] {
				return vkMusicTarget{}, false
			}
			if !vkMusicIDPattern.MatchString(target.OwnerID + "_" + target.ID + "_" + values[0]) {
				return vkMusicTarget{}, false
			}
			target.AccessKey = values[0]
		}
	}
	return target, true
}

func (t vkMusicTarget) sourceID() string { return t.OwnerID + "_" + t.ID }

func (t vkMusicTarget) canonicalURL() string {
	path := "/audio"
	if t.Playlist {
		path = "/music/album/"
	}
	value := t.sourceID()
	if t.AccessKey != "" {
		value += "_" + t.AccessKey
	}
	return "https://vk.ru" + path + value
}

func vkMusicArgsTarget(args []string) (vkMusicTarget, bool) {
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			return parseVKMusicURL(args[i+1])
		}
	}
	return vkMusicTarget{}, false
}

func singleSourceURL(rawURL string) bool {
	if singleYouTubeID(rawURL) != "" {
		return true
	}
	target, ok := parseVKMusicURL(rawURL)
	return ok && !target.Playlist
}

// VK sessions are never taken from the YouTube cookie file. Only cookies for
// VK origins enter the temporary jar, even if the user exported all sites.
func (d *downloader) isolatedCookiesForArgs(args []string) (string, func(), error) {
	if _, ok := vkMusicArgsTarget(args); !ok {
		return d.isolatedCookieFile()
	}
	path := d.vkCookiesFile
	if path == "" {
		path = filepath.Join(filepath.Dir(d.downloadDir), "vk-cookies.txt")
	}
	snapshot, err := readCookieSnapshotFile(path)
	if err != nil {
		return "", func() {}, errVKMusicSessionRequired
	}
	var filtered bytes.Buffer
	filtered.WriteString("# Netscape HTTP Cookie File\n")
	count := 0
	for _, row := range bytes.Split(snapshot.data, []byte("\n")) {
		line := strings.TrimSuffix(string(row), "\r")
		if strings.TrimSpace(line) == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_")) {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			return "", func() {}, errVKMusicSessionRequired
		}
		domain := strings.TrimPrefix(strings.TrimPrefix(fields[0], "#HttpOnly_"), ".")
		if !vkMusicHost(domain) {
			continue
		}
		if (fields[1] != "TRUE" && fields[1] != "FALSE") || !strings.HasPrefix(fields[2], "/") ||
			(fields[3] != "TRUE" && fields[3] != "FALSE") || fields[5] == "" || fields[6] == "" {
			return "", func() {}, errVKMusicSessionRequired
		}
		if _, err := strconv.ParseInt(fields[4], 10, 64); err != nil {
			return "", func() {}, errVKMusicSessionRequired
		}
		filtered.WriteString(line + "\n")
		count++
	}
	if count == 0 {
		return "", func() {}, errVKMusicSessionRequired
	}
	snapshot.data = filtered.Bytes()
	digest := sha256.Sum256(snapshot.data)
	snapshot.fingerprint = hex.EncodeToString(digest[:])
	return d.isolateCookieSnapshot(snapshot)
}

func (d *downloader) vkMusicExtractorArgs(args []string) ([]string, func(), error) {
	if _, ok := vkMusicArgsTarget(args); !ok {
		return args, func() {}, nil
	}
	dir, err := os.MkdirTemp(d.downloadDir, ".vk-music-extractor-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "e6akl4k", "yt_dlp_plugins", "extractor")
	if err := os.MkdirAll(path, 0o700); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if err := os.WriteFile(filepath.Join(path, "vk_music.py"), vkMusicExtractor, 0o600); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return argsBeforeSeparator(args, "--plugin-dirs", dir, "--sleep-requests", "2", "--concurrent-fragments", "1"), cleanup, nil
}

func (d *downloader) sourceExtractorArgs(args []string) ([]string, func(), error) {
	if _, ok := vkMusicArgsTarget(args); ok {
		return d.vkMusicExtractorArgs(args)
	}
	return d.newgroundsExtractorArgs(args)
}

func (d *downloader) acquireVKMusic(ctx context.Context, args []string) (func(), error) {
	if _, ok := vkMusicArgsTarget(args); ok {
		return d.vkGate.acquire(ctx)
	}
	return func() {}, nil
}

// Subprocess errors can contain cookie rows or signed media URLs. Classify VK
// failures before logging or returning them to the rest of the bot.
func vkMusicProcessError(args []string, stderr string, err error) (string, error) {
	if _, ok := vkMusicArgsTarget(args); !ok {
		return stderr, err
	}
	if err == nil && !strings.Contains(strings.ToLower(stderr), "error:") {
		return "", nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	code := "extract"
	low := strings.ToLower(stderr)
	for _, candidate := range []string{"session", "challenge", "rate_limit", "unavailable", "preview_only", "unsafe_url", "protocol", "too_large"} {
		if strings.Contains(low, "vk_music:"+candidate) {
			code = candidate
			break
		}
	}
	if strings.Contains(low, "429") {
		code = "rate_limit"
	}
	if strings.Contains(low, "403") {
		code = "session"
	}
	message := vkMusicErrorMessage(code)
	return message, fmt.Errorf("%s", message)
}

func vkMusicErrorMessage(code string) string {
	switch code {
	case "session":
		return errVKMusicSessionRequired.Error()
	case "challenge":
		return "VK просит подтвердить вход в браузере. Администратору нужно пройти проверку и обновить cookies VK."
	case "rate_limit":
		return "VK временно ограничил запросы. Попробуй позже."
	case "unavailable":
		return "Трек или плейлист VK недоступен этой сессии, удалён либо ограничен по региону."
	case "preview_only":
		return "VK отдал только фрагмент трека. Полную запись скачать не удалось."
	case "too_large":
		return "Плейлист VK слишком большой для получения полного списка."
	default:
		return "Не удалось получить аудио из VK Музыки. Администратору нужно проверить сессию и совместимость обработчика VK."
	}
}

// A session may be allowed only a preview. Validate the finished file against
// VK's track duration before it can enter Telegram or the persistent cache.
func (d *downloader) verifyVKMusicAudio(ctx context.Context, path string, expected int) error {
	if expected <= 0 {
		return errors.New(vkMusicErrorMessage("protocol"))
	}
	bin := "ffprobe"
	if d.ffmpegBin != "" {
		bin = filepath.Join(filepath.Dir(d.ffmpegBin), "ffprobe")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, bin, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	seconds, parseErr := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || parseErr != nil || !(seconds > 0) {
		return errors.New(vkMusicErrorMessage("protocol"))
	}
	margin := max(2.0, float64(expected)*0.02)
	if seconds+margin < float64(expected) {
		return errors.New(vkMusicErrorMessage("preview_only"))
	}
	return nil
}
