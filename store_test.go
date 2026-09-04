package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestStorePersistsLanguageCacheStatsAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	state, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := state.setLanguage(ctx, 42, "en"); err != nil {
		t.Fatal(err)
	}
	entry := cachedAudio{Key: "youtube:id:flac:best", FileID: "file-id", Title: "Track", Artist: "Artist", Format: "flac", Quality: "best", Size: 123, MediaType: "document"}
	if err := state.putCachedAudio(ctx, entry); err != nil {
		t.Fatal(err)
	}
	state.increment(ctx, "downloads_ok")
	state.recordDownload(ctx, 42, "youtube.com", "mp3:320", "ok", time.Second, "")
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	state, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if lang, ok := state.language(ctx, 42); !ok || lang != "en" {
		t.Fatalf("language=%q ok=%v", lang, ok)
	}
	if got, ok := state.cachedAudio(ctx, entry.Key, time.Hour); !ok || got.FileID != entry.FileID || got.Title != entry.Title || got.MediaType != "document" {
		t.Fatalf("cache=%#v ok=%v", got, ok)
	}
	stats, err := state.stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DownloadsOK != 1 || stats.UniqueUsers != 1 || stats.CachedTracks != 1 {
		t.Fatalf("stats=%#v", stats)
	}
	var history int
	if err := state.db.QueryRow(`SELECT count(*) FROM download_history WHERE user_id=42 AND status='ok'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("history=%d err=%v", history, err)
	}
}

func TestStorePersistsPendingTelegramUpdates(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	update := tgbotapi.Update{UpdateID: 123, Message: &tgbotapi.Message{Text: "hello"}}
	inserted, err := state.persistUpdate(context.Background(), update)
	if err != nil || !inserted {
		t.Fatalf("persist inserted=%v err=%v", inserted, err)
	}
	inserted, err = state.persistUpdate(context.Background(), update)
	if err != nil || inserted {
		t.Fatalf("duplicate inserted=%v err=%v", inserted, err)
	}
	pending, err := state.pendingUpdates(context.Background())
	if err != nil || len(pending) != 1 || pending[0].UpdateID != 123 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	if err := state.finishUpdate(context.Background(), 123, true); err != nil {
		t.Fatal(err)
	}
	pending, err = state.pendingUpdates(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("finished update still pending: %#v err=%v", pending, err)
	}
}

func TestStoreResolvesObservedTelegramUsername(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if err := state.observeTelegramUser(ctx, &tgbotapi.User{ID: 42, UserName: "Known_User"}); err != nil {
		t.Fatal(err)
	}
	if userID, found, err := state.telegramUserIDByUsername(ctx, "@KNOWN_user"); err != nil || !found || userID != 42 {
		t.Fatalf("userID=%d found=%v err=%v", userID, found, err)
	}
	if err := state.observeTelegramUser(ctx, &tgbotapi.User{ID: 84, UserName: "known_user"}); err != nil {
		t.Fatal(err)
	}
	if userID, found, err := state.telegramUserIDByUsername(ctx, "known_user"); err != nil || !found || userID != 84 {
		t.Fatalf("reassigned userID=%d found=%v err=%v", userID, found, err)
	}
	if _, found, err := state.telegramUserIDByUsername(ctx, "bad username"); err != nil || found {
		t.Fatalf("invalid username found=%v err=%v", found, err)
	}
}

func TestStoreUserNoticeCanBeThrottledAndDismissed(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if show, err := state.claimUserNotice(ctx, 42, "notice", time.Hour); err != nil || !show {
		t.Fatalf("first claim show=%v err=%v", show, err)
	}
	if show, err := state.claimUserNotice(ctx, 42, "notice", time.Hour); err != nil || show {
		t.Fatalf("throttled claim show=%v err=%v", show, err)
	}
	if _, err := state.db.Exec(`UPDATE user_notices SET last_shown_at=unixepoch()-7200 WHERE user_id=42 AND notice='notice'`); err != nil {
		t.Fatal(err)
	}
	if show, err := state.claimUserNotice(ctx, 42, "notice", time.Hour); err != nil || !show {
		t.Fatalf("expired claim show=%v err=%v", show, err)
	}
	if err := state.dismissUserNotice(ctx, 42, "notice"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.Exec(`UPDATE user_notices SET last_shown_at=0 WHERE user_id=42 AND notice='notice'`); err != nil {
		t.Fatal(err)
	}
	if show, err := state.claimUserNotice(ctx, 42, "notice", time.Hour); err != nil || show {
		t.Fatalf("dismissed claim show=%v err=%v", show, err)
	}
}

func TestStoreCanExplicitlyDiscardPendingTelegramUpdates(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.persistUpdate(context.Background(), tgbotapi.Update{UpdateID: 321}); err != nil {
		t.Fatal(err)
	}
	if err := state.discardPendingUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pending, err := state.pendingUpdates(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
}

func TestStoreExpiresCache(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.putCachedAudio(context.Background(), cachedAudio{Key: "old", FileID: "file"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.Exec(`UPDATE audio_cache SET updated_at=? WHERE cache_key='old'`, time.Now().Add(-2*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.cachedAudio(context.Background(), "old", time.Hour); ok {
		t.Fatal("expired cache entry was returned")
	}
}

func TestStoreAggregatesMediaPerformance(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now()
	for _, elapsed := range []int64{10, 20, 30, 40, 100} {
		state.recordMediaStage(context.Background(), mediaStageSample{Stage: "source_download", Source: "octave", ElapsedMS: elapsed, SizeBytes: 1000, OK: elapsed != 100, CreatedAt: now})
	}
	rows, err := state.mediaPerformance(context.Background(), now.Add(-time.Minute))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	if rows[0].Count != 5 || rows[0].OK != 4 || rows[0].P50 != 30*time.Millisecond || rows[0].P95 != 40*time.Millisecond {
		t.Fatalf("performance=%#v", rows[0])
	}
}
