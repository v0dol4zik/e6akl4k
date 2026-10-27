package main

import (
	"context"
	"sync"
	"time"
)

type mediaOperation struct {
	id      string
	mode    string
	format  string
	quality string
	started time.Time
	readyAt time.Time
	first   sync.Once
}

type mediaOperationKey struct{}

func newMediaOperation(mode string) *mediaOperation {
	id, _ := randomID()
	return &mediaOperation{id: id, mode: mode, started: time.Now()}
}

func withMediaOperation(ctx context.Context, operation *mediaOperation) context.Context {
	if operation == nil {
		return ctx
	}
	return context.WithValue(ctx, mediaOperationKey{}, operation)
}

func mediaOperationFromContext(ctx context.Context) *mediaOperation {
	operation, _ := ctx.Value(mediaOperationKey{}).(*mediaOperation)
	return operation
}

func (operation *mediaOperation) delivered(ctx context.Context) {
	if operation == nil {
		return
	}
	operation.first.Do(func() {
		logMediaStageContext(ctx, "request_first_delivery", "telegram", operation.started, 0, true, "mode", operation.mode)
	})
}

func reporterContext(reporter *statusReporter) context.Context {
	if reporter != nil && reporter.ctx != nil {
		return reporter.ctx
	}
	return context.Background()
}
