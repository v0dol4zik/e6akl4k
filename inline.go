package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	inlineResultLimit = 5
	inlineResultTTL   = 10 * time.Minute
	inlineQueryTTL    = 9 * time.Second
	inlineDebounce    = 450 * time.Millisecond
	inlineMinQueryLen = 3
)

type inlineCandidate struct {
	URL       string
	CacheKey  string
	Title     string
	Artist    string
	Duration  string
	UserID    int64
	ExpiresAt time.Time
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
	bot           *tgbotapi.BotAPI
	downloader    *downloader
	cacheChatID   int64
	placeholderID string
	cachePath     string
	store         *store
	cacheTTL      time.Duration

	mu          sync.Mutex
	candidates  map[string]inlineCandidate
	resultOrder []string
	active      map[string]inlineActiveDownload
	activeUser  map[int64]string
	queries     map[int64]inlineActiveQuery
	querySeq    uint64
	fileIDs     map[string]string
}

func newInlineService(ctx context.Context, bot *tgbotapi.BotAPI, dl *downloader, cacheChatID int64) (*inlineService, error) {
	service := &inlineService{
		bot:         bot,
		downloader:  dl,
		cacheChatID: cacheChatID,
		cachePath:   inlineCachePath(dl.downloadDir),
		candidates:  make(map[string]inlineCandidate),
		active:      make(map[string]inlineActiveDownload),
		activeUser:  make(map[int64]string),
		queries:     make(map[int64]inlineActiveQuery),
		fileIDs:     make(map[string]string),
	}
	if err := service.loadCache(); err != nil {
		return nil, fmt.Errorf("прочитать inline-кэш: %w", err)
	}
	placeholderID := strings.TrimSpace(os.Getenv("INLINE_PLACEHOLDER_FILE_ID"))
	if placeholderID == "" {
		var err error
		placeholderID, err = service.uploadPlaceholder(ctx)
		if err != nil {
			return nil, fmt.Errorf("подготовить inline placeholder: %w", err)
		}
	}
	service.placeholderID = placeholderID
	return service, nil
}

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

func inlineCachePath(downloadDir string) string {
	if cacheDir := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); cacheDir != "" {
		return filepath.Join(cacheDir, "inline-audio-cache.json")
	}
	return filepath.Join(downloadDir, "inline-audio-cache.json")
}

func (s *inlineService) uploadPlaceholder(ctx context.Context) (string, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", errors.New("ffmpeg не найден в PATH")
	}
	file, err := os.CreateTemp(s.downloader.downloadDir, ".inline-placeholder-*.mp3")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	defer os.Remove(path)

	placeholderCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(placeholderCtx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo",
		"-t", "1", "-q:a", "9", "-acodec", "libmp3lame", "-vn",
		"-metadata", "title=Downloading…", "-metadata", "artist=e6akl4k bot",
		path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(output)))
	}
	audio := tgbotapi.NewAudio(s.cacheChatID, tgbotapi.FilePath(path))
	audio.Title = "Downloading…"
	audio.Performer = "e6akl4k bot"
	sent, err := s.bot.Send(audio)
	if err != nil {
		return "", fmt.Errorf("загрузить placeholder в cache-канал: %w", err)
	}
	if sent.Audio == nil || sent.Audio.FileID == "" {
		return "", errors.New("Telegram не вернул file_id placeholder")
	}
	log.Printf("Inline placeholder загружен в cache-чат %d", s.cacheChatID)
	return sent.Audio.FileID, nil
}

func (s *inlineService) loadCache() error {
	data, err := os.ReadFile(s.cachePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, &s.fileIDs); err != nil {
		return err
	}
	if s.fileIDs == nil {
		s.fileIDs = make(map[string]string)
	}
	return nil
}

func (s *inlineService) cachedFileID(key string) string {
	if s.store != nil {
		if entry, ok := s.store.cachedAudio(context.Background(), key, s.cacheTTL); ok {
			return entry.FileID
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileIDs[key]
}

func (s *inlineService) cacheFileID(key, fileID string) error {
	if s.store != nil {
		return s.store.putCachedAudio(context.Background(), cachedAudio{Key: key, FileID: fileID, Format: "mp3", Quality: "320"})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileIDs[key] = fileID
	data, err := json.MarshalIndent(s.fileIDs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.cachePath), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.cachePath), ".inline-cache-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, s.cachePath)
}

func (s *inlineService) attachStore(state *store, ttl time.Duration) error {
	if state == nil {
		return nil
	}
	s.mu.Lock()
	legacy := make(map[string]string, len(s.fileIDs))
	for key, value := range s.fileIDs {
		legacy[key] = value
	}
	s.store, s.cacheTTL = state, ttl
	s.fileIDs = make(map[string]string)
	s.mu.Unlock()
	for key, fileID := range legacy {
		if _, ok := state.cachedAudio(context.Background(), key, ttl); !ok {
			if err := state.putCachedAudio(context.Background(), cachedAudio{Key: key, FileID: fileID, Format: "mp3", Quality: "320"}); err != nil {
				return err
			}
		}
	}
	if len(legacy) > 0 {
		_ = os.Rename(s.cachePath, s.cachePath+".migrated")
	}
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

func (s *inlineService) setActive(id string, active inlineActiveDownload) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.activeUser[active.userID]; exists {
		return false
	}
	s.active[id] = active
	s.activeUser[active.userID] = id
	return true
}

func (s *inlineService) clearActive(id string) {
	s.mu.Lock()
	if active, ok := s.active[id]; ok && s.activeUser[active.userID] == id {
		delete(s.activeUser, active.userID)
	}
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

func inlineCacheKey(url, id string) string {
	if id != "" {
		return "youtube:" + id + ":mp3:320"
	}
	sum := sha256.Sum256([]byte(url))
	return "url:" + hex.EncodeToString(sum[:16]) + ":mp3:320"
}

func inlineResultCaption(candidate inlineCandidate, lang string, loading bool) string {
	lines := make([]string, 0, 4)
	if loading {
		lines = append(lines, tr("inline_loading", lang))
	}
	lines = append(lines, "<b>"+html.EscapeString(shortenRunes(candidate.Title, maxTitleLength))+"</b>")
	if candidate.Artist != "" {
		lines = append(lines, "👤 "+html.EscapeString(shortenRunes(candidate.Artist, maxTitleLength)))
	}
	if candidate.Duration != "" {
		lines = append(lines, "⏱ "+html.EscapeString(candidate.Duration))
	}
	return strings.Join(lines, "\n")
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
