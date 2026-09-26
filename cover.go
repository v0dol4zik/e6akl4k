package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// maxCoverBytes bounds one cover image read from disk or a music API.
const maxCoverBytes = 20 << 20

var (
	errCoverNotFound = errors.New("cover not found")
	// deezerCoverSize and yandexCoverSize match the size part of Deezer
	// (".../1000x1000-000000-80-0-0.jpg") and Yandex (".../1000x1000") artwork paths.
	deezerCoverSize = regexp.MustCompile(`/\d+x\d+(-\d+){0,4}\.jpg$`)
	yandexCoverSize = regexp.MustCompile(`/\d+x\d+$`)
)

// coverImageHosts are the artwork CDNs of the music APIs used by export; a cover URL from an
// API response is fetched only from these hosts and only over HTTPS.
var coverImageHosts = []string{"dzcdn.net", "avatars.yandex.net", "avatars.mds.yandex.net"}

// coverImage is a ready-to-send cover file; Name is the file name without its extension.
type coverImage struct {
	Name string
	Ext  string
	Data []byte
}

type coverFile struct {
	path          string
	width, height int
	playlist      bool
}

// coverFor fetches the best cover for a link: music APIs give the release artwork directly,
// everything else goes through yt-dlp thumbnails.
func (a *app) coverFor(ctx context.Context, rawURL string) (coverImage, error) {
	if streamingOnlyLink(rawURL) {
		return coverImage{}, errExportUnsupported
	}
	if result, handled, err := musicServiceTracklist(ctx, rawURL, false); handled && err == nil && result.CoverURL != "" {
		for _, candidate := range coverCandidates(result.CoverURL) {
			cover, coverErr := downloadCoverImage(ctx, candidate, result.Name)
			if coverErr == nil {
				return cover, nil
			}
			log.Printf("Не удалось загрузить обложку из API: %v", coverErr)
		}
	}
	return a.downloader.fetchCover(ctx, rawURL)
}

// coverCandidates lists artwork URLs best first: the largest rendition the CDN serves, then
// the URL named by the API, in case the larger one is missing for this release.
func coverCandidates(rawURL string) []string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return []string{rawURL}
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "dzcdn.net" || strings.HasSuffix(host, ".dzcdn.net"):
		parsed.Path = deezerCoverSize.ReplaceAllString(parsed.Path, "/1800x1800-000000-100-0-0.jpg")
	case coverImageHost(host):
		parsed.Path = yandexCoverSize.ReplaceAllString(parsed.Path, "/orig")
	}
	if best := parsed.String(); best != rawURL {
		return []string{best, rawURL}
	}
	return []string{rawURL}
}

func (a *app) sendCover(chatID int64, cover coverImage, lang string) error {
	name := firstNonEmpty(cover.Name, tr("export_untitled", lang))
	document := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: exportFileName(cover.Name, "cover") + "." + cover.Ext, Bytes: cover.Data})
	document.Caption = tr("cover_caption", lang, "name", html.EscapeString(shortenRunes(name, 200)))
	document.ParseMode = "HTML"
	_, err := sendTelegram(a.bot, document)
	return err
}

// fetchCover writes the thumbnails of a link (and of its playlist, limited to the first entry)
// into a session directory without downloading media, then picks the best one.
func (d *downloader) fetchCover(ctx context.Context, rawURL string) (coverImage, error) {
	session, err := randomID()
	if err != nil {
		return coverImage{}, err
	}
	dir := filepath.Join(d.downloadDir, session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return coverImage{}, err
	}
	defer d.clearSession(session)
	args := append(d.commonArgs(),
		"--dump-single-json", "--no-simulate", "--skip-download",
		"--ignore-no-formats-error", "--ignore-errors",
		"--write-thumbnail", "--playlist-items", "1",
		"--paths", dir,
		"--output", "thumbnail:track.%(ext)s",
		"--output", "pl_thumbnail:playlist.%(ext)s",
		"--", rawURL)
	stdout, stderr, runErr := d.run(ctx, args...)
	if ctx.Err() != nil {
		return coverImage{}, ctx.Err()
	}
	var info mediaInfo
	_ = json.Unmarshal(stdout, &info)
	files := d.coverFiles(ctx, dir)
	if len(files) == 0 {
		if runErr != nil {
			return coverImage{}, errors.New(humanizeError(firstNonEmpty(stderr, runErr.Error())))
		}
		return coverImage{}, errCoverNotFound
	}
	chosen, side := pickCover(files, topicUpload(&info))
	path := chosen.path
	if side > 0 {
		cropped := filepath.Join(dir, "cover.png")
		if err := d.convertImage(ctx, chosen.path, cropped, fmt.Sprintf("crop=%d:%d", side, side)); err != nil {
			log.Printf("Не удалось обрезать обложку: %v", err)
		} else {
			path = cropped
		}
	}
	data, err := readCoverFile(path)
	if err != nil {
		return coverImage{}, err
	}
	ext := "jpg"
	if strings.EqualFold(filepath.Ext(path), ".png") {
		ext = "png"
	}
	return coverImage{Name: coverName(&info), Ext: ext, Data: data}, nil
}

// coverFiles lists the written thumbnails with their dimensions. Formats the standard library
// cannot decode (WebP from YouTube) are converted to lossless PNG first.
func (d *downloader) coverFiles(ctx context.Context, dir string) []coverFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []coverFile
	for _, entry := range entries {
		name := entry.Name()
		playlist := strings.HasPrefix(name, "playlist.")
		if !entry.Type().IsRegular() || (!playlist && !strings.HasPrefix(name, "track.")) {
			continue
		}
		path := filepath.Join(dir, name)
		switch strings.ToLower(filepath.Ext(name)) {
		case ".jpg", ".jpeg", ".png":
		default:
			converted := strings.TrimSuffix(path, filepath.Ext(path)) + ".converted.png"
			if err := d.convertImage(ctx, path, converted, ""); err != nil {
				log.Printf("Не удалось конвертировать обложку %s: %v", name, err)
				continue
			}
			path = converted
		}
		width, height, ok := imageSize(path)
		if ok {
			files = append(files, coverFile{path: path, width: width, height: height, playlist: playlist})
		}
	}
	return files
}

// pickCover prefers square artwork: the playlist (album) image, then the track image. YouTube
// "Artist - Topic" uploads show the album art centred in a letterboxed 16:9 frame, so their
// largest thumbnail is cropped to that square (side > 0); anything else is sent as is.
func pickCover(files []coverFile, topic bool) (coverFile, int) {
	for _, playlist := range []bool{true, false} {
		for _, file := range files {
			if file.playlist == playlist && squareImage(file.width, file.height) {
				return file, 0
			}
		}
	}
	largest, largestTrack := files[0], -1
	for i, file := range files {
		if file.width*file.height > largest.width*largest.height {
			largest = file
		}
		if !file.playlist && (largestTrack < 0 || file.width*file.height > files[largestTrack].width*files[largestTrack].height) {
			largestTrack = i
		}
	}
	if topic && largestTrack >= 0 {
		file := files[largestTrack]
		if side := min(file.height, file.width*9/16); side > 0 && side < file.width {
			return file, side
		}
	}
	return largest, 0
}

func squareImage(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	diff := width - height
	if diff < 0 {
		diff = -diff
	}
	return diff*50 <= max(width, height)
}

func topicUpload(info *mediaInfo) bool {
	candidates := []*mediaInfo{info}
	if len(info.Entries) > 0 {
		candidates = append(candidates, info.Entries[0])
	}
	for _, candidate := range candidates {
		if candidate != nil && strings.HasSuffix(firstNonEmpty(candidate.Channel, candidate.Uploader), " - Topic") {
			return true
		}
	}
	return false
}

func coverName(info *mediaInfo) string {
	if info.Type == "playlist" || len(info.Entries) > 0 {
		return playlistName(info.Title, firstNonEmpty(info.Uploader, info.Channel))
	}
	return trackFromInfo(info).line()
}

// convertImage re-encodes one image with an optional ffmpeg filter (a centred crop). The
// output format follows its extension; covers use PNG, so a crop adds no JPEG artefacts.
func (d *downloader) convertImage(ctx context.Context, input, output, filter string) error {
	if d.ffmpegBin == "" {
		return errors.New("ffmpeg недоступен")
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", input, "-frames:v", "1"}
	if filter != "" {
		args = append(args, "-vf", filter)
	}
	if !strings.EqualFold(filepath.Ext(output), ".png") {
		args = append(args, "-q:v", "2")
	}
	args = append(args, "-update", "1", output)
	combined, err := exec.CommandContext(ctx, d.ffmpegBin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, shortenRunes(strings.TrimSpace(string(combined)), 300))
	}
	return nil
}

func imageSize(path string) (int, int, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

func readCoverFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxCoverBytes {
		return nil, errors.New("обложка слишком большая")
	}
	return os.ReadFile(path)
}

// downloadCoverImage fetches artwork named by a music API. Only HTTPS URLs on the known image
// CDNs are requested, and the body must decode as JPEG or PNG.
func downloadCoverImage(ctx context.Context, rawURL, name string) (coverImage, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || !coverImageHost(parsed.Hostname()) {
		return coverImage{}, errors.New("недопустимый адрес обложки")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return coverImage{}, err
	}
	response, err := makeResolverClient().Do(request)
	if err != nil {
		return coverImage{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return coverImage{}, fmt.Errorf("обложка недоступна: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCoverBytes+1))
	if err != nil {
		return coverImage{}, err
	}
	if len(data) > maxCoverBytes {
		return coverImage{}, errors.New("обложка слишком большая")
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return coverImage{}, errors.New("обложка не является изображением JPEG или PNG")
	}
	ext := "jpg"
	if format == "png" {
		ext = "png"
	}
	return coverImage{Name: name, Ext: ext, Data: data}, nil
}

func coverImageHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range coverImageHosts {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
