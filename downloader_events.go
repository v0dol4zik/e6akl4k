package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The slash prevents yt-dlp's --print shorthand from treating this literal
// marker as a metadata field (which would print "NA").
const mediaEventPrefix = "__MUSICBOT_EVENT__/"

// yt-dlp's own hooks delimit transfer and postprocessing without logging its
// URLs or retaining progress output. Each process handles its playlist in order.
type downloadEvents struct {
	mu                             sync.Mutex
	ctx                            context.Context
	source, format, quality        string
	preparedAt, transferAt, postAt time.Time
	size                           int64
	line                           []byte
}

// Pipes may split a line at any byte. Each pipe needs its own line buffer;
// only complete events may enter the shared state machine.
type downloadEventStream struct {
	events *downloadEvents
	line   []byte
}

func (e *downloadEvents) stream() *downloadEventStream { return &downloadEventStream{events: e} }

func (s *downloadEventStream) Write(p []byte) (int, error) {
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	for _, b := range p {
		if b == '\n' || b == '\r' {
			s.events.event(strings.TrimSpace(string(s.line)))
			s.line = s.line[:0]
		} else if len(s.line) < 4096 {
			s.line = append(s.line, b)
		}
	}
	return len(p), nil
}

func (s *downloadEventStream) close() {
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	if len(s.line) > 0 {
		s.events.event(strings.TrimSpace(string(s.line)))
		s.line = nil
	}
}

func newDownloadEvents(ctx context.Context, args []string) *downloadEvents {
	e := &downloadEvents{ctx: ctx, preparedAt: time.Now()}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--":
			e.source = sourceHost(args[i+1])
		case "--audio-format":
			e.format = args[i+1]
		case "--audio-quality":
			e.quality = strings.TrimSuffix(args[i+1], "K")
			if e.quality == "0" {
				e.quality = "best"
			}
		}
	}
	return e
}

func (e *downloadEvents) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, b := range p {
		if b == '\n' || b == '\r' {
			e.event(strings.TrimSpace(string(e.line)))
			e.line = e.line[:0]
		} else if len(e.line) < 4096 {
			e.line = append(e.line, b)
		}
	}
	return len(p), nil
}

func (e *downloadEvents) stage(name string, at time.Time, size int64, ok bool) {
	if !at.IsZero() {
		logMediaStageContext(e.ctx, name, e.source, at, size, ok, "format", e.format, "quality", e.quality)
	}
}

func (e *downloadEvents) event(line string) {
	if !strings.HasPrefix(line, mediaEventPrefix) {
		return
	}
	event := strings.TrimPrefix(line, mediaEventPrefix)
	switch {
	case event == "transfer":
		e.stage("source_extract", e.preparedAt, 0, true)
		e.preparedAt = time.Time{}
		e.transferAt = time.Now()
	case strings.HasPrefix(event, "finished:"):
		size, err := strconv.ParseInt(strings.TrimPrefix(event, "finished:"), 10, 64)
		if err != nil || size < 0 || e.transferAt.IsZero() {
			return
		}
		e.size = size
		e.stage("source_transfer", e.transferAt, e.size, true)
		e.transferAt = time.Time{}
		e.postAt = time.Now()
	case event == "complete":
		e.stage("source_postprocess", e.postAt, 0, true)
		e.postAt = time.Time{}
		e.preparedAt = time.Now()
	}
}

func (e *downloadEvents) close(ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(bytes.TrimSpace(e.line)) > 0 {
		e.event(string(e.line))
	}
	if !ok {
		e.stage("source_extract", e.preparedAt, 0, false)
		e.stage("source_transfer", e.transferAt, e.size, false)
		e.stage("source_postprocess", e.postAt, 0, false)
	}
}
