package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobGateBoundsWorkersAndQueue(t *testing.T) {
	gate := newJobGate(1, 1)
	_, releaseFirst, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondReady := make(chan func(), 1)
	queued := make(chan int, 1)
	go func() {
		_, release, acquireErr := gate.acquireNotify(context.Background(), func(position int) { queued <- position })
		if acquireErr == nil {
			secondReady <- release
		}
	}()
	select {
	case position := <-queued:
		if position != 1 {
			t.Fatalf("position=%d", position)
		}
	case <-time.After(time.Second):
		t.Fatal("job did not enter queue")
	}
	if _, _, err := gate.acquire(context.Background()); !errors.Is(err, errQueueFull) {
		t.Fatalf("third acquire err=%v", err)
	}
	releaseFirst()
	select {
	case release := <-secondReady:
		release()
	case <-time.After(time.Second):
		t.Fatal("queued job did not start")
	}
}

func TestRateLimiterWindow(t *testing.T) {
	limiter := newRateLimiter(2, 20*time.Millisecond)
	if ok, _ := limiter.allow(1); !ok {
		t.Fatal("first request rejected")
	}
	if ok, _ := limiter.allow(1); !ok {
		t.Fatal("second request rejected")
	}
	if ok, retry := limiter.allow(1); ok || retry <= 0 {
		t.Fatalf("third request ok=%v retry=%v", ok, retry)
	}
	time.Sleep(25 * time.Millisecond)
	if ok, _ := limiter.allow(1); !ok {
		t.Fatal("request after window rejected")
	}
}

func TestFlightGroupDeduplicatesWork(t *testing.T) {
	var group flightGroup
	var calls atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			entry, err := group.do(context.Background(), "same", func() (cachedAudio, error) {
				calls.Add(1)
				time.Sleep(20 * time.Millisecond)
				return cachedAudio{FileID: "one"}, nil
			})
			if err != nil || entry.FileID != "one" {
				t.Errorf("entry=%#v err=%v", entry, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("work called %d times", got)
	}
}
