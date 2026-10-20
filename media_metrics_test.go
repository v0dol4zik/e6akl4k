package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMediaPerformanceSeparatesQualityAndFailures(t *testing.T) {
	app, state := newMetricsTestApp(t)
	ctx := context.Background()
	for _, sample := range []mediaStageSample{
		{Stage: "source_download", Source: "youtube", Format: "mp3", Quality: "best", ElapsedMS: 10, OK: true},
		{Stage: "source_download", Source: "youtube", Format: "mp3", Quality: "best", ElapsedMS: 30, OK: true},
		{Stage: "source_download", Source: "youtube", Format: "mp3", Quality: "best", ElapsedMS: 900, OK: false},
		{Stage: "source_download", Source: "youtube", Format: "mp3", Quality: "320", ElapsedMS: 100, OK: true},
	} {
		sample.CreatedAt = time.Now()
		state.recordMediaStage(ctx, sample)
	}
	rows, err := state.mediaPerformance(ctx, time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.Quality == "best" && (row.P95 != 30*time.Millisecond || row.FailedP95 != 900*time.Millisecond || row.Count != 3 || row.OK != 2) {
			t.Fatalf("best: %+v", row)
		}
	}
	body := scrapeMetrics(t, observabilityHandler(app))
	if !strings.Contains(body, `quality="best"} 0.030000`) || !strings.Contains(body, `quality="320"} 0.100000`) {
		t.Fatal(body)
	}
}

func TestDownloadEventsAreBoundedAndSeparateStages(t *testing.T) {
	e := newDownloadEvents(context.Background(), []string{"--audio-format", "mp3", "--audio-quality", "0", "--", "https://youtu.be/abcdefghijk"})
	for _, p := range []string{strings.Repeat("x", 10000) + "\n", mediaEventPrefix + "trans", "fer\n"} {
		_, _ = e.Write([]byte(p))
	}
	if len(e.line) != 0 || e.transferAt.IsZero() || !e.preparedAt.IsZero() {
		t.Fatalf("after extraction: %+v", e)
	}
	_, _ = e.Write([]byte(mediaEventPrefix + "finished:not-a-size\n"))
	if e.transferAt.IsZero() {
		t.Fatal("invalid progress completed a stage")
	}
	_, _ = e.Write([]byte(mediaEventPrefix + "finished:1024\n"))
	if e.postAt.IsZero() || !e.transferAt.IsZero() || e.size != 1024 {
		t.Fatalf("after transfer: %+v", e)
	}
	_, _ = e.Write([]byte(mediaEventPrefix + "complete\n"))
	if !e.postAt.IsZero() || e.preparedAt.IsZero() {
		t.Fatal("postprocessing did not finish")
	}
}
