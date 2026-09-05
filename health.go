package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type healthReport struct {
	Status          string `json:"status"`
	Database        string `json:"database"`
	YTDLP           string `json:"yt_dlp"`
	DiskFreeBytes   uint64 `json:"disk_free_bytes"`
	ActiveDownloads int    `json:"active_downloads"`
	QueuedDownloads int    `json:"queued_downloads"`
	ActiveLookups   int    `json:"active_lookups"`
	QueuedLookups   int    `json:"queued_lookups"`
	ActiveArchives  int    `json:"active_archives"`
	QueuedArchives  int    `json:"queued_archives"`
}

func (a *app) health(ctx context.Context) (healthReport, bool) {
	report := healthReport{Status: "ok", Database: "ok", YTDLP: "ok"}
	ok := true
	if a.store == nil || a.store.db.PingContext(ctx) != nil {
		report.Database = "error"
		ok = false
	}
	if info, err := os.Stat(a.downloader.bin); err != nil || !info.Mode().IsRegular() {
		report.YTDLP = "error"
		ok = false
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Clean(a.downloader.downloadDir), &stat); err == nil {
		report.DiskFreeBytes = stat.Bavail * uint64(stat.Bsize)
		threshold := a.cfg.DiskWarningBytes
		if threshold <= 0 {
			threshold = 100 * 1024 * 1024
		}
		if report.DiskFreeBytes < uint64(threshold) {
			ok = false
		}
	} else {
		ok = false
	}
	report.ActiveDownloads, report.QueuedDownloads, _ = a.downloads.snapshot()
	report.ActiveLookups, report.QueuedLookups, _ = a.lookups.snapshot()
	report.ActiveArchives, report.QueuedArchives, _ = a.archives.snapshot()
	if !ok {
		report.Status = "degraded"
	}
	return report, ok
}

func startHTTPServer(ctx context.Context, a *app, addr string) *http.Server {
	server := &http.Server{Addr: addr, Handler: observabilityHandler(a), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP observability server: %v", err)
		}
	}()
	return server
}

func observabilityHandler(a *app) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		report, ok := a.health(checkCtx)
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(report)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := a.metrics.get(r.Context(), a.store)
		if err != nil {
			http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		da, dw, _ := a.downloads.snapshot()
		la, lw, _ := a.lookups.snapshot()
		aa, aw, _ := a.archives.snapshot()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		stats := snapshot.stats
		_, _ = fmt.Fprintf(w, "musicbot_downloads_total{result=\"ok\"} %d\nmusicbot_downloads_total{result=\"partial\"} %d\nmusicbot_downloads_total{result=\"failed\"} %d\nmusicbot_downloads_total{result=\"cancelled\"} %d\nmusicbot_youtube_cookie_errors_total %d\nmusicbot_cache_hits_total %d\nmusicbot_searches_total %d\nmusicbot_search_octave_total %d\nmusicbot_search_youtube_fallback_total %d\nmusicbot_rate_limited_total %d\nmusicbot_queue_rejected_total %d\nmusicbot_users %d\nmusicbot_cached_tracks %d\nmusicbot_downloads_active %d\nmusicbot_downloads_queued %d\nmusicbot_lookups_active %d\nmusicbot_lookups_queued %d\nmusicbot_archives_active %d\nmusicbot_archives_queued %d\n",
			stats.DownloadsOK, stats.DownloadsPartial, stats.DownloadsFailed, stats.Cancelled, stats.CookieErrors, stats.CacheHits, stats.Searches, stats.SearchOctave, stats.SearchYouTubeFallback, stats.RateLimited, stats.QueueRejected, stats.UniqueUsers, stats.CachedTracks, da, dw, la, lw, aa, aw)
		_, _ = fmt.Fprint(w, "# HELP musicbot_counter_total persistent counters from the SQLite counters table\n# TYPE musicbot_counter_total counter\n")
		for _, name := range snapshot.counterNames {
			_, _ = fmt.Fprintf(w, "musicbot_counter_total{name=\"%s\"} %d\n", prometheusLabel(name), snapshot.counters[name])
		}
		_, _ = fmt.Fprint(w, "# HELP musicbot_cache_hit_ratio share of deliveries served from the Telegram file_id cache\n# TYPE musicbot_cache_hit_ratio gauge\n")
		_, _ = fmt.Fprintf(w, "musicbot_cache_hit_ratio %.4f\n", snapshot.cacheHitRatio)
		for _, stage := range snapshot.modeStages {
			_, _ = fmt.Fprintf(w, "musicbot_media_stage_samples{stage=\"%s\",source=\"%s\",mode=\"%s\"} %d\nmusicbot_media_stage_success_ratio{stage=\"%s\",source=\"%s\",mode=\"%s\"} %.4f\nmusicbot_media_stage_p95_seconds{stage=\"%s\",source=\"%s\",mode=\"%s\"} %.6f\n",
				prometheusLabel(stage.Stage), prometheusLabel(stage.Source), prometheusLabel(stage.Mode), stage.Count,
				prometheusLabel(stage.Stage), prometheusLabel(stage.Source), prometheusLabel(stage.Mode), float64(stage.OK)/float64(max(stage.Count, 1)),
				prometheusLabel(stage.Stage), prometheusLabel(stage.Source), prometheusLabel(stage.Mode), stage.P95.Seconds())
		}
		_, _ = fmt.Fprint(w, "# HELP musicbot_stage_seconds stage latency quantiles over the last hour\n# TYPE musicbot_stage_seconds gauge\n# HELP musicbot_stage_ok_ratio share of successful stage samples over the last hour\n# TYPE musicbot_stage_ok_ratio gauge\n")
		for _, stage := range snapshot.stages {
			stageLabel, sourceLabel := prometheusLabel(stage.Stage), prometheusLabel(stage.Source)
			_, _ = fmt.Fprintf(w, "musicbot_stage_seconds{stage=\"%s\",source=\"%s\",quantile=\"0.5\"} %.6f\nmusicbot_stage_seconds{stage=\"%s\",source=\"%s\",quantile=\"0.95\"} %.6f\nmusicbot_stage_ok_ratio{stage=\"%s\",source=\"%s\"} %.4f\n",
				stageLabel, sourceLabel, stage.P50.Seconds(), stageLabel, sourceLabel, stage.P95.Seconds(), stageLabel, sourceLabel, float64(stage.OK)/float64(max(stage.Count, 1)))
		}
		_, _ = fmt.Fprint(w, "# HELP musicbot_octave_fast_path_ratio share of Octave deliveries in the last hour that used the Telegram remote-URL fast path\n# TYPE musicbot_octave_fast_path_ratio gauge\n")
		_, _ = fmt.Fprintf(w, "musicbot_octave_fast_path_ratio %.4f\nmusicbot_octave_fast_path_samples{path=\"remote\"} %d\nmusicbot_octave_fast_path_samples{path=\"local\"} %d\n", snapshot.fastPathRatio, snapshot.fastPathRemote, snapshot.fastPathLocal)
		_, _ = fmt.Fprint(w, "# HELP musicbot_octave_circuit_state Octave remote-URL circuit breaker: 0=closed, 1=open, 2=half-open\n# TYPE musicbot_octave_circuit_state gauge\n")
		_, _ = fmt.Fprintf(w, "musicbot_octave_circuit_state %d\n", circuitStateValue(a.octaveRemote.state()))
	})
	return mux
}

// prometheusLabel escapes a label value for the Prometheus text exposition
// format: backslash, double quote and newline are the only special characters.
func prometheusLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

// circuitStateValue maps the breaker state to the documented gauge encoding.
func circuitStateValue(state circuitState) int {
	switch state {
	case circuitOpen:
		return 1
	case circuitHalfOpen:
		return 2
	default:
		return 0
	}
}

const metricsCacheTTL = 30 * time.Second

// metricsSnapshot holds every SQLite-derived value exported on /metrics.
type metricsSnapshot struct {
	stats          statsSnapshot
	counters       map[string]int64
	counterNames   []string
	stages         []stagePerformance
	modeStages     []stagePerformance
	cacheHitRatio  float64
	fastPathRatio  float64
	fastPathRemote int
	fastPathLocal  int
}

// metricsCache memoises the SQLite aggregates for metricsCacheTTL so frequent
// scrapes do not re-read the samples table on every request.
type metricsCache struct {
	mu           sync.Mutex
	ttl          time.Duration
	now          func() time.Time
	snapshot     metricsSnapshot
	computedAt   time.Time
	computations int
}

func (c *metricsCache) get(ctx context.Context, state *store) (metricsSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	ttl := c.ttl
	if ttl <= 0 {
		ttl = metricsCacheTTL
	}
	current := now()
	if !c.computedAt.IsZero() && current.Sub(c.computedAt) < ttl {
		return c.snapshot, nil
	}
	if state == nil {
		return metricsSnapshot{}, errors.New("store unavailable")
	}
	c.computations++
	snapshot, err := computeMetricsSnapshot(ctx, state, current.Add(-time.Hour))
	if err != nil {
		if c.computedAt.IsZero() {
			return metricsSnapshot{}, err
		}
		return c.snapshot, nil
	}
	c.snapshot, c.computedAt = snapshot, current
	return snapshot, nil
}

func computeMetricsSnapshot(ctx context.Context, state *store, since time.Time) (metricsSnapshot, error) {
	stats, err := state.stats(ctx)
	if err != nil {
		return metricsSnapshot{}, err
	}
	counters, err := state.counters(ctx)
	if err != nil {
		return metricsSnapshot{}, err
	}
	samples, err := state.mediaStageSamples(ctx, since)
	if err != nil {
		return metricsSnapshot{}, err
	}
	snapshot := metricsSnapshot{stats: stats, counters: counters, counterNames: make([]string, 0, len(counters))}
	for name := range counters {
		snapshot.counterNames = append(snapshot.counterNames, name)
	}
	sort.Strings(snapshot.counterNames)
	snapshot.stages = aggregateStagePerformance(samples, false)
	snapshot.modeStages = aggregateStagePerformance(samples, true)
	if deliveries := stats.CacheHits + stats.DownloadsOK + stats.DownloadsPartial + stats.DownloadsFailed; deliveries > 0 {
		snapshot.cacheHitRatio = float64(stats.CacheHits) / float64(deliveries)
	}
	snapshot.fastPathRemote, snapshot.fastPathLocal = octaveFastPathCounts(samples)
	if total := snapshot.fastPathRemote + snapshot.fastPathLocal; total > 0 {
		snapshot.fastPathRatio = float64(snapshot.fastPathRemote) / float64(total)
	}
	return snapshot, nil
}

// octaveFastPathCounts splits Octave track deliveries into the Telegram
// remote-URL fast path (telegram_upload with mode=remote_url) and local
// downloads through the bot (source_download from octave).
func octaveFastPathCounts(samples []mediaStageSample) (remote, local int) {
	for _, sample := range samples {
		switch {
		case sample.Stage == "telegram_upload" && sample.Mode == "remote_url":
			remote++
		case sample.Stage == "source_download" && sample.Source == "octave":
			local++
		}
	}
	return remote, local
}

func (a *app) startDiskMonitor(ctx context.Context) {
	if len(a.administratorIDs(ctx)) == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(a.cfg.DiskCheckInterval)
		defer ticker.Stop()
		warned := false
		for {
			var stat syscall.Statfs_t
			err := syscall.Statfs(filepath.Clean(a.downloader.downloadDir), &stat)
			free := int64(stat.Bavail * uint64(stat.Bsize))
			low := err == nil && free < a.cfg.DiskWarningBytes
			if low && !warned {
				for _, adminID := range a.administratorIDs(ctx) {
					lang := a.langOrDefault(adminID)
					a.sendText(adminID, tr("admin_disk_warning", lang, "free", humanSize(free, lang)), "", nil)
				}
			}
			warned = low
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
