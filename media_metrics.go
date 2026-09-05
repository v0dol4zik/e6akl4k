package main

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type mediaStageSample struct {
	Stage     string
	Source    string
	ElapsedMS int64
	SizeBytes int64
	OK        bool
	Mode      string
	Format    string
	Quality   string
	CreatedAt time.Time
}

type stagePerformance struct {
	Stage      string
	Source     string
	Mode       string
	Count      int
	OK         int
	P50        time.Duration
	P95        time.Duration
	Bytes      int64
	Throughput int64
}

type mediaMetricsRecorder struct {
	samples chan mediaStageSample
	dropped atomic.Int64
}

var activeMediaRecorder atomic.Pointer[mediaMetricsRecorder]

func startMediaMetricsRecorder(ctx context.Context, state *store) *mediaMetricsRecorder {
	if state == nil {
		return nil
	}
	recorder := &mediaMetricsRecorder{samples: make(chan mediaStageSample, 1024)}
	activeMediaRecorder.Store(recorder)
	go func() {
		for {
			select {
			case sample := <-recorder.samples:
				state.recordMediaStage(ctx, sample)
			case <-ctx.Done():
				return
			}
		}
	}()
	return recorder
}

func logMediaStage(stage, source string, started time.Time, size int64, ok bool, extra ...any) {
	elapsed := time.Since(started)
	attributes := []any{
		"stage", stage,
		"source", source,
		"elapsed_ms", elapsed.Milliseconds(),
		"ok", ok,
	}
	sample := mediaStageSample{Stage: stage, Source: source, ElapsedMS: elapsed.Milliseconds(), SizeBytes: size, OK: ok, CreatedAt: time.Now()}
	if size > 0 {
		attributes = append(attributes, "size_bytes", size)
		if elapsed > 0 {
			attributes = append(attributes, "bytes_per_second", int64(float64(size)/elapsed.Seconds()))
		}
	}
	for index := 0; index+1 < len(extra); index += 2 {
		key, _ := extra[index].(string)
		value, _ := extra[index+1].(string)
		switch key {
		case "mode":
			sample.Mode = value
		case "format":
			sample.Format = value
		case "quality":
			sample.Quality = value
		}
	}
	attributes = append(attributes, extra...)
	slog.Info("media_stage", attributes...)
	if recorder := activeMediaRecorder.Load(); recorder != nil {
		select {
		case recorder.samples <- sample:
		default:
			recorder.dropped.Add(1)
		}
	}
}

func (s *store) recordMediaStage(ctx context.Context, sample mediaStageSample) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO media_stage_samples(stage,source,elapsed_ms,size_bytes,ok,mode,format,quality,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		sample.Stage, sample.Source, sample.ElapsedMS, sample.SizeBytes, sample.OK, sample.Mode, sample.Format, sample.Quality, sample.CreatedAt.Unix())
}

// mediaPerformance aggregates the last hour (or any window) of stage samples
// grouped by stage, source and mode; it backs /perf and the per-mode metrics.
func (s *store) mediaPerformance(ctx context.Context, since time.Time) ([]stagePerformance, error) {
	samples, err := s.mediaStageSamples(ctx, since)
	if err != nil {
		return nil, err
	}
	return aggregateStagePerformance(samples, true), nil
}

// mediaStageSamples loads bounded raw samples recorded at or after since.
func (s *store) mediaStageSamples(ctx context.Context, since time.Time) ([]mediaStageSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stage,source,mode,elapsed_ms,size_bytes,ok FROM media_stage_samples WHERE created_at>=? ORDER BY stage,source,mode,elapsed_ms LIMIT 50000`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var samples []mediaStageSample
	for rows.Next() {
		var sample mediaStageSample
		if err := rows.Scan(&sample.Stage, &sample.Source, &sample.Mode, &sample.ElapsedMS, &sample.SizeBytes, &sample.OK); err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

// aggregateStagePerformance buckets samples by stage and source, additionally
// by mode when byMode is set, and computes counts, P50/P95 and throughput.
func aggregateStagePerformance(samples []mediaStageSample, byMode bool) []stagePerformance {
	type bucket struct {
		stage, source, mode string
		elapsed             []int64
		ok                  int
		bytes               int64
	}
	buckets := make(map[string]*bucket)
	for _, sample := range samples {
		mode := sample.Mode
		if !byMode {
			mode = ""
		}
		key := sample.Stage + "\x00" + sample.Source + "\x00" + mode
		item := buckets[key]
		if item == nil {
			item = &bucket{stage: sample.Stage, source: sample.Source, mode: mode}
			buckets[key] = item
		}
		item.elapsed = append(item.elapsed, sample.ElapsedMS)
		item.bytes += sample.SizeBytes
		if sample.OK {
			item.ok++
		}
	}
	result := make([]stagePerformance, 0, len(buckets))
	for _, bucket := range buckets {
		sort.Slice(bucket.elapsed, func(i, j int) bool { return bucket.elapsed[i] < bucket.elapsed[j] })
		count := len(bucket.elapsed)
		p50, p95 := percentile(bucket.elapsed, 0.50), percentile(bucket.elapsed, 0.95)
		var totalMS int64
		for _, elapsed := range bucket.elapsed {
			totalMS += elapsed
		}
		throughput := int64(0)
		if totalMS > 0 {
			throughput = bucket.bytes * 1000 / totalMS
		}
		result = append(result, stagePerformance{Stage: bucket.stage, Source: bucket.source, Mode: bucket.mode, Count: count, OK: bucket.ok, P50: time.Duration(p50) * time.Millisecond, P95: time.Duration(p95) * time.Millisecond, Bytes: bucket.bytes, Throughput: throughput})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].P95 != result[j].P95 {
			return result[i].P95 > result[j].P95
		}
		if result[i].Stage != result[j].Stage {
			return strings.Compare(result[i].Stage, result[j].Stage) < 0
		}
		if result[i].Source != result[j].Source {
			return strings.Compare(result[i].Source, result[j].Source) < 0
		}
		return strings.Compare(result[i].Mode, result[j].Mode) < 0
	})
	return result
}

func percentile(sorted []int64, fraction float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * fraction)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
