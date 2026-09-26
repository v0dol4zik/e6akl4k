package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigParsesAndBoundsEnvironment(t *testing.T) {
	keys := []string{"BOT_TOKEN", "CACHE_CHAT_ID", "INLINE_CACHE_CHAT_ID", "ERROR_CHAT_ID", "DOWNLOAD_WORKERS", "DOWNLOAD_QUEUE_SIZE", "YTDLP_SLEEP_REQUESTS", "YTDLP_CONCURRENT_FRAGMENTS", "RATE_WINDOW", "ADMIN_IDS", "MAX_FILE_SIZE", "TELEGRAM_API_URL", "XDG_DATA_HOME"}
	for _, key := range keys {
		key := key
		old, ok := os.LookupEnv(key)
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		})
		_ = os.Unsetenv(key)
	}
	_ = os.Setenv("BOT_TOKEN", "token")
	_ = os.Setenv("CACHE_CHAT_ID", "-1001")
	_ = os.Setenv("ERROR_CHAT_ID", "-1002")
	_ = os.Setenv("DOWNLOAD_WORKERS", "4")
	_ = os.Setenv("YTDLP_SLEEP_REQUESTS", "2")
	_ = os.Setenv("YTDLP_CONCURRENT_FRAGMENTS", "6")
	_ = os.Setenv("RATE_WINDOW", "2m")
	_ = os.Setenv("ADMIN_IDS", "10, 20")
	_ = os.Setenv("MAX_FILE_SIZE", "52428800")
	_ = os.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheChatID != -1001 || cfg.ErrorChatID != -1002 || cfg.DownloadWorkers != 4 || cfg.YTDLPSleepRequests != 2 || cfg.YTDLPFragments != 6 || cfg.RateWindow != 2*time.Minute || !cfg.AdminIDs[10] || !cfg.AdminIDs[20] {
		t.Fatalf("config=%#v", cfg)
	}
	if cfg.MaxFileSize != maxFileSize {
		t.Fatalf("unbounded file size=%d", cfg.MaxFileSize)
	}
	if cfg.DownloadQueueSize != 0 {
		t.Fatalf("download queue size=%d, want no waiting queue", cfg.DownloadQueueSize)
	}
}

func TestLoadConfigRejectsInvalidEnvironment(t *testing.T) {
	oldToken, tokenOK := os.LookupEnv("BOT_TOKEN")
	oldWorkers, workersOK := os.LookupEnv("DOWNLOAD_WORKERS")
	t.Cleanup(func() {
		if tokenOK {
			_ = os.Setenv("BOT_TOKEN", oldToken)
		} else {
			_ = os.Unsetenv("BOT_TOKEN")
		}
		if workersOK {
			_ = os.Setenv("DOWNLOAD_WORKERS", oldWorkers)
		} else {
			_ = os.Unsetenv("DOWNLOAD_WORKERS")
		}
	})
	_ = os.Setenv("BOT_TOKEN", "token")
	_ = os.Setenv("DOWNLOAD_WORKERS", "many")
	if _, err := loadConfig(); err == nil {
		t.Fatal("invalid DOWNLOAD_WORKERS was silently accepted")
	}
}

func TestLoadConfigRequiresToken(t *testing.T) {
	old, ok := os.LookupEnv("BOT_TOKEN")
	_ = os.Unsetenv("BOT_TOKEN")
	defer func() {
		if ok {
			_ = os.Setenv("BOT_TOKEN", old)
		}
	}()
	if _, err := loadConfig(); err == nil {
		t.Fatal("missing token accepted")
	}
}

func TestLoadConfigYouTubeClients(t *testing.T) {
	t.Setenv("BOT_TOKEN", "token")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("YTDLP_YOUTUBE_CLIENTS", "")
	t.Setenv("YTDLP_YOUTUBE_COOKIE_CLIENTS", "")
	cfg, err := loadConfig()
	if err != nil || cfg.YTDLPYouTubeClients != "tv_simply" || cfg.YTDLPYouTubeCookieClients != "mweb" {
		t.Fatalf("defaults: %q %q %v", cfg.YTDLPYouTubeClients, cfg.YTDLPYouTubeCookieClients, err)
	}
	t.Setenv("YTDLP_YOUTUBE_CLIENTS", " TV_Simply, web ")
	t.Setenv("YTDLP_YOUTUBE_COOKIE_CLIENTS", "default,-web_safari")
	if cfg, err = loadConfig(); err != nil || cfg.YTDLPYouTubeClients != "tv_simply,web" || cfg.YTDLPYouTubeCookieClients != "default,-web_safari" {
		t.Fatalf("custom: %q %q %v", cfg.YTDLPYouTubeClients, cfg.YTDLPYouTubeCookieClients, err)
	}
	for _, value := range []string{"web;youtube:skip=dash", "web,,tv", "-", "web=1"} {
		t.Setenv("YTDLP_YOUTUBE_CLIENTS", value)
		if _, err := loadConfig(); err == nil {
			t.Errorf("YTDLP_YOUTUBE_CLIENTS=%q must be rejected", value)
		}
	}
}

func TestLoadConfigYandexProxy(t *testing.T) {
	t.Setenv("BOT_TOKEN", "token")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("YANDEX_PROXY", " socks5h://yandex-relay:1080 ")
	cfg, err := loadConfig()
	if err != nil || cfg.YandexProxy == nil || cfg.YandexProxy.String() != "socks5h://yandex-relay:1080" {
		t.Fatalf("proxy=%v err=%v", cfg.YandexProxy, err)
	}
	for _, value := range []string{"yandex-relay:1080", "ftp://relay:21", "socks5://", "http://relay:8080/path", "socks5://user:secret@relay:1080?x=1"} {
		t.Setenv("YANDEX_PROXY", value)
		if _, err := loadConfig(); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("YANDEX_PROXY=%q err=%v", value, err)
		}
	}
}

func TestLoadConfigTelegramAPIURL(t *testing.T) {
	t.Setenv("BOT_TOKEN", "token")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("TELEGRAM_API_URL", "")
	t.Setenv("MAX_FILE_SIZE", "524288000")
	if _, err := loadConfig(); err == nil {
		t.Fatal("the cloud Bot API must not accept files over 50 MB")
	}

	t.Setenv("TELEGRAM_API_URL", " http://telegram-bot-api:8081/ ")
	cfg, err := loadConfig()
	if err != nil || cfg.TelegramAPIURL != "http://telegram-bot-api:8081" || cfg.MaxFileSize != 524288000 {
		t.Fatalf("url=%q size=%d err=%v", cfg.TelegramAPIURL, cfg.MaxFileSize, err)
	}
	if limit := (&app{cfg: cfg}).fileLimit(); limit != 524288000 {
		t.Fatalf("fileLimit=%d", limit)
	}
	t.Setenv("MAX_FILE_SIZE", "")
	if cfg, err = loadConfig(); err != nil || cfg.MaxFileSize != maxFileSize {
		t.Fatalf("the limit stays 50 MB until MAX_FILE_SIZE is raised: size=%d err=%v", cfg.MaxFileSize, err)
	}
	t.Setenv("MAX_FILE_SIZE", "2097152001")
	if _, err := loadConfig(); err == nil {
		t.Fatal("MAX_FILE_SIZE over the local server limit was accepted")
	}

	t.Setenv("MAX_FILE_SIZE", "")
	for _, value := range []string{"telegram-bot-api:8081", "ftp://telegram-bot-api", "http://", "http://telegram-bot-api:8081/bot", "http://user:secret@telegram-bot-api:8081", "http://telegram-bot-api:8081?x=1", "http://telegram-bot-api:8081/#x"} {
		t.Setenv("TELEGRAM_API_URL", value)
		if _, err := loadConfig(); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("TELEGRAM_API_URL=%q err=%v", value, err)
		}
	}
}
