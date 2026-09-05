package main

import (
	"context"
	"database/sql"
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
	state.recordDownload(ctx, 42, "youtube.com", "mp3:320", "ok", time.Second, "", "", "", "")
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

func TestStoreMigratesUserPreferencesOnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, language TEXT NOT NULL, updated_at INTEGER NOT NULL);
INSERT INTO users(user_id, language, updated_at) VALUES(7, 'ru', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if lang, ok := state.language(ctx, 7); !ok || lang != "ru" {
		t.Fatalf("language=%q ok=%v", lang, ok)
	}
	if format, quality, ok := state.userPreference(ctx, 7); ok || format != "" || quality != "" {
		t.Fatalf("legacy user must ask each time: format=%q quality=%q ok=%v", format, quality, ok)
	}
	if err := state.setUserPreference(ctx, 7, "mp3", "320"); err != nil {
		t.Fatal(err)
	}
	if err := state.setUserPreference(ctx, 8, "flac", "best"); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	state, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if lang, ok := state.language(ctx, 7); !ok || lang != "ru" {
		t.Fatalf("preference must keep the language: lang=%q ok=%v", lang, ok)
	}
	if format, quality, ok := state.userPreference(ctx, 7); !ok || format != "mp3" || quality != "320" {
		t.Fatalf("preference=%q/%q ok=%v", format, quality, ok)
	}
	if format, quality, ok := state.userPreference(ctx, 8); !ok || format != "flac" || quality != "best" {
		t.Fatalf("preference=%q/%q ok=%v", format, quality, ok)
	}
	if err := state.setUserPreference(ctx, 7, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := state.userPreference(ctx, 7); ok {
		t.Fatal("clearing the preference must switch back to asking each time")
	}
	if _, _, ok := state.userPreference(ctx, 999); ok {
		t.Fatal("unknown user must have no preference")
	}
}

func TestStoreMigratesDownloadHistoryOnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE download_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  source TEXT NOT NULL,
  format TEXT NOT NULL,
  status TEXT NOT NULL,
  elapsed_ms INTEGER NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
INSERT INTO download_history(user_id,source,format,status,elapsed_ms,error,created_at) VALUES(7,'youtube.com','mp3:320','delivered',10,'',1)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := state.recentDownloads(ctx, 7, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("legacy rows without a cache key must stay hidden: %#v", items)
	}
	state.recordDownload(ctx, 7, "youtube.com", "mp3:320", "delivered", time.Second, "", "youtube:abc:mp3:320", "Track", "Artist")
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	state, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	items, err = state.recentDownloads(ctx, 7, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].CacheKey != "youtube:abc:mp3:320" || items[0].Title != "Track" || items[0].Artist != "Artist" || items[0].Format != "mp3:320" {
		t.Fatalf("items=%#v", items)
	}
	var indexes int
	if err := state.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='download_history_user_id'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("index count=%d err=%v", indexes, err)
	}
}

func TestStoreRecentDownloadsDedupesAndClears(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	state.recordDownload(ctx, 1, "youtube.com", "mp3:320", "delivered", time.Second, "", "youtube:a:mp3:320", "Old A", "")
	state.recordDownload(ctx, 1, "youtube.com", "flac:best", "delivered", time.Second, "", "youtube:b:flac:best", "B", "Artist B")
	state.recordDownload(ctx, 1, "youtube.com", "mp3:320", "failed", time.Second, "boom", "youtube:c:mp3:320", "C", "")
	state.recordDownload(ctx, 1, "youtube.com", "mp3:320", "partial", time.Second, "", "youtube:d:mp3:320", "D", "")
	state.recordDownload(ctx, 1, "youtube.com", "mp3:320", "delivered", time.Second, "", "", "Playlist", "")
	state.recordDownload(ctx, 1, "youtube.com", "mp3:320", "delivered", time.Second, "", "youtube:a:mp3:320", "New A", "Artist A")
	state.recordDownload(ctx, 2, "youtube.com", "mp3:320", "delivered", time.Second, "", "youtube:e:mp3:320", "E", "")

	items, err := state.recentDownloads(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].CacheKey != "youtube:a:mp3:320" || items[0].Title != "New A" || items[1].CacheKey != "youtube:b:flac:best" {
		t.Fatalf("items=%#v", items)
	}
	if items[0].ID <= items[1].ID {
		t.Fatalf("newest entry must come first: %#v", items)
	}
	limited, err := state.recentDownloads(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].CacheKey != "youtube:a:mp3:320" {
		t.Fatalf("limited=%#v", limited)
	}
	if item, ok := state.historyEntry(ctx, 2, items[0].ID); ok {
		t.Fatalf("another user's row must be invisible: %#v", item)
	}
	if item, ok := state.historyEntry(ctx, 1, items[0].ID); !ok || item.Title != "New A" {
		t.Fatalf("item=%#v ok=%v", item, ok)
	}

	var before int
	if err := state.db.QueryRow(`SELECT count(*) FROM download_history WHERE user_id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := state.clearHistory(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if items, err := state.recentDownloads(ctx, 1, 10); err != nil || len(items) != 0 {
		t.Fatalf("history must be empty after clear: items=%#v err=%v", items, err)
	}
	if items, err := state.recentDownloads(ctx, 2, 10); err != nil || len(items) != 1 {
		t.Fatalf("other users must keep their history: items=%#v err=%v", items, err)
	}
	// Failed, partial and keyless rows are administrator statistics, not /history entries, and must survive.
	var remaining int
	if err := state.db.QueryRow(`SELECT count(*) FROM download_history WHERE user_id=1`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if before != 6 || remaining != 3 {
		t.Fatalf("clearHistory must only remove delivered rows with a cache key: before=%d remaining=%d", before, remaining)
	}
	if err := state.setLanguage(ctx, 1, "en"); err != nil {
		t.Fatal(err)
	}
	if info, ok, err := state.userInfo(ctx, 1); err != nil || !ok || info.Downloads != 3 {
		t.Fatalf("admin download count must survive clearing: info=%#v ok=%v err=%v", info, ok, err)
	}
}
