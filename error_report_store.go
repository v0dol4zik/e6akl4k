package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// errorReportRetention is how long report rows stay available for the buttons and the digest.
const errorReportRetention = 14 * 24 * time.Hour

// errorReportRecord is one persisted user-facing failure. URL is kept in full so that the
// retry and fix buttons can repeat the request; Error is already redacted.
type errorReportRecord struct {
	ID          string
	Stage       string
	Class       string
	Fingerprint string
	ChatID      int64
	UserID      int64
	URL         string
	Query       string
	Format      string
	Error       string
	Version     string
	CreatedAt   time.Time
	ResolvedAt  time.Time
	ResolvedBy  int64
}

// errorDigestRow aggregates expected failures of one stage and fingerprint.
type errorDigestRow struct {
	Stage       string
	Fingerprint string
	Sample      string
	Count       int
	Users       int
}

func (s *store) saveErrorReport(ctx context.Context, record errorReportRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO error_reports(id,stage,class,fingerprint,chat_id,user_id,url,query,format,error,version,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, record.ID, record.Stage, record.Class, record.Fingerprint, record.ChatID, record.UserID,
		record.URL, record.Query, record.Format, record.Error, record.Version, record.CreatedAt.Unix())
	return err
}

func (s *store) errorReportByID(ctx context.Context, id string) (errorReportRecord, error) {
	var record errorReportRecord
	var created, resolved int64
	err := s.db.QueryRowContext(ctx, `SELECT id,stage,class,fingerprint,chat_id,user_id,url,query,format,error,version,created_at,resolved_at,resolved_by
FROM error_reports WHERE id=?`, id).Scan(&record.ID, &record.Stage, &record.Class, &record.Fingerprint, &record.ChatID, &record.UserID,
		&record.URL, &record.Query, &record.Format, &record.Error, &record.Version, &created, &resolved, &record.ResolvedBy)
	if err != nil {
		return errorReportRecord{}, err
	}
	record.CreatedAt = time.Unix(created, 0)
	if resolved > 0 {
		record.ResolvedAt = time.Unix(resolved, 0)
	}
	return record, nil
}

// resolveErrorReport claims a report for the fix button; only the first admin wins.
func (s *store) resolveErrorReport(ctx context.Context, id string, adminID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE error_reports SET resolved_at=unixepoch(), resolved_by=? WHERE id=? AND resolved_at=0`, adminID, id)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

// unresolveErrorReport releases a claim whose notice could not be delivered.
func (s *store) unresolveErrorReport(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE error_reports SET resolved_at=0, resolved_by=0 WHERE id=?`, id)
}

// errorReportCounts returns how many real and expected failures were recorded since the cutoff.
func (s *store) errorReportCounts(ctx context.Context, since time.Time) (real, expected int, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT class,COUNT(*) FROM error_reports WHERE created_at >= ? GROUP BY class`, since.Unix())
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var class string
		var count int
		if err := rows.Scan(&class, &count); err != nil {
			return 0, 0, err
		}
		if class == errorClassExpected {
			expected += count
		} else {
			real += count
		}
	}
	return real, expected, rows.Err()
}

// lastRealErrorReport returns the newest unexpected failure, if any is still retained.
func (s *store) lastRealErrorReport(ctx context.Context) (errorReportRecord, bool) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM error_reports WHERE class<>? ORDER BY created_at DESC, rowid DESC LIMIT 1`, errorClassExpected).Scan(&id)
	if err != nil {
		return errorReportRecord{}, false
	}
	record, err := s.errorReportByID(ctx, id)
	return record, err == nil
}

// errorDigest groups expected failures since the cutoff, most frequent first. Users counts
// distinct users (or chats when the user is unknown).
func (s *store) errorDigest(ctx context.Context, since time.Time, limit int) ([]errorDigestRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stage,fingerprint,MAX(error),COUNT(*),
COUNT(DISTINCT CASE WHEN user_id<>0 THEN user_id WHEN chat_id<>0 THEN chat_id END)
FROM error_reports WHERE class=? AND created_at >= ?
GROUP BY stage,fingerprint ORDER BY COUNT(*) DESC, stage LIMIT ?`, errorClassExpected, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []errorDigestRow
	for rows.Next() {
		var row errorDigestRow
		if err := rows.Scan(&row.Stage, &row.Fingerprint, &row.Sample, &row.Count, &row.Users); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *store) pruneErrorReports(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM error_reports WHERE created_at < ?`, before.Unix())
	return err
}

func isUniqueViolation(err error) bool {
	return err != nil && !errors.Is(err, sql.ErrNoRows) && strings.Contains(strings.ToLower(err.Error()), "unique")
}
