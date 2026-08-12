package main

import (
	"context"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// updateDispatcher preserves Telegram update order for a user while allowing
// unrelated users to be handled concurrently.
type updateDispatcher struct {
	mu       sync.Mutex
	changed  *sync.Cond
	capacity int
	closed   bool
	queued   int
	active   int
	pending  map[int64][]dispatchJob
	ready    []int64
	busy     map[int64]bool
}

type dispatchJob struct {
	update   tgbotapi.Update
	complete func(bool)
}

func newUpdateDispatcher(capacity int) *updateDispatcher {
	if capacity < 1 {
		capacity = 1
	}
	d := &updateDispatcher{
		capacity: capacity,
		pending:  make(map[int64][]dispatchJob),
		busy:     make(map[int64]bool),
	}
	d.changed = sync.NewCond(&d.mu)
	return d
}

func (d *updateDispatcher) submit(ctx context.Context, update tgbotapi.Update, complete func(bool)) bool {
	key := updateOwnerKey(update)
	d.mu.Lock()
	defer d.mu.Unlock()
	stopWakeup := context.AfterFunc(ctx, func() {
		d.mu.Lock()
		d.changed.Broadcast()
		d.mu.Unlock()
	})
	defer stopWakeup()
	for !d.closed && d.queued >= d.capacity && ctx.Err() == nil {
		d.changed.Wait()
	}
	if d.closed || ctx.Err() != nil {
		return false
	}
	wasEmpty := len(d.pending[key]) == 0
	d.pending[key] = append(d.pending[key], dispatchJob{update: update, complete: complete})
	d.queued++
	if wasEmpty && !d.busy[key] {
		d.ready = append(d.ready, key)
		d.changed.Signal()
	}
	return true
}

func (d *updateDispatcher) take() (int64, dispatchJob, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for len(d.ready) == 0 {
		if d.closed && d.active == 0 {
			return 0, dispatchJob{}, false
		}
		d.changed.Wait()
	}
	key := d.ready[0]
	d.ready = d.ready[1:]
	queue := d.pending[key]
	job := queue[0]
	if len(queue) == 1 {
		delete(d.pending, key)
	} else {
		d.pending[key] = queue[1:]
	}
	d.queued--
	d.active++
	d.busy[key] = true
	d.changed.Broadcast()
	return key, job, true
}

func (d *updateDispatcher) done(key int64) {
	d.mu.Lock()
	delete(d.busy, key)
	if len(d.pending[key]) > 0 {
		d.ready = append(d.ready, key)
	}
	if d.active > 0 {
		d.active--
	}
	d.changed.Broadcast()
	d.mu.Unlock()
}

func (d *updateDispatcher) close() {
	d.mu.Lock()
	d.closed = true
	d.changed.Broadcast()
	d.mu.Unlock()
}

func updateOwnerKey(update tgbotapi.Update) int64 {
	if update.CallbackQuery != nil && (strings.HasPrefix(update.CallbackQuery.Data, "cancel_download:") || strings.HasPrefix(update.CallbackQuery.Data, "inline_cancel:")) {
		return -int64(update.UpdateID) - 1
	}
	switch {
	case update.Message != nil && update.Message.From != nil:
		return update.Message.From.ID
	case update.CallbackQuery != nil && update.CallbackQuery.From != nil:
		return update.CallbackQuery.From.ID
	case update.InlineQuery != nil && update.InlineQuery.From != nil:
		return update.InlineQuery.From.ID
	case update.ChosenInlineResult != nil && update.ChosenInlineResult.From != nil:
		return update.ChosenInlineResult.From.ID
	default:
		// Unknown updates must not block one another.
		return -int64(update.UpdateID) - 1
	}
}
