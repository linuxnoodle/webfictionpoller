package handlers

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// AddSource attaches a new source to a series. The first source for a series
// becomes the primary automatically; otherwise the new source is an alternate
// with the supplied priority (default 100, lower = higher priority).
//
// Callers should validate provider+URL compatibility (MatchURL) before
// calling — this method trusts the inputs and only enforces the unique
// (series_id, source_url) constraint.
func (s *Store) AddSource(seriesID int64, providerName, sourceURL string, priority int) (*models.SeriesSource, error) {
	if priority < 0 {
		priority = 100
	}

	// Determine primary-ness: first source for the series is auto-primary.
	var existing int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM series_sources WHERE series_id = ?", seriesID).Scan(&existing)
	isPrimary := existing == 0

	// If this is becoming the primary, demote any other primary first.
	if isPrimary {
		_, _ = s.db.Exec("UPDATE series_sources SET is_primary = 0 WHERE series_id = ?", seriesID)
	}

	res, err := s.db.Exec(`
		INSERT INTO series_sources (series_id, provider_name, source_url, priority, is_primary)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, seriesID, providerName, sourceURL, priority, isPrimary)
	if err != nil {
		return nil, fmt.Errorf("insert source: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already exists; return the existing row.
		return s.GetSourceByURL(seriesID, sourceURL)
	}
	id, _ := res.LastInsertId()

	// If this became primary, mirror into the denormalized series columns
	// (back-compat for existing queries / OPDS).
	if isPrimary {
		_, _ = s.db.Exec("UPDATE series SET provider_name = ?, source_url = ? WHERE id = ?",
			providerName, sourceURL, seriesID)
	}

	return s.GetSourceByID(id)
}

// ListSources returns every source for a series ordered by priority asc, then
// is_primary desc, then id asc — i.e. effective polling order.
func (s *Store) ListSources(seriesID int64) ([]models.SeriesSource, error) {
	rows, err := s.db.Query(`
		SELECT id, series_id, provider_name, source_url, priority, is_primary,
		       last_ok, last_fail, last_error, consecutive_fails, disabled, created_at
		FROM series_sources
		WHERE series_id = ?
		ORDER BY priority ASC, is_primary DESC, id ASC
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSources(rows)
}

// ActiveSourcesForPoll returns non-disabled sources for a series in polling
// order. Used by the scheduler's failover loop.
func (s *Store) ActiveSourcesForPoll(seriesID int64) ([]models.SeriesSource, error) {
	rows, err := s.db.Query(`
		SELECT id, series_id, provider_name, source_url, priority, is_primary,
		       last_ok, last_fail, last_error, consecutive_fails, disabled, created_at
		FROM series_sources
		WHERE series_id = ? AND disabled = 0
		ORDER BY is_primary DESC, priority ASC, id ASC
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSources(rows)
}

// GetSourceByID fetches a single source row.
func (s *Store) GetSourceByID(id int64) (*models.SeriesSource, error) {
	var src models.SeriesSource
	var lastOK, lastFail sql.NullTime
	var lastErr sql.NullString
	err := s.db.QueryRow(`
		SELECT id, series_id, provider_name, source_url, priority, is_primary,
		       last_ok, last_fail, last_error, consecutive_fails, disabled, created_at
		FROM series_sources WHERE id = ?
	`, id).Scan(&src.ID, &src.SeriesID, &src.ProviderName, &src.SourceURL, &src.Priority, &src.IsPrimary,
		&lastOK, &lastFail, &lastErr, &src.ConsecutiveFails, &src.Disabled, &src.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	src.LastOK = nullTimePtr(lastOK)
	src.LastFail = nullTimePtr(lastFail)
	src.LastError = lastErr.String
	return &src, nil
}

// GetSourceByURL fetches a source by its (series_id, source_url).
func (s *Store) GetSourceByURL(seriesID int64, sourceURL string) (*models.SeriesSource, error) {
	var src models.SeriesSource
	var lastOK, lastFail sql.NullTime
	var lastErr sql.NullString
	err := s.db.QueryRow(`
		SELECT id, series_id, provider_name, source_url, priority, is_primary,
		       last_ok, last_fail, last_error, consecutive_fails, disabled, created_at
		FROM series_sources WHERE series_id = ? AND source_url = ?
	`, seriesID, sourceURL).Scan(&src.ID, &src.SeriesID, &src.ProviderName, &src.SourceURL, &src.Priority, &src.IsPrimary,
		&lastOK, &lastFail, &lastErr, &src.ConsecutiveFails, &src.Disabled, &src.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	src.LastOK = nullTimePtr(lastOK)
	src.LastFail = nullTimePtr(lastFail)
	src.LastError = lastErr.String
	return &src, nil
}

// UpdateSource mutates priority / disabled. Pass -1 to leave priority unchanged.
func (s *Store) UpdateSource(id int64, priority int, disabled bool) error {
	if priority >= 0 {
		_, err := s.db.Exec(`UPDATE series_sources SET priority = ?, disabled = ? WHERE id = ?`,
			priority, disabled, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE series_sources SET disabled = ? WHERE id = ?`, disabled, id)
	return err
}

// DeleteSource removes a source. If it was the primary, the next-highest
// priority source is promoted. Returns ErrLastSource if attempting to delete
// the only remaining source (a series must always have at least one).
func (s *Store) DeleteSource(id int64) error {
	src, err := s.GetSourceByID(id)
	if err != nil {
		return err
	}
	if src == nil {
		return ErrSourceNotFound
	}

	var remaining int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM series_sources WHERE series_id = ?", src.SeriesID).Scan(&remaining)
	if remaining <= 1 {
		return ErrLastSource
	}

	if _, err := s.db.Exec("DELETE FROM series_sources WHERE id = ?", id); err != nil {
		return err
	}

	// Promote replacement if we just removed the primary.
	if src.IsPrimary {
		var nextID int64
		err := s.db.QueryRow(`
			SELECT id FROM series_sources
			WHERE series_id = ? AND disabled = 0
			ORDER BY priority ASC, id ASC LIMIT 1
		`, src.SeriesID).Scan(&nextID)
		if err == nil {
			_ = s.PromoteSource(nextID)
		}
	}
	return nil
}

// PromoteSource makes the given source the primary for its series. Demotes any
// existing primary and mirrors the new source's provider/URL into the
// denormalized series columns.
func (s *Store) PromoteSource(id int64) error {
	src, err := s.GetSourceByID(id)
	if err != nil || src == nil {
		return ErrSourceNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE series_sources SET is_primary = 0 WHERE series_id = ?", src.SeriesID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE series_sources SET is_primary = 1, priority = 0 WHERE id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE series SET provider_name = ?, source_url = ? WHERE id = ?",
		src.ProviderName, src.SourceURL, src.SeriesID); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordSourceOK marks a successful poll/fetch against the source: bumps
// last_ok, resets consecutive_fails.
func (s *Store) RecordSourceOK(id int64) error {
	_, err := s.db.Exec(`
		UPDATE series_sources
		SET last_ok = ?, last_error = '', consecutive_fails = 0
		WHERE id = ?
	`, time.Now().UTC(), id)
	return err
}

// RecordSourceFail marks a failed poll/fetch against the source: bumps
// last_fail + consecutive_fails, records the error message.
func (s *Store) RecordSourceFail(id int64, errMsg string) error {
	_, err := s.db.Exec(`
		UPDATE series_sources
		SET last_fail = ?, last_error = ?, consecutive_fails = consecutive_fails + 1
		WHERE id = ?
	`, time.Now().UTC(), truncate(errMsg, 500), id)
	return err
}

// AutoPromoteIfFailing promotes the healthiest alternate source when the
// current primary has exceeded `threshold` consecutive failures. Returns the
// promoted source id (0 if no promotion happened).
//
// This is the automated failover: an admin can also trigger promotion
// manually via the API/UI.
func (s *Store) AutoPromoteIfFailing(seriesID int64, threshold int) (int64, error) {
	if threshold <= 0 {
		return 0, nil
	}
	var primaryID int64
	var primaryFails int
	err := s.db.QueryRow(`
		SELECT id, consecutive_fails FROM series_sources
		WHERE series_id = ? AND is_primary = 1
	`, seriesID).Scan(&primaryID, &primaryFails)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if primaryFails < threshold {
		return 0, nil
	}

	// Find the healthiest alternate: lowest consecutive_fails, then priority.
	var nextID int64
	err = s.db.QueryRow(`
		SELECT id FROM series_sources
		WHERE series_id = ? AND id != ? AND disabled = 0
		ORDER BY consecutive_fails ASC, priority ASC, id ASC
		LIMIT 1
	`, seriesID, primaryID).Scan(&nextID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := s.PromoteSource(nextID); err != nil {
		return 0, err
	}
	return nextID, nil
}

func scanSources(rows *sql.Rows) ([]models.SeriesSource, error) {
	var out []models.SeriesSource
	for rows.Next() {
		var src models.SeriesSource
		var lastOK, lastFail sql.NullTime
		var lastErr sql.NullString
		if err := rows.Scan(&src.ID, &src.SeriesID, &src.ProviderName, &src.SourceURL,
			&src.Priority, &src.IsPrimary, &lastOK, &lastFail, &lastErr,
			&src.ConsecutiveFails, &src.Disabled, &src.CreatedAt); err != nil {
			return nil, err
		}
		src.LastOK = nullTimePtr(lastOK)
		src.LastFail = nullTimePtr(lastFail)
		src.LastError = lastErr.String
		out = append(out, src)
	}
	return out, nil
}

func nullTimePtr(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time
	return &t
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Sentinel errors for source operations.
var (
	ErrSourceNotFound = fmt.Errorf("source not found")
	ErrLastSource     = fmt.Errorf("cannot delete the only remaining source for a series")
)
