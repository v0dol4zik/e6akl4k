package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type config struct {
	BotToken           string
	DownloadDir        string
	DatabasePath       string
	CacheChatID        int64
	ErrorChatID        int64
	LastfmAPIKey       string
	ShazamPython       string
	YandexProxy        *url.URL
	TelegramAPIURL     string
	TelegramFileDir    string
	HTTPAddr           string
	DownloadWorkers    int
	DownloadQueueSize  int
	LookupWorkers      int
	LookupQueueSize    int
	ArchiveWorkers     int
	ArchiveQueueSize   int
	UpdateWorkers      int
	UpdateQueueSize    int
	RateLimit          int
	InlineRateLimit    int
	RateWindow         time.Duration
	AdminIDs           map[int64]bool
	CacheTTL           time.Duration
	MaxPlaylistTracks  int
	MaxFileSize        int64
	ShutdownTimeout    time.Duration
	DiskWarningBytes   int64
	DiskCheckInterval  time.Duration
	DropPendingUpdates bool
	YTDLPSleepRequests int
	YTDLPFragments     int
	// YTDLPYouTubeClients and YTDLPYouTubeCookieClients pin the YouTube player clients of the
	// anonymous and the signed-in download; "default" keeps yt-dlp's choice.
	YTDLPYouTubeClients       string
	YTDLPYouTubeCookieClients string
	// YTDLPPOTProviderURL is the bgutil PO token server for YouTube, such as
	// http://bgutil-pot:4416; "" leaves yt-dlp without a PO token provider.
	YTDLPPOTProviderURL string

	// StatusMessage keeps a pinned bot status message in the error chat.
	StatusMessage       bool
	CookieCheckInterval time.Duration
	ErrorDigestInterval time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		BotToken:        strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		DownloadDir:     envString("DOWNLOAD_DIR", "downloads"),
		HTTPAddr:        envString("HTTP_ADDR", "127.0.0.1:8080"),
		ShazamPython:    envString("SHAZAM_PYTHON", "python3"),
		TelegramFileDir: envString("TELEGRAM_FILE_DIR", ""),
		AdminIDs:        make(map[int64]bool),
	}
	var err error
	if cfg.DownloadWorkers, err = strictEnvInt("DOWNLOAD_WORKERS", maxParallelDownloads, 1, 32); err != nil {
		return config{}, err
	}
	if cfg.DownloadQueueSize, err = strictEnvInt("DOWNLOAD_QUEUE_SIZE", 0, 0, 10000); err != nil {
		return config{}, err
	}
	if cfg.YTDLPSleepRequests, err = strictEnvInt("YTDLP_SLEEP_REQUESTS", 0, 0, 60); err != nil {
		return config{}, err
	}
	if cfg.YTDLPFragments, err = strictEnvInt("YTDLP_CONCURRENT_FRAGMENTS", 4, 1, 16); err != nil {
		return config{}, err
	}
	if cfg.YTDLPYouTubeClients, err = strictEnvClients("YTDLP_YOUTUBE_CLIENTS", "tv_simply"); err != nil {
		return config{}, err
	}
	if cfg.YTDLPYouTubeCookieClients, err = strictEnvClients("YTDLP_YOUTUBE_COOKIE_CLIENTS", "mweb"); err != nil {
		return config{}, err
	}
	if cfg.YTDLPPOTProviderURL, err = envServerURL("YTDLP_POT_PROVIDER_URL", "сервера PO-токенов вида http://bgutil-pot:4416"); err != nil {
		return config{}, err
	}
	if cfg.LookupWorkers, err = strictEnvInt("LOOKUP_WORKERS", 2, 1, 32); err != nil {
		return config{}, err
	}
	if cfg.LookupQueueSize, err = strictEnvInt("LOOKUP_QUEUE_SIZE", 40, 0, 10000); err != nil {
		return config{}, err
	}
	if cfg.ArchiveWorkers, err = strictEnvInt("ARCHIVE_WORKERS", 1, 1, 8); err != nil {
		return config{}, err
	}
	if cfg.ArchiveQueueSize, err = strictEnvInt("ARCHIVE_QUEUE_SIZE", 10, 0, 1000); err != nil {
		return config{}, err
	}
	if cfg.UpdateWorkers, err = strictEnvInt("UPDATE_WORKERS", 32, 1, 256); err != nil {
		return config{}, err
	}
	if cfg.UpdateQueueSize, err = strictEnvInt("UPDATE_QUEUE_SIZE", 256, 1, 10000); err != nil {
		return config{}, err
	}
	if cfg.RateLimit, err = strictEnvInt("RATE_LIMIT", 12, 1, 10000); err != nil {
		return config{}, err
	}
	if cfg.InlineRateLimit, err = strictEnvInt("INLINE_RATE_LIMIT", 60, 1, 10000); err != nil {
		return config{}, err
	}
	if cfg.MaxPlaylistTracks, err = strictEnvInt("MAX_PLAYLIST_TRACKS", maxPlaylistTracks, 1, maxPlaylistTracksCeiling); err != nil {
		return config{}, err
	}
	if cfg.TelegramAPIURL, err = envTelegramAPIURL("TELEGRAM_API_URL"); err != nil {
		return config{}, err
	}
	if cfg.TelegramFileDir != "" && !filepath.IsAbs(cfg.TelegramFileDir) {
		return config{}, fmt.Errorf("TELEGRAM_FILE_DIR должен быть абсолютным путём к рабочему каталогу локального Bot API")
	}
	fileSizeCap := maxFileSize
	if cfg.TelegramAPIURL != "" {
		fileSizeCap = localMaxFileSize
	}
	if cfg.MaxFileSize, err = strictEnvInt64("MAX_FILE_SIZE", maxFileSize, 1024*1024, fileSizeCap); err != nil {
		return config{}, err
	}
	if cfg.RateWindow, err = strictEnvDuration("RATE_WINDOW", time.Minute); err != nil {
		return config{}, err
	}
	if cfg.CacheTTL, err = strictEnvDuration("CACHE_TTL", 180*24*time.Hour); err != nil {
		return config{}, err
	}
	if cfg.ShutdownTimeout, err = strictEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second); err != nil {
		return config{}, err
	}
	if cfg.DiskCheckInterval, err = strictEnvDuration("DISK_CHECK_INTERVAL", 10*time.Minute); err != nil {
		return config{}, err
	}
	if cfg.DiskWarningBytes, err = strictEnvInt64("DISK_WARNING_BYTES", 512*1024*1024, 10*1024*1024, 100*1024*1024*1024); err != nil {
		return config{}, err
	}
	if cfg.DropPendingUpdates, err = strictEnvBool("DROP_PENDING_UPDATES", false); err != nil {
		return config{}, err
	}
	if cfg.StatusMessage, err = strictEnvBool("STATUS_MESSAGE", true); err != nil {
		return config{}, err
	}
	if cfg.CookieCheckInterval, err = strictEnvDuration("COOKIE_CHECK_INTERVAL", 3*time.Hour); err != nil {
		return config{}, err
	}
	if cfg.ErrorDigestInterval, err = strictEnvDuration("ERROR_DIGEST_INTERVAL", 24*time.Hour); err != nil {
		return config{}, err
	}
	if cfg.AdminIDs, err = strictIDSet("ADMIN_IDS", os.Getenv("ADMIN_IDS")); err != nil {
		return config{}, err
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
	if errorChatText := strings.TrimSpace(os.Getenv("ERROR_CHAT_ID")); errorChatText != "" {
		id, err := strconv.ParseInt(errorChatText, 10, 64)
		if err != nil || id == 0 {
			return config{}, fmt.Errorf("ERROR_CHAT_ID должен быть числовым ID чата: %q", errorChatText)
		}
		cfg.ErrorChatID = id
	}
	cfg.LastfmAPIKey = strings.TrimSpace(os.Getenv("LASTFM_API_KEY"))
	if cfg.YandexProxy, err = envProxyURL("YANDEX_PROXY"); err != nil {
		return config{}, err
	}
	databaseDefault := filepath.Join(envString("XDG_DATA_HOME", cfg.DownloadDir), "musicbot.db")
	cfg.DatabasePath = envString("DATABASE_PATH", databaseDefault)
	return cfg, nil
}

// envProxyURL reads an optional HTTP or SOCKS5 proxy address. The value is never echoed in the
// error because it may carry a password.
func envProxyURL(key string) (*url.URL, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil, nil
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Hostname() != "" && strings.Trim(parsed.Path, "/") == "" && parsed.RawQuery == "" {
		switch parsed.Scheme {
		case "http", "https", "socks5", "socks5h":
			return parsed, nil
		}
	}
	return nil, fmt.Errorf("%s должен быть адресом прокси вида socks5h://host:port или http://host:port", key)
}

// envTelegramAPIURL reads the base address of a local Telegram Bot API server, such as
// http://telegram-bot-api:8081, without a trailing slash; "" means the cloud Bot API.
func envTelegramAPIURL(key string) (string, error) {
	return envServerURL(key, "Bot API сервера вида http://telegram-bot-api:8081")
}

// envServerURL reads the base address of a sidecar server: http or https, a host and an optional
// port, without credentials, path, or query, and without a trailing slash; "" means none. Commas
// and semicolons are refused too, because yt-dlp splits extractor arguments on them.
func envServerURL(key, what string) (string, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil &&
		strings.Trim(parsed.Path, "/") == "" && parsed.RawQuery == "" && parsed.Fragment == "" && !strings.ContainsAny(value, "%?#,;") {
		return parsed.Scheme + "://" + parsed.Host, nil
	}
	return "", fmt.Errorf("%s должен быть адресом %s", key, what)
}

func strictEnvInt(key string, fallback, min, max int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s должен быть целым числом от %d до %d: %q", key, min, max, value)
	}
	return parsed, nil
}

func strictEnvInt64(key string, fallback, min, max int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s должен быть целым числом от %d до %d: %q", key, min, max, value)
	}
	return parsed, nil
}

func strictEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s должен быть положительной длительностью: %q", key, value)
	}
	return parsed, nil
}

func strictEnvBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s должен быть true или false: %q", key, value)
	}
	return parsed, nil
}

// strictEnvClients reads a comma-separated list of yt-dlp YouTube player clients such as
// "tv_simply,web" or "default,-web", so that the value cannot inject other extractor arguments.
func strictEnvClients(key, fallback string) (string, error) {
	value := strings.ToLower(strings.ReplaceAll(envString(key, fallback), " ", ""))
	for _, client := range strings.Split(value, ",") {
		name := strings.TrimPrefix(client, "-")
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
			return "", fmt.Errorf("%s должен быть списком клиентов YouTube через запятую: %q", key, value)
		}
	}
	return value, nil
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func strictIDSet(key, value string) (map[int64]bool, error) {
	result := make(map[int64]bool)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		id, err := strconv.ParseInt(item, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%s содержит некорректный Telegram ID: %q", key, item)
		}
		result[id] = true
	}
	return result, nil
}
