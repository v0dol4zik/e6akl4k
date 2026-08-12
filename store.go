package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type cachedAudio struct {
	Key       string
	FileID    string
	Title     string
	Artist    string
	Duration  string
	Format    string
	Quality   string
	Size      int64
	UpdatedAt time.Time
}

type statsSnapshot struct {
	StartedAt       time.Time
	DownloadsOK     int64
	DownloadsFailed int64
	CacheHits       int64
	Searches        int64
	RateLimited     int64
	QueueRejected   int64
	CookieErrors    int64
	Cancelled       int64
	UniqueUsers     int64
	CachedTracks    int64
}

type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("создать каталог базы: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+absolute+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(absolute, 0o600)
	_ = os.Chmod(absolute+"-wal", 0o600)
	_ = os.Chmod(absolute+"-shm", 0o600)
	return s, nil
}

func (s *store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS users (
  user_id INTEGER PRIMARY KEY,
  language TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audio_cache (
  cache_key TEXT PRIMARY KEY,
  file_id TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  artist TEXT NOT NULL DEFAULT '',
  duration TEXT NOT NULL DEFAULT '',
  format TEXT NOT NULL DEFAULT '',
  quality TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS counters (
  name TEXT PRIMARY KEY,
  value INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS metadata (
  name TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS download_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  source TEXT NOT NULL,
  format TEXT NOT NULL,
  status TEXT NOT NULL,
  elapsed_ms INTEGER NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS download_history_created_at ON download_history(created_at);
INSERT OR IGNORE INTO metadata(name, value) VALUES ('started_at', CAST(unixepoch() AS TEXT));
CREATE INDEX IF NOT EXISTS audio_cache_updated_at ON audio_cache(updated_at);
`)
	return err
}

func (s *store) recordDownload(ctx context.Context, userID int64, source, format, status string, elapsed time.Duration, message string) {
	if len(message) > 500 {
		message = message[:500]
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO download_history(user_id,source,format,status,elapsed_ms,error,created_at) VALUES(?,?,?,?,?,?,unixepoch())`, userID, source, format, status, elapsed.Milliseconds(), message)
}

func (s *store) Close() error { return s.db.Close() }

func (s *store) language(ctx context.Context, userID int64) (string, bool) {
	var lang string
	err := s.db.QueryRowContext(ctx, `SELECT language FROM users WHERE user_id=?`, userID).Scan(&lang)
	return lang, err == nil
}

func (s *store) setLanguage(ctx context.Context, userID int64, lang string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(user_id, language, updated_at) VALUES(?,?,unixepoch())
ON CONFLICT(user_id) DO UPDATE SET language=excluded.language, updated_at=excluded.updated_at`, userID, lang)
	return err
}

func (s *store) cachedAudio(ctx context.Context, key string, ttl time.Duration) (cachedAudio, bool) {
	var entry cachedAudio
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT cache_key,file_id,title,artist,duration,format,quality,size,updated_at
FROM audio_cache WHERE cache_key=?`, key).Scan(&entry.Key, &entry.FileID, &entry.Title, &entry.Artist, &entry.Duration, &entry.Format, &entry.Quality, &entry.Size, &updated)
	if err != nil {
		return cachedAudio{}, false
	}
	entry.UpdatedAt = time.Unix(updated, 0)
	if ttl > 0 && time.Since(entry.UpdatedAt) > ttl {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM audio_cache WHERE cache_key=?`, key)
		return cachedAudio{}, false
	}
	return entry, true
}

func (s *store) putCachedAudio(ctx context.Context, entry cachedAudio) error {
	if entry.Key == "" || entry.FileID == "" {
		return errors.New("пустой cache key или file_id")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audio_cache(cache_key,file_id,title,artist,duration,format,quality,size,updated_at)
VALUES(?,?,?,?,?,?,?,?,unixepoch()) ON CONFLICT(cache_key) DO UPDATE SET file_id=excluded.file_id,title=excluded.title,
artist=excluded.artist,duration=excluded.duration,format=excluded.format,quality=excluded.quality,size=excluded.size,updated_at=excluded.updated_at`,
		entry.Key, entry.FileID, entry.Title, entry.Artist, entry.Duration, entry.Format, entry.Quality, entry.Size)
	return err
}

func (s *store) deleteCachedAudio(ctx context.Context, key string) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM audio_cache WHERE cache_key=?`, key)
}

func (s *store) increment(ctx context.Context, name string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO counters(name,value) VALUES(?,1)
ON CONFLICT(name) DO UPDATE SET value=value+1`, name)
}

func (s *store) stats(ctx context.Context) (statsSnapshot, error) {
	var snapshot statsSnapshot
	var started string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE name='started_at'`).Scan(&started); err == nil {
		var unix int64
		_, _ = fmt.Sscan(started, &unix)
		snapshot.StartedAt = time.Unix(unix, 0)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name,value FROM counters`)
	if err != nil {
		return snapshot, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var value int64
		if err := rows.Scan(&name, &value); err != nil {
			return snapshot, err
		}
		switch name {
		case "downloads_ok":
			snapshot.DownloadsOK = value
		case "downloads_failed":
			snapshot.DownloadsFailed = value
		case "cache_hits":
			snapshot.CacheHits = value
		case "searches":
			snapshot.Searches = value
		case "rate_limited":
			snapshot.RateLimited = value
		case "queue_rejected":
			snapshot.QueueRejected = value
		case "youtube_cookie_errors":
			snapshot.CookieErrors = value
		case "downloads_cancelled":
			snapshot.Cancelled = value
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&snapshot.UniqueUsers); err != nil {
		return snapshot, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audio_cache`).Scan(&snapshot.CachedTracks); err != nil {
		return snapshot, err
	}
	return snapshot, rows.Err()
}

func (s *store) cleanup(ctx context.Context, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM audio_cache WHERE updated_at < ?`, time.Now().Add(-ttl).Unix())
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM download_history WHERE created_at < ?`, time.Now().Add(-90*24*time.Hour).Unix())
	}
	return err
}

func (s *store) importLegacyInlineCache(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var entries map[string]string
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	for key, fileID := range entries {
		if err := s.putCachedAudio(ctx, cachedAudio{Key: key, FileID: fileID, Format: "mp3", Quality: "320"}); err != nil {
			return err
		}
	}
	return os.Rename(path, path+".migrated")
}
