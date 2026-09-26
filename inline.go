package main

import (
	"context"
	"encoding/json"
	"html"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	inlineResultLimit = 5
	inlineResultTTL   = 10 * time.Minute
	inlineQueryTTL    = 9 * time.Second
	inlineDebounce    = 450 * time.Millisecond
	inlineMinQueryLen = 3
	// Inline mode always sends MP3 320, the format Telegram's player plays everywhere.
	inlineFormat  = "mp3"
	inlineQuality = "320"
)

var youtubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

type inlineCandidate struct {
	URL        string
	CacheKey   string
	Title      string
	Artist     string
	Duration   string
	SourceID   string
	Extractor  string
	Thumbnail  string
	Confidence int
	Match      string
	UserID     int64
	ExpiresAt  time.Time
}

type inlineActiveDownload struct {
	cancel          context.CancelFunc
	userID          int64
	inlineMessageID string
}

type inlineActiveQuery struct {
	id     uint64
	cancel context.CancelFunc
}

type inlineService struct {
	store    *store
	cacheTTL time.Duration

	mu          sync.Mutex
	candidates  map[string]inlineCandidate
	resultOrder []string
	active      map[string]inlineActiveDownload
	queries     map[int64]inlineActiveQuery
	querySeq    uint64
}

func newInlineService(state *store, cacheTTL time.Duration) *inlineService {
	return &inlineService{
		store:      state,
		cacheTTL:   cacheTTL,
		candidates: make(map[string]inlineCandidate),
		active:     make(map[string]inlineActiveDownload),
		queries:    make(map[int64]inlineActiveQuery),
	}
}

// beginQuery starts the lookup of a user's newest inline query and cancels the previous one,
// which Telegram no longer shows.
func (s *inlineService) beginQuery(parent context.Context, userID int64) (context.Context, uint64) {
	ctx, cancel := context.WithTimeout(parent, inlineQueryTTL)
	s.mu.Lock()
	if previous, ok := s.queries[userID]; ok {
		previous.cancel()
	}
	s.querySeq++
	id := s.querySeq
	s.queries[userID] = inlineActiveQuery{id: id, cancel: cancel}
	s.mu.Unlock()
	return ctx, id
}

func (s *inlineService) finishQuery(userID int64, id uint64) {
	s.mu.Lock()
	if current, ok := s.queries[userID]; ok && current.id == id {
		current.cancel()
		delete(s.queries, userID)
	}
	s.mu.Unlock()
}

func (s *inlineService) cancelQuery(userID int64) {
	s.mu.Lock()
	if current, ok := s.queries[userID]; ok {
		current.cancel()
		delete(s.queries, userID)
	}
	s.mu.Unlock()
}

func (s *inlineService) cachedFileID(key string) string {
	if s.store == nil || key == "" {
		return ""
	}
	if entry, ok := s.store.cachedAudio(context.Background(), key, s.cacheTTL); ok {
		return entry.FileID
	}
	return ""
}

func inlineCachePath(downloadDir string) string {
	if cacheDir := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); cacheDir != "" {
		return filepath.Join(cacheDir, "inline-audio-cache.json")
	}
	return filepath.Join(downloadDir, "inline-audio-cache.json")
}

// migrateLegacyInlineCache imports the file_id map that inline mode kept in a JSON file before
// the SQLite cache and renames the file, so it is read only once.
func migrateLegacyInlineCache(ctx context.Context, state *store, path string, ttl time.Duration) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	legacy := make(map[string]string)
	if len(data) > 0 {
		if err := json.Unmarshal(data, &legacy); err != nil {
			return err
		}
	}
	for key, fileID := range legacy {
		if _, ok := state.cachedAudio(ctx, key, ttl); ok {
			continue
		}
		if err := state.putCachedAudio(ctx, cachedAudio{Key: key, FileID: fileID, Format: inlineFormat, Quality: inlineQuality}); err != nil {
			return err
		}
	}
	if err := os.Rename(path, path+".migrated"); err != nil {
		return err
	}
	log.Printf("Inline-кэш из %s перенесён в SQLite: %d записей", path, len(legacy))
	return nil
}

func (s *inlineService) storeCandidate(candidate inlineCandidate) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", err
	}
	candidate.ExpiresAt = time.Now().Add(inlineResultTTL)
	s.mu.Lock()
	for key, existing := range s.candidates {
		if time.Now().After(existing.ExpiresAt) {
			delete(s.candidates, key)
		}
	}
	s.candidates[id] = candidate
	s.resultOrder = append(s.resultOrder, id)
	for len(s.resultOrder) > maxStoredEntries {
		delete(s.candidates, s.resultOrder[0])
		s.resultOrder = s.resultOrder[1:]
	}
	s.mu.Unlock()
	return id, nil
}

func (s *inlineService) takeCandidate(id string, userID int64) (inlineCandidate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate, ok := s.candidates[id]
	if !ok || candidate.UserID != userID || time.Now().After(candidate.ExpiresAt) {
		return inlineCandidate{}, false
	}
	delete(s.candidates, id)
	return candidate, true
}

// setActive registers a running download for its cancel button; the per-user download guard
// already keeps one download per user.
func (s *inlineService) setActive(id string, active inlineActiveDownload) {
	s.mu.Lock()
	s.active[id] = active
	s.mu.Unlock()
}

func (s *inlineService) clearActive(id string) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
}

func (s *inlineService) cancelDownload(id string, userID int64, inlineMessageID string) bool {
	s.mu.Lock()
	active, ok := s.active[id]
	if ok && (active.userID != userID || active.inlineMessageID != inlineMessageID) {
		ok = false
	}
	s.mu.Unlock()
	if ok {
		active.cancel()
	}
	return ok
}

// inlineCacheKey is the cache key of the MP3 320 inline mode sends. It is the key private-chat
// downloads use too, so a track downloaded either way is reused by both.
func inlineCacheKey(rawURL, extractor, id string) string {
	if key := sourceCacheKey(extractor, id, inlineFormat, inlineQuality); key != "" {
		return key
	}
	return generalCacheKey(rawURL, inlineFormat, inlineQuality)
}

// youtubeCandidate is the inline result of one YouTube video; its title comes from the caller.
func youtubeCandidate(id string) inlineCandidate {
	link := "https://www.youtube.com/watch?v=" + id
	return inlineCandidate{
		URL: link, CacheKey: inlineCacheKey(link, "youtube", id), SourceID: id, Extractor: "youtube",
		Thumbnail: youtubeThumbnail(id),
	}
}

// youtubeThumbnail is the 320×180 JPEG preview of a video, which Telegram can show next to an
// inline result without fetching the page.
func youtubeThumbnail(id string) string {
	if !youtubeIDPattern.MatchString(id) {
		return ""
	}
	return "https://i.ytimg.com/vi/" + id + "/mqdefault.jpg"
}

// youtubeLinkTarget reads the video ID of a YouTube link. playlist is true for a playlist link
// without a video; channels and other pages return neither.
func youtubeLinkTarget(rawURL string) (id string, playlist bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")), "www.")
	segments := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
	switch {
	case host == "youtu.be":
		if len(segments) > 0 {
			id = segments[0]
		}
	case host == "youtube.com" || strings.HasSuffix(host, ".youtube.com"):
		switch {
		case len(segments) == 1 && segments[0] == "watch":
			id = parsed.Query().Get("v")
		case len(segments) >= 2 && (segments[0] == "shorts" || segments[0] == "live" || segments[0] == "embed"):
			id = segments[1]
		}
		if id == "" && parsed.Query().Get("list") != "" {
			return "", true
		}
	default:
		return "", false
	}
	if !youtubeIDPattern.MatchString(id) {
		return "", false
	}
	return id, false
}

// inlineMessageText is the text of an inline message that is not audio yet: a status line over
// the track it is about.
func inlineMessageText(status string, candidate inlineCandidate) string {
	lines := []string{status, "<b>" + html.EscapeString(shortenRunes(firstNonEmpty(candidate.Title, "Unknown"), maxTitleLength)) + "</b>"}
	if details := inlineDetails(candidate); details != "" {
		lines = append(lines, html.EscapeString(details))
	}
	return strings.Join(lines, "\n")
}

// inlineDetails is the "artist · duration" line under a result title.
func inlineDetails(candidate inlineCandidate) string {
	parts := make([]string, 0, 2)
	if candidate.Artist != "" {
		parts = append(parts, shortenRunes(candidate.Artist, maxTitleLength))
	}
	if candidate.Duration != "" {
		parts = append(parts, candidate.Duration)
	}
	return strings.Join(parts, " · ")
}

func inlineDurationSeconds(value string) int {
	parts := strings.Split(value, ":")
	seconds := 0
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			return 0
		}
		seconds = seconds*60 + number
	}
	return seconds
}
