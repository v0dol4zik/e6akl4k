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
	ready        chan struct{}
	granted      bool
	continuation bool
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
	return g.acquireJob(ctx, queued, false)
}

// Continuations belong to an admitted request, which may yield its slot
// between tracks even with no queue for new requests. At most two workers per
// request and 256 update handlers bound this separate queue to 512 waiters.
func (g *jobGate) acquireContinuation(ctx context.Context, queued func(int)) (int, func(), error) {
	return g.acquireJob(ctx, queued, true)
}

func (g *jobGate) acquireJob(ctx context.Context, queued func(int), continuation bool) (int, func(), error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	g.mu.Lock()
	if g.active < g.workerLimit && len(g.waiters) == 0 {
		g.active++
		g.mu.Unlock()
		return 0, g.releaseFunc(), nil
	}
	waiting := 0
	for _, waiter := range g.waiters {
		if waiter.continuation == continuation {
			waiting++
		}
	}
	limit := g.queueLimit
	if continuation {
		limit = 512
	}
	if waiting >= limit {
		g.mu.Unlock()
		return 0, nil, errQueueFull
	}
	waiter := &gateWaiter{ready: make(chan struct{}), continuation: continuation}
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
	done   chan struct{}
	value  cachedAudio
	err    error
	refs   int
	cancel context.CancelFunc
}

type flightGroup struct {
	mu sync.Mutex
	m  map[string]*flightCall
}

func (g *flightGroup) do(ctx context.Context, key string, fn func() (cachedAudio, error)) (cachedAudio, error) {
	return g.doContext(ctx, key, func(context.Context) (cachedAudio, error) { return fn() })
}

func (g *flightGroup) doContext(ctx context.Context, key string, fn func(context.Context) (cachedAudio, error)) (cachedAudio, error) {
	if err := ctx.Err(); err != nil {
		return cachedAudio{}, err
	}
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*flightCall)
	}
	call := g.m[key]
	if call == nil {
		workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		call = &flightCall{done: make(chan struct{}), cancel: cancel}
		g.m[key] = call
		go func() {
			call.value, call.err = fn(workCtx)
			cancel()
			g.mu.Lock()
			if g.m[key] == call {
				delete(g.m, key)
			}
			close(call.done)
			g.mu.Unlock()
		}()
	}
	call.refs++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		call.refs--
		last := call.refs == 0
		if last {
			if g.m[key] == call {
				delete(g.m, key)
			}
			call.cancel()
		}
		g.mu.Unlock()
		if last {
			<-call.done
		}
	}()
	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		return cachedAudio{}, ctx.Err()
	}
}
