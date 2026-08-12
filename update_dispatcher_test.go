package main

import (
	"context"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestUpdateDispatcherPreservesUserOrder(t *testing.T) {
	dispatcher := newUpdateDispatcher(4)
	first := tgbotapi.Update{UpdateID: 1, Message: &tgbotapi.Message{From: &tgbotapi.User{ID: 42}}}
	second := tgbotapi.Update{UpdateID: 2, CallbackQuery: &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 42}}}
	if !dispatcher.submit(context.Background(), first, nil) || !dispatcher.submit(context.Background(), second, nil) {
		t.Fatal("submit rejected")
	}
	key, job, ok := dispatcher.take()
	if !ok || job.update.UpdateID != 1 {
		t.Fatalf("first job=%#v ok=%v", job, ok)
	}
	ready := make(chan dispatchJob, 1)
	go func() {
		_, next, _ := dispatcher.take()
		ready <- next
	}()
	select {
	case <-ready:
		t.Fatal("second update for the same user ran concurrently")
	default:
	}
	dispatcher.done(key)
	if next := <-ready; next.update.UpdateID != 2 {
		t.Fatalf("second update=%d", next.update.UpdateID)
	}
	dispatcher.done(key)
	dispatcher.close()
}

func TestCancelUpdateCanBypassUserQueue(t *testing.T) {
	download := tgbotapi.Update{UpdateID: 10, CallbackQuery: &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 42}, Data: "dl:mp3:320:key"}}
	cancel := tgbotapi.Update{UpdateID: 11, CallbackQuery: &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 42}, Data: "cancel_download:key"}}
	if updateOwnerKey(download) == updateOwnerKey(cancel) {
		t.Fatal("cancel callback would be blocked behind the active download")
	}
}
