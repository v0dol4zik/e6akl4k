package main

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfigParsesAndBoundsEnvironment(t *testing.T) {
	keys := []string{"BOT_TOKEN", "CACHE_CHAT_ID", "INLINE_CACHE_CHAT_ID", "ERROR_CHAT_ID", "DOWNLOAD_WORKERS", "DOWNLOAD_QUEUE_SIZE", "YTDLP_SLEEP_REQUESTS", "YTDLP_CONCURRENT_FRAGMENTS", "RATE_WINDOW", "ADMIN_IDS", "MAX_FILE_SIZE", "XDG_DATA_HOME"}
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
