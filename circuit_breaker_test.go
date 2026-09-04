package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestCircuitBreakerTransitions(t *testing.T) {
	now := time.Unix(1000, 0)
	breaker := newCircuitBreaker(3, 5*time.Minute, 10*time.Minute)
	breaker.now = func() time.Time { return now }
	for index := 0; index < 3; index++ {
		if !breaker.allow() {
			t.Fatalf("closed breaker rejected attempt %d", index)
		}
		breaker.failure()
	}
	if breaker.state() != circuitOpen || breaker.allow() {
		t.Fatalf("breaker state=%s allow=%v", breaker.state(), breaker.allow())
	}
	now = now.Add(10 * time.Minute)
	if breaker.state() != circuitHalfOpen || !breaker.allow() || breaker.allow() {
		t.Fatal("half-open breaker did not allow exactly one probe")
	}
	breaker.success()
	if breaker.state() != circuitClosed || !breaker.allow() {
		t.Fatal("successful probe did not close breaker")
	}
}

func TestOctaveRemoteFailureClassification(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errQueueFull, &tgbotapi.Error{Code: http.StatusTooManyRequests}} {
		if countOctaveRemoteFailure(err) {
			t.Errorf("non-definitive failure was counted: %v", err)
		}
	}
	if !countOctaveRemoteFailure(errors.New("remote fetch failed")) {
		t.Fatal("definitive failure was ignored")
	}
}

func TestCircuitBreakerForgetsOldFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	breaker := newCircuitBreaker(2, time.Minute, time.Minute)
	breaker.now = func() time.Time { return now }
	breaker.failure()
	now = now.Add(2 * time.Minute)
	breaker.failure()
	if breaker.state() != circuitClosed {
		t.Fatal("old failure incorrectly opened breaker")
	}
}
