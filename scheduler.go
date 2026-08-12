package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errQueueFull = errors.New("очередь заполнена")

type jobGate struct {
	mu          sync.Mutex
	workerLimit int
	queueLimit  int
	active      int
	waiters     []*gateWaiter
}

type gateWaiter struct {
	ready   chan struct{}
	granted bool
}

func newJobGate(workers, queue int) *jobGate {
	if workers < 1 {
		workers = 1
	}
	if queue < 0 {
		queue = 0
	}
	return &jobGate{workerLimit: workers, queueLimit: queue}
}

func (g *jobGate) acquire(ctx context.Context) (int, func(), error) {
	return g.acquireNotify(ctx, nil)
}

func (g *jobGate) acquireNotify(ctx context.Context, queued func(int)) (int, func(), error) {
	g.mu.Lock()
	if g.active < g.workerLimit && len(g.waiters) == 0 {
		g.active++
		g.mu.Unlock()
		return 0, g.releaseFunc(), nil
	}
	if len(g.waiters) >= g.queueLimit {
		g.mu.Unlock()
		return 0, nil, errQueueFull
	}
	waiter := &gateWaiter{ready: make(chan struct{})}
	g.waiters = append(g.waiters, waiter)
	position := len(g.waiters)
	g.mu.Unlock()
	if queued != nil {
		queued(position)
	}
	select {
	case <-waiter.ready:
		return position, g.releaseFunc(), nil
	case <-ctx.Done():
		g.mu.Lock()
		if waiter.granted {
			g.active--
			g.grantNextLocked()
		} else {
			for i, queuedWaiter := range g.waiters {
				if queuedWaiter == waiter {
					g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
					break
				}
			}
		}
		g.mu.Unlock()
		return position, nil, ctx.Err()
	}
}

func (g *jobGate) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if g.active > 0 {
				g.active--
			}
			g.grantNextLocked()
			g.mu.Unlock()
		})
	}
}

func (g *jobGate) grantNextLocked() {
	if g.active >= g.workerLimit || len(g.waiters) == 0 {
		return
	}
	waiter := g.waiters[0]
	g.waiters = g.waiters[1:]
	g.active++
	waiter.granted = true
	close(waiter.ready)
}

func (g *jobGate) snapshot() (active, waiting, capacity int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active, len(g.waiters), g.workerLimit
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
