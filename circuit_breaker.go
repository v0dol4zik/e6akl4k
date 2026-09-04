package main

import (
	"sync"
	"time"
)

type circuitState string

const (
	circuitClosed   circuitState = "closed"
	circuitOpen     circuitState = "open"
	circuitHalfOpen circuitState = "half_open"
)

type circuitBreaker struct {
	mu             sync.Mutex
	threshold      int
	failureWindow  time.Duration
	openFor        time.Duration
	now            func() time.Time
	failures       []time.Time
	openedAt       time.Time
	halfOpenActive bool
}

func newCircuitBreaker(threshold int, failureWindow, openFor time.Duration) *circuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	return &circuitBreaker{threshold: threshold, failureWindow: failureWindow, openFor: openFor, now: time.Now}
}

func (b *circuitBreaker) allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if !b.openedAt.IsZero() {
		if now.Sub(b.openedAt) < b.openFor {
			return false
		}
		if b.halfOpenActive {
			return false
		}
		b.halfOpenActive = true
		return true
	}
	b.prune(now)
	return true
}

func (b *circuitBreaker) success() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.failures = nil
	b.openedAt = time.Time{}
	b.halfOpenActive = false
	b.mu.Unlock()
}

func (b *circuitBreaker) cancelProbe() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.halfOpenActive = false
	b.mu.Unlock()
}

func (b *circuitBreaker) failure() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if !b.openedAt.IsZero() && b.halfOpenActive {
		b.openedAt = now
		b.halfOpenActive = false
		return
	}
	b.prune(now)
	b.failures = append(b.failures, now)
	if len(b.failures) >= b.threshold {
		b.openedAt = now
		b.halfOpenActive = false
	}
}

func (b *circuitBreaker) prune(now time.Time) {
	cutoff := now.Add(-b.failureWindow)
	kept := b.failures[:0]
	for _, failure := range b.failures {
		if !failure.Before(cutoff) {
			kept = append(kept, failure)
		}
	}
	b.failures = kept
}

func (b *circuitBreaker) state() circuitState {
	if b == nil {
		return circuitClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openedAt.IsZero() {
		return circuitClosed
	}
	if b.now().Sub(b.openedAt) >= b.openFor {
		return circuitHalfOpen
	}
	return circuitOpen
}
