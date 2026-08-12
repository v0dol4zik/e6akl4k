package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errQueueFull = errors.New("очередь заполнена")

type jobGate struct {
	workers chan struct{}
	queue   chan struct{}
	mu      sync.Mutex
	waiting int
}

func newJobGate(workers, queue int) *jobGate {
	return &jobGate{workers: make(chan struct{}, workers), queue: make(chan struct{}, queue)}
}

func (g *jobGate) acquire(ctx context.Context) (int, func(), error) {
	return g.acquireNotify(ctx, nil)
}

func (g *jobGate) acquireNotify(ctx context.Context, queued func(int)) (int, func(), error) {
	select {
	case g.workers <- struct{}{}:
		return 0, func() { <-g.workers }, nil
	default:
	}
	position := 0
	select {
	case g.queue <- struct{}{}:
		g.mu.Lock()
		g.waiting++
		position = g.waiting
		g.mu.Unlock()
		if queued != nil {
			queued(position)
		}
	default:
		return 0, nil, errQueueFull
	}
	select {
	case g.workers <- struct{}{}:
		<-g.queue
		g.mu.Lock()
		g.waiting--
		g.mu.Unlock()
		return position, func() { <-g.workers }, nil
	case <-ctx.Done():
		<-g.queue
		g.mu.Lock()
		g.waiting--
		g.mu.Unlock()
		return position, nil, ctx.Err()
	}
}

func (g *jobGate) snapshot() (active, waiting, capacity int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.workers), g.waiting, cap(g.workers)
}

type rateBucket struct {
	started time.Time
	count   int
}

type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[int64]rateBucket
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, buckets: make(map[int64]rateBucket)}
}

func (r *rateLimiter) allow(userID int64) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if len(r.buckets) > 10000 {
		for id, old := range r.buckets {
			if now.Sub(old.started) >= r.window {
				delete(r.buckets, id)
			}
		}
	}
	bucket := r.buckets[userID]
	if bucket.started.IsZero() || now.Sub(bucket.started) >= r.window {
		r.buckets[userID] = rateBucket{started: now, count: 1}
		return true, 0
	}
	if bucket.count >= r.limit {
		return false, r.window - now.Sub(bucket.started)
	}
	bucket.count++
	r.buckets[userID] = bucket
	return true, 0
}

type flightCall struct {
	done  chan struct{}
	value cachedAudio
	err   error
}

type flightGroup struct {
	mu sync.Mutex
	m  map[string]*flightCall
}

func (g *flightGroup) do(ctx context.Context, key string, fn func() (cachedAudio, error)) (cachedAudio, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*flightCall)
	}
	if call := g.m[key]; call != nil {
		g.mu.Unlock()
		select {
		case <-call.done:
			return call.value, call.err
		case <-ctx.Done():
			return cachedAudio{}, ctx.Err()
		}
	}
	call := &flightCall{done: make(chan struct{})}
	g.m[key] = call
	g.mu.Unlock()
	call.value, call.err = fn()
	g.mu.Lock()
	delete(g.m, key)
	close(call.done)
	g.mu.Unlock()
	return call.value, call.err
}
