package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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
	MediaType string
	UpdatedAt time.Time
}

// historyItem is one delivered download shown by /history.
type historyItem struct {
	ID        int64
	CacheKey  string
	Title     string
	Artist    string
	Format    string
	CreatedAt time.Time
}

type statsSnapshot struct {
	StartedAt        time.Time
	DownloadsOK      int64
	DownloadsPartial int64
	DownloadsFailed  int64
	CacheHits        int64
	Searches         int64
	RateLimited      int64
	QueueRejected    int64
	CookieErrors     int64
	Cancelled        int64
	TooLarge         int64
	UniqueUsers      int64
	CachedTracks     int64
}

type store struct {
	db *sql.DB
}

type adminRecord struct {
	UserID    int64
	AddedBy   int64
	CreatedAt time.Time
}

type banRecord struct {
	UserID    int64
	BannedBy  int64
	Reason    string
	CreatedAt time.Time
}

type auditRecord struct {
	ActorID   int64
	Action    string
	TargetID  int64
	Details   string
	CreatedAt time.Time
}

type userInfo struct {
	UserID       int64
	Language     string
	UpdatedAt    time.Time
	Downloads    int64
	LastDownload time.Time
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
CREATE TABLE IF NOT EXISTS telegram_users (
  user_id INTEGER PRIMARY KEY,
  username TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS user_notices (
  user_id INTEGER NOT NULL,
  notice TEXT NOT NULL,
  dismissed INTEGER NOT NULL DEFAULT 0,
  last_shown_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, notice)
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
  media_type TEXT NOT NULL DEFAULT 'audio',
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
CREATE TABLE IF NOT EXISTS telegram_updates (
  update_id INTEGER PRIMARY KEY,
  payload BLOB NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS admins (
  user_id INTEGER PRIMARY KEY,
  added_by INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS bans (
  user_id INTEGER PRIMARY KEY,
  banned_by INTEGER NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS admin_audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_id INTEGER NOT NULL,
  action TEXT NOT NULL,
  target_id INTEGER NOT NULL DEFAULT 0,
  details TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS media_stage_samples (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  stage TEXT NOT NULL,
  source TEXT NOT NULL,
  elapsed_ms INTEGER NOT NULL,
  size_bytes INTEGER NOT NULL DEFAULT 0,
  ok INTEGER NOT NULL,
  mode TEXT NOT NULL DEFAULT '',
  format TEXT NOT NULL DEFAULT '',
  quality TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS telegram_updates_status_id ON telegram_updates(status, update_id);
CREATE INDEX IF NOT EXISTS download_history_created_at ON download_history(created_at);
CREATE INDEX IF NOT EXISTS bans_created_at ON bans(created_at);
CREATE INDEX IF NOT EXISTS admin_audit_created_at ON admin_audit(created_at);
CREATE INDEX IF NOT EXISTS media_stage_samples_created_at ON media_stage_samples(created_at);
CREATE INDEX IF NOT EXISTS media_stage_samples_stage_source ON media_stage_samples(stage,source,created_at);
CREATE INDEX IF NOT EXISTS telegram_users_username ON telegram_users(username);
INSERT OR IGNORE INTO metadata(name, value) VALUES ('started_at', CAST(unixepoch() AS TEXT));
CREATE INDEX IF NOT EXISTS audio_cache_updated_at ON audio_cache(updated_at);
`)
	if err != nil {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE audio_cache ADD COLUMN media_type TEXT NOT NULL DEFAULT 'audio'`,
		`ALTER TABLE users ADD COLUMN default_format TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN default_quality TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE download_history ADD COLUMN cache_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE download_history ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE download_history ADD COLUMN artist TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN lastfm_user TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN notifications_off INTEGER NOT NULL DEFAULT 0`,
	} {
		_, err = s.db.ExecContext(ctx, statement)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS download_history_user_id ON download_history(user_id, id)`)
	return err
}

func (s *store) addAdmin(ctx context.Context, userID, actorID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO admins(user_id,added_by,created_at) VALUES(?,?,unixepoch())`, userID, actorID)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed > 0, nil
}

func (s *store) deleteAdmin(ctx context.Context, userID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM admins WHERE user_id=?`, userID)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed > 0, nil
}

func (s *store) isDynamicAdmin(ctx context.Context, userID int64) bool {
	var exists int
	return s.db.QueryRowContext(ctx, `SELECT 1 FROM admins WHERE user_id=?`, userID).Scan(&exists) == nil
}

func (s *store) admins(ctx context.Context) ([]adminRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id,added_by,created_at FROM admins ORDER BY created_at,user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []adminRecord
	for rows.Next() {
		var item adminRecord
		var created int64
		if err := rows.Scan(&item.UserID, &item.AddedBy, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *store) banUser(ctx context.Context, userID, actorID int64, reason string) (bool, error) {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO bans(user_id,banned_by,reason,created_at) VALUES(?,?,?,unixepoch())`, userID, actorID, reason)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed > 0, nil
}

func (s *store) pardonUser(ctx context.Context, userID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM bans WHERE user_id=?`, userID)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed > 0, nil
}

func (s *store) isBanned(ctx context.Context, userID int64) (banRecord, bool) {
	var item banRecord
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id,banned_by,reason,created_at FROM bans WHERE user_id=?`, userID).Scan(&item.UserID, &item.BannedBy, &item.Reason, &created)
	if err != nil {
		return banRecord{}, false
	}
	item.CreatedAt = time.Unix(created, 0)
	return item, true
}

func (s *store) bans(ctx context.Context, limit, offset int) ([]banRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id,banned_by,reason,created_at FROM bans ORDER BY created_at DESC,user_id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []banRecord
	for rows.Next() {
		var item banRecord
		var created int64
		if err := rows.Scan(&item.UserID, &item.BannedBy, &item.Reason, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *store) audit(ctx context.Context, actorID int64, action string, targetID int64, details string) {
	if len(details) > 1000 {
		details = details[:1000]
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO admin_audit(actor_id,action,target_id,details,created_at) VALUES(?,?,?,?,unixepoch())`, actorID, action, targetID, details)
}

func (s *store) auditLog(ctx context.Context, limit int) ([]auditRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT actor_id,action,target_id,details,created_at FROM admin_audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []auditRecord
	for rows.Next() {
		var item auditRecord
		var created int64
		if err := rows.Scan(&item.ActorID, &item.Action, &item.TargetID, &item.Details, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *store) userInfo(ctx context.Context, userID int64) (userInfo, bool, error) {
	item := userInfo{UserID: userID}
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT language,updated_at FROM users WHERE user_id=?`, userID).Scan(&item.Language, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	item.UpdatedAt = time.Unix(updated, 0)
	var last int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(max(created_at),0) FROM download_history WHERE user_id=?`, userID).Scan(&item.Downloads, &last); err != nil {
		return item, true, err
	}
	if last > 0 {
		item.LastDownload = time.Unix(last, 0)
	}
	return item, true, nil
}

func (s *store) recordDownload(ctx context.Context, userID int64, source, format, status string, elapsed time.Duration, message, cacheKey, title, artist string) {
	if len(message) > 500 {
		message = message[:500]
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO download_history(user_id,source,format,status,elapsed_ms,error,cache_key,title,artist,created_at) VALUES(?,?,?,?,?,?,?,?,?,unixepoch())`, userID, source, format, status, elapsed.Milliseconds(), message, cacheKey, title, artist)
}

// recentDownloads returns the newest delivered downloads of a user with a cache key,
// one row per cache key, newest first.
func (s *store) recentDownloads(ctx context.Context, userID int64, limit int) ([]historyItem, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,cache_key,title,artist,format,created_at FROM download_history
WHERE id IN (SELECT max(id) FROM download_history WHERE user_id=? AND status='delivered' AND cache_key<>'' GROUP BY cache_key)
ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []historyItem
	for rows.Next() {
		var item historyItem
		var created int64
		if err := rows.Scan(&item.ID, &item.CacheKey, &item.Title, &item.Artist, &item.Format, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0)
		items = append(items, item)
	}
	return items, rows.Err()
}

// historyEntry returns one history row; ok is false when it does not belong to the user.
func (s *store) historyEntry(ctx context.Context, userID, id int64) (historyItem, bool) {
	var item historyItem
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id,cache_key,title,artist,format,created_at FROM download_history WHERE id=? AND user_id=?`, id, userID).
		Scan(&item.ID, &item.CacheKey, &item.Title, &item.Artist, &item.Format, &created)
	if err != nil {
		return historyItem{}, false
	}
	item.CreatedAt = time.Unix(created, 0)
	return item, true
}

// clearHistory hides the user's /history entries. Only rows that /history can show are removed;
// failed or legacy rows stay so administrator reports keep their download counts.
func (s *store) clearHistory(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM download_history WHERE user_id=? AND status='delivered' AND cache_key<>''`, userID)
	return err
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

// userPreference returns the stored default download format and quality.
// An empty format means the user is asked every time.
func (s *store) userPreference(ctx context.Context, userID int64) (string, string, bool) {
	var format, quality string
	err := s.db.QueryRowContext(ctx, `SELECT default_format, default_quality FROM users WHERE user_id=?`, userID).Scan(&format, &quality)
	if err != nil || format == "" {
		return "", "", false
	}
	return format, quality, true
}

func (s *store) setUserPreference(ctx context.Context, userID int64, format, quality string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(user_id, language, default_format, default_quality, updated_at) VALUES(?,?,?,?,unixepoch())
ON CONFLICT(user_id) DO UPDATE SET default_format=excluded.default_format, default_quality=excluded.default_quality, updated_at=excluded.updated_at`, userID, defaultLang, format, quality)
	return err
}

// lastfmUser returns the linked last.fm profile, or an empty string when none is linked.
func (s *store) lastfmUser(ctx context.Context, userID int64) string {
	var name string
	_ = s.db.QueryRowContext(ctx, `SELECT lastfm_user FROM users WHERE user_id=?`, userID).Scan(&name)
	return name
}

// setLastfmUser links a last.fm profile; an empty name unlinks it.
func (s *store) setLastfmUser(ctx context.Context, userID int64, name string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(user_id, language, lastfm_user, updated_at) VALUES(?,?,?,unixepoch())
ON CONFLICT(user_id) DO UPDATE SET lastfm_user=excluded.lastfm_user, updated_at=excluded.updated_at`, userID, defaultLang, name)
	return err
}

// notificationsOff reports whether a user muted the notices sent by /msgall and /msg.
func (s *store) notificationsOff(ctx context.Context, userID int64) bool {
	var off int
	_ = s.db.QueryRowContext(ctx, `SELECT notifications_off FROM users WHERE user_id=?`, userID).Scan(&off)
	return off != 0
}

func (s *store) setNotificationsOff(ctx context.Context, userID int64, off bool) error {
	value := 0
	if off {
		value = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(user_id, language, notifications_off, updated_at) VALUES(?,?,?,unixepoch())
ON CONFLICT(user_id) DO UPDATE SET notifications_off=excluded.notifications_off, updated_at=excluded.updated_at`, userID, defaultLang, value)
	return err
}

type noticeRecipient struct {
	UserID int64
	Lang   string
}

// noticeRecipients lists the users a broadcast reaches: everyone who chose a language, except
// the sender, banned users and those who muted notices. muted counts the latter.
func (s *store) noticeRecipients(ctx context.Context, senderID int64) (recipients []noticeRecipient, muted int, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, language, notifications_off FROM users
WHERE user_id>0 AND user_id<>? AND user_id NOT IN (SELECT user_id FROM bans) ORDER BY user_id`, senderID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var recipient noticeRecipient
		var off int
		if err := rows.Scan(&recipient.UserID, &recipient.Lang, &off); err != nil {
			return nil, 0, err
		}
		if off != 0 {
			muted++
			continue
		}
		recipients = append(recipients, recipient)
	}
	return recipients, muted, rows.Err()
}

func normalizeTelegramUsername(value string) (string, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "@"))
	if len(value) < 5 || len(value) > 32 {
		return "", false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return "", false
		}
	}
	return strings.ToLower(value), true
}

func (s *store) observeTelegramUser(ctx context.Context, user *tgbotapi.User) error {
	if s == nil || user == nil || user.ID <= 0 {
		return nil
	}
	username, valid := normalizeTelegramUsername(user.UserName)
	if !valid {
		username = ""
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if username != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE telegram_users SET username='' WHERE username=? AND user_id<>?`, username, user.ID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO telegram_users(user_id,username,updated_at) VALUES(?,?,unixepoch())
ON CONFLICT(user_id) DO UPDATE SET username=excluded.username, updated_at=excluded.updated_at`, user.ID, username); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) telegramUserIDByUsername(ctx context.Context, username string) (int64, bool, error) {
	username, valid := normalizeTelegramUsername(username)
	if !valid {
		return 0, false, nil
	}
	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM telegram_users WHERE username=? ORDER BY updated_at DESC LIMIT 1`, username).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return userID, err == nil, err
}

// telegramUsername returns the last observed username for a user, or "" when none is known.
func (s *store) telegramUsername(ctx context.Context, userID int64) string {
	var username string
	if err := s.db.QueryRowContext(ctx, `SELECT username FROM telegram_users WHERE user_id=?`, userID).Scan(&username); err != nil {
		return ""
	}
	return username
}

func (s *store) claimUserNotice(ctx context.Context, userID int64, notice string, interval time.Duration) (bool, error) {
	if s == nil || userID <= 0 || strings.TrimSpace(notice) == "" {
		return false, nil
	}
	seconds := max(int64(interval/time.Second), 0)
	result, err := s.db.ExecContext(ctx, `INSERT INTO user_notices(user_id,notice,dismissed,last_shown_at) VALUES(?,?,0,unixepoch())
ON CONFLICT(user_id,notice) DO UPDATE SET last_shown_at=unixepoch()
WHERE user_notices.dismissed=0 AND user_notices.last_shown_at <= unixepoch()-?`, userID, notice, seconds)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (s *store) dismissUserNotice(ctx context.Context, userID int64, notice string) error {
	if s == nil || userID <= 0 || strings.TrimSpace(notice) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_notices(user_id,notice,dismissed,last_shown_at) VALUES(?,?,1,0)
ON CONFLICT(user_id,notice) DO UPDATE SET dismissed=1`, userID, notice)
	return err
}

func (s *store) cachedAudio(ctx context.Context, key string, ttl time.Duration) (cachedAudio, bool) {
	var entry cachedAudio
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT cache_key,file_id,title,artist,duration,format,quality,size,media_type,updated_at
FROM audio_cache WHERE cache_key=?`, key).Scan(&entry.Key, &entry.FileID, &entry.Title, &entry.Artist, &entry.Duration, &entry.Format, &entry.Quality, &entry.Size, &entry.MediaType, &updated)
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
	mediaType := entry.MediaType
	if mediaType == "" {
		mediaType = "audio"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audio_cache(cache_key,file_id,title,artist,duration,format,quality,size,media_type,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,unixepoch()) ON CONFLICT(cache_key) DO UPDATE SET file_id=excluded.file_id,title=excluded.title,
artist=excluded.artist,duration=excluded.duration,format=excluded.format,quality=excluded.quality,size=excluded.size,media_type=excluded.media_type,updated_at=excluded.updated_at`,
		entry.Key, entry.FileID, entry.Title, entry.Artist, entry.Duration, entry.Format, entry.Quality, entry.Size, mediaType)
	return err
}

func (s *store) deleteCachedAudio(ctx context.Context, key string) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM audio_cache WHERE cache_key=?`, key)
}

func (s *store) increment(ctx context.Context, name string) {
	s.incrementBy(ctx, name, 1)
}

func (s *store) incrementBy(ctx context.Context, name string, delta int64) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO counters(name,value) VALUES(?,?)
ON CONFLICT(name) DO UPDATE SET value=value+excluded.value`, name, delta)
}

// counters returns every row of the counters table keyed by counter name.
func (s *store) counters(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,value FROM counters`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int64)
	for rows.Next() {
		var name string
		var value int64
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		result[name] = value
	}
	return result, rows.Err()
}

// metadata returns the value stored under name in the metadata table or "" when absent.
func (s *store) metadata(ctx context.Context, name string) string {
	var value string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE name=?`, name).Scan(&value); err != nil {
		return ""
	}
	return value
}

func (s *store) setMetadata(ctx context.Context, name, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO metadata(name,value) VALUES(?,?)
ON CONFLICT(name) DO UPDATE SET value=excluded.value`, name, value)
	return err
}

// cookieAlertTime returns the persisted time of the last stale-cookie alert or the zero time.
func (s *store) cookieAlertTime(ctx context.Context) time.Time {
	unix, err := strconv.ParseInt(s.metadata(ctx, cookieAlertMetadataKey), 10, 64)
	if err != nil || unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
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
		case "downloads_partial":
			snapshot.DownloadsPartial = value
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
		case "downloads_too_large":
			snapshot.TooLarge = value
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
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM telegram_updates WHERE status='done' AND updated_at < ?`, time.Now().Add(-7*24*time.Hour).Unix())
	}
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM media_stage_samples WHERE created_at < ?`, time.Now().Add(-30*24*time.Hour).Unix())
	}
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM media_stage_samples WHERE id NOT IN (SELECT id FROM media_stage_samples ORDER BY id DESC LIMIT 50000)`)
	}
	return err
}

func (s *store) persistUpdate(ctx context.Context, update tgbotapi.Update) (bool, error) {
	payload, err := json.Marshal(update)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO telegram_updates(update_id,payload,status,created_at,updated_at)
VALUES(?,?,'pending',unixepoch(),unixepoch())`, update.UpdateID, payload)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *store) pendingUpdates(ctx context.Context) ([]tgbotapi.Update, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM telegram_updates WHERE status='pending' ORDER BY update_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var updates []tgbotapi.Update
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var update tgbotapi.Update
		if err := json.Unmarshal(payload, &update); err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, rows.Err()
}

func (s *store) finishUpdate(ctx context.Context, updateID int, success bool) error {
	status := "done"
	if !success {
		status = "pending"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE telegram_updates SET status=?,updated_at=unixepoch() WHERE update_id=?`, status, updateID)
	return err
}

func (s *store) discardPendingUpdates(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM telegram_updates WHERE status='pending'`)
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
