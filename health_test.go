package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthAndMetricsHandlers(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(bin, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	state.increment(context.Background(), "downloads_ok")
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute}
	app := newAppWithServices(context.Background(), nil, &downloader{bin: bin, downloadDir: dir}, state, cfg)
	handler := observabilityHandler(app)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"status":"ok"`) {
		t.Fatalf("health code=%d body=%s", health.Code, health.Body.String())
	}

	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), `musicbot_downloads_total{result="ok"} 1`) {
		t.Fatalf("metrics code=%d body=%s", metrics.Code, metrics.Body.String())
	}
}
