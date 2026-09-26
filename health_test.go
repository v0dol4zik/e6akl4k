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
	state.increment(context.Background(), "downloads_partial")
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
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), `musicbot_downloads_total{result="ok"} 1`) || !strings.Contains(metrics.Body.String(), `musicbot_downloads_total{result="partial"} 1`) || !strings.Contains(metrics.Body.String(), "musicbot_archives_active 0") {
		t.Fatalf("metrics code=%d body=%s", metrics.Code, metrics.Body.String())
	}
}

func newMetricsTestApp(t *testing.T) (*app, *store) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(bin, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := openStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	cfg := config{DownloadWorkers: 1, DownloadQueueSize: 1, LookupWorkers: 1, LookupQueueSize: 1, RateLimit: 5, RateWindow: time.Minute}
	return newAppWithServices(context.Background(), nil, &downloader{bin: bin, downloadDir: dir}, state, cfg), state
}

func scrapeMetrics(t *testing.T, handler http.Handler) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics code=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func TestMetricsExportsCacheAndStageSeries(t *testing.T) {
	app, state := newMetricsTestApp(t)
	ctx := context.Background()
	for range 3 {
		state.increment(ctx, "cache_hits")
	}
	state.increment(ctx, "downloads_ok")
	state.increment(ctx, "custom_counter")
	now := time.Now()
	for _, elapsed := range []int64{10, 20, 30, 40, 100} {
		state.recordMediaStage(ctx, mediaStageSample{Stage: "source_download", Source: "youtube", ElapsedMS: elapsed, SizeBytes: 1000, OK: elapsed != 100, CreatedAt: now})
	}
	state.recordMediaStage(ctx, mediaStageSample{Stage: "telegram_upload", Source: "telegram", Mode: "remote_url", ElapsedMS: 5, OK: true, CreatedAt: now})
	state.recordMediaStage(ctx, mediaStageSample{Stage: "telegram_upload", Source: "telegram", Mode: "multipart", ElapsedMS: 7, OK: true, CreatedAt: now})
	state.recordMediaStage(ctx, mediaStageSample{Stage: "source_download", Source: "soundcloud", ElapsedMS: 500, OK: true, CreatedAt: now.Add(-2 * time.Hour)})

	body := scrapeMetrics(t, observabilityHandler(app))
	tests := []struct {
		name string
		line string
	}{
		{name: "counter row", line: `musicbot_counter_total{name="cache_hits"} 3`},
		{name: "custom counter row", line: `musicbot_counter_total{name="custom_counter"} 1`},
		{name: "cache hit ratio", line: "musicbot_cache_hit_ratio 0.7500"},
		{name: "p50", line: `musicbot_stage_seconds{stage="source_download",source="youtube",quantile="0.5"} 0.030000`},
		{name: "p95", line: `musicbot_stage_seconds{stage="source_download",source="youtube",quantile="0.95"} 0.040000`},
		{name: "ok ratio", line: `musicbot_stage_ok_ratio{stage="source_download",source="youtube"} 0.8000`},
		{name: "mode-less upload aggregate", line: `musicbot_stage_ok_ratio{stage="telegram_upload",source="telegram"} 1.0000`},
		{name: "legacy per-mode series", line: `musicbot_media_stage_samples{stage="telegram_upload",source="telegram",mode="remote_url"} 1`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(body, tc.line+"\n") {
				t.Fatalf("missing %q in:\n%s", tc.line, body)
			}
		})
	}
	if strings.Contains(body, `source="soundcloud"`) {
		t.Fatalf("samples older than one hour must be ignored:\n%s", body)
	}
}

func TestMetricsCacheHitRatioZeroWithoutDeliveries(t *testing.T) {
	app, _ := newMetricsTestApp(t)
	body := scrapeMetrics(t, observabilityHandler(app))
	for _, line := range []string{"musicbot_cache_hit_ratio 0.0000"} {
		if !strings.Contains(body, line+"\n") {
			t.Fatalf("missing %q in:\n%s", line, body)
		}
	}
}

func TestMetricsAggregatesAreCachedForThirtySeconds(t *testing.T) {
	app, state := newMetricsTestApp(t)
	clock := time.Now()
	app.metrics.now = func() time.Time { return clock }
	handler := observabilityHandler(app)
	ctx := context.Background()
	state.increment(ctx, "cache_hits")

	first := scrapeMetrics(t, handler)
	if !strings.Contains(first, `musicbot_counter_total{name="cache_hits"} 1`+"\n") {
		t.Fatalf("first scrape:\n%s", first)
	}
	state.increment(ctx, "cache_hits")
	state.recordMediaStage(ctx, mediaStageSample{Stage: "transcode", Source: "youtube", ElapsedMS: 50, OK: true, CreatedAt: clock})
	clock = clock.Add(29 * time.Second)
	second := scrapeMetrics(t, handler)
	if !strings.Contains(second, `musicbot_counter_total{name="cache_hits"} 1`+"\n") || strings.Contains(second, `stage="transcode"`) {
		t.Fatalf("second scrape within TTL must return the cached snapshot:\n%s", second)
	}
	if app.metrics.computations != 1 {
		t.Fatalf("computations=%d want 1", app.metrics.computations)
	}
	clock = clock.Add(2 * time.Second)
	third := scrapeMetrics(t, handler)
	if !strings.Contains(third, `musicbot_counter_total{name="cache_hits"} 2`+"\n") || !strings.Contains(third, `musicbot_stage_ok_ratio{stage="transcode",source="youtube"} 1.0000`+"\n") {
		t.Fatalf("scrape after TTL must recompute:\n%s", third)
	}
	if app.metrics.computations != 2 {
		t.Fatalf("computations=%d want 2", app.metrics.computations)
	}
}

func TestMetricsEscapesLabelValues(t *testing.T) {
	app, state := newMetricsTestApp(t)
	ctx := context.Background()
	state.increment(ctx, "weird\"name\\with\nnewline")
	state.recordMediaStage(ctx, mediaStageSample{Stage: "stage\"quoted", Source: "src\\back", ElapsedMS: 5, OK: true, CreatedAt: time.Now()})
	body := scrapeMetrics(t, observabilityHandler(app))
	for _, line := range []string{
		`musicbot_counter_total{name="weird\"name\\with\nnewline"} 1`,
		`musicbot_stage_ok_ratio{stage="stage\"quoted",source="src\\back"} 1.0000`,
	} {
		if !strings.Contains(body, line+"\n") {
			t.Fatalf("missing %q in:\n%s", line, body)
		}
	}
}

func TestPrometheusLabelEscaping(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{name: "plain", in: "youtube", want: "youtube"},
		{name: "backslash", in: `a\b`, want: `a\\b`},
		{name: "quote", in: `a"b`, want: `a\"b`},
		{name: "newline", in: "a\nb", want: `a\nb`},
		{name: "combined", in: "\\\"\n", want: `\\\"\n`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := prometheusLabel(tc.in); got != tc.want {
				t.Fatalf("prometheusLabel(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}
