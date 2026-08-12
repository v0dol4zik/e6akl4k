package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type config struct {
	BotToken          string
	DownloadDir       string
	DatabasePath      string
	CacheChatID       int64
	HTTPAddr          string
	DownloadWorkers   int
	DownloadQueueSize int
	LookupWorkers     int
	LookupQueueSize   int
	UpdateWorkers     int
	UpdateQueueSize   int
	RateLimit         int
	InlineRateLimit   int
	RateWindow        time.Duration
	CookieConcurrency int
	AdminIDs          map[int64]bool
	CacheTTL          time.Duration
	MaxPlaylistTracks int
	MaxFileSize       int64
	ShutdownTimeout   time.Duration
	DiskWarningBytes  int64
	DiskCheckInterval time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		BotToken:          strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		DownloadDir:       envString("DOWNLOAD_DIR", "downloads"),
		HTTPAddr:          envString("HTTP_ADDR", "127.0.0.1:8080"),
		DownloadWorkers:   envInt("DOWNLOAD_WORKERS", maxParallelDownloads, 1, 32),
		DownloadQueueSize: envInt("DOWNLOAD_QUEUE_SIZE", 20, 0, 10000),
		LookupWorkers:     envInt("LOOKUP_WORKERS", 2, 1, 32),
		LookupQueueSize:   envInt("LOOKUP_QUEUE_SIZE", 40, 0, 10000),
		UpdateWorkers:     envInt("UPDATE_WORKERS", 32, 1, 256),
		UpdateQueueSize:   envInt("UPDATE_QUEUE_SIZE", 256, 1, 10000),
		RateLimit:         envInt("RATE_LIMIT", 12, 1, 10000),
		InlineRateLimit:   envInt("INLINE_RATE_LIMIT", 60, 1, 10000),
		RateWindow:        envDuration("RATE_WINDOW", time.Minute),
		CookieConcurrency: envInt("YTDLP_COOKIE_CONCURRENCY", 1, 1, 32),
		CacheTTL:          envDuration("CACHE_TTL", 180*24*time.Hour),
		MaxPlaylistTracks: envInt("MAX_PLAYLIST_TRACKS", maxPlaylistTracks, 1, 1000),
		MaxFileSize:       envInt64("MAX_FILE_SIZE", maxFileSize, 1024*1024, maxFileSize),
		ShutdownTimeout:   envDuration("SHUTDOWN_TIMEOUT", 30*time.Second),
		DiskWarningBytes:  envInt64("DISK_WARNING_BYTES", 512*1024*1024, 10*1024*1024, 100*1024*1024*1024),
		DiskCheckInterval: envDuration("DISK_CHECK_INTERVAL", 10*time.Minute),
		AdminIDs:          parseIDSet(os.Getenv("ADMIN_IDS")),
	}
	if cfg.BotToken == "" {
		return config{}, fmt.Errorf("BOT_TOKEN не задан в .env или переменных окружения")
	}
	cacheChatText := firstNonEmpty(os.Getenv("CACHE_CHAT_ID"), os.Getenv("INLINE_CACHE_CHAT_ID"))
	if cacheChatText != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(cacheChatText), 10, 64)
		if err != nil || id == 0 {
			return config{}, fmt.Errorf("CACHE_CHAT_ID/INLINE_CACHE_CHAT_ID должен быть числовым ID чата: %q", cacheChatText)
		}
		cfg.CacheChatID = id
	}
	databaseDefault := filepath.Join(envString("XDG_DATA_HOME", cfg.DownloadDir), "musicbot.db")
	cfg.DatabasePath = envString("DATABASE_PATH", databaseDefault)
	return cfg, nil
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

func envInt64(key string, fallback, min, max int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func parseIDSet(value string) map[int64]bool {
	result := make(map[int64]bool)
	for _, item := range strings.Split(value, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64); err == nil && id != 0 {
			result[id] = true
		}
	}
	return result
}
