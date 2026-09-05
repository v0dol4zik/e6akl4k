package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type healthReport struct {
	Status          string `json:"status"`
	Database        string `json:"database"`
	YTDLP           string `json:"yt_dlp"`
	YTDLPCookies    string `json:"yt_dlp_cookies"`
	DiskFreeBytes   uint64 `json:"disk_free_bytes"`
	ActiveDownloads int    `json:"active_downloads"`
	QueuedDownloads int    `json:"queued_downloads"`
	ActiveLookups   int    `json:"active_lookups"`
	QueuedLookups   int    `json:"queued_lookups"`
	ActiveArchives  int    `json:"active_archives"`
	QueuedArchives  int    `json:"queued_archives"`
}

func (a *app) health(ctx context.Context) (healthReport, bool) {
	report := healthReport{Status: "ok", Database: "ok", YTDLP: "ok", YTDLPCookies: "ok"}
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
	report.YTDLPCookies = a.cookieStatus(ctx)
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
		stats, err := a.store.stats(r.Context())
		if err != nil {
			http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		da, dw, _ := a.downloads.snapshot()
		la, lw, _ := a.lookups.snapshot()
		aa, aw, _ := a.archives.snapshot()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "musicbot_downloads_total{result=\"ok\"} %d\nmusicbot_downloads_total{result=\"partial\"} %d\nmusicbot_downloads_total{result=\"failed\"} %d\nmusicbot_downloads_total{result=\"cancelled\"} %d\nmusicbot_youtube_cookie_errors_total %d\nmusicbot_cache_hits_total %d\nmusicbot_searches_total %d\nmusicbot_rate_limited_total %d\nmusicbot_queue_rejected_total %d\nmusicbot_users %d\nmusicbot_cached_tracks %d\nmusicbot_downloads_active %d\nmusicbot_downloads_queued %d\nmusicbot_lookups_active %d\nmusicbot_lookups_queued %d\nmusicbot_archives_active %d\nmusicbot_archives_queued %d\n",
			stats.DownloadsOK, stats.DownloadsPartial, stats.DownloadsFailed, stats.Cancelled, stats.CookieErrors, stats.CacheHits, stats.Searches, stats.RateLimited, stats.QueueRejected, stats.UniqueUsers, stats.CachedTracks, da, dw, la, lw, aa, aw)
		if performance, perfErr := a.store.mediaPerformance(r.Context(), time.Now().Add(-time.Hour)); perfErr == nil {
			for _, stage := range performance {
				_, _ = fmt.Fprintf(w, "musicbot_media_stage_samples{stage=\"%s\",source=\"%s\",mode=\"%s\"} %d\nmusicbot_media_stage_success_ratio{stage=\"%s\",source=\"%s\",mode=\"%s\"} %.4f\nmusicbot_media_stage_p95_seconds{stage=\"%s\",source=\"%s\",mode=\"%s\"} %.6f\n",
					prometheusLabel(stage.Stage), prometheusLabel(stage.Source), prometheusLabel(stage.Mode), stage.Count,
					prometheusLabel(stage.Stage), prometheusLabel(stage.Source), prometheusLabel(stage.Mode), float64(stage.OK)/float64(max(stage.Count, 1)),
					prometheusLabel(stage.Stage), prometheusLabel(stage.Source), prometheusLabel(stage.Mode), stage.P95.Seconds())
			}
		}
		circuitValue := 0
		switch a.octaveRemote.state() {
		case circuitHalfOpen:
			circuitValue = 1
		case circuitOpen:
			circuitValue = 2
		}
		_, _ = fmt.Fprintf(w, "musicbot_octave_remote_circuit_state %d\n", circuitValue)
	})
	return mux
}

func prometheusLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
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
