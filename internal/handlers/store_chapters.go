package handlers

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func (s *Store) GetTimeView(page, pageSize int, sortBy string, unreadOnly bool) ([]models.ChapterWithSeries, error) {
	orderCol := "c.published_at"
	if sortBy == "received" {
		orderCol = "c.created_at"
	}
	offset := page * pageSize

	whereClause := "s.status IN ('active', 'binge')"
	if unreadOnly {
		whereClause += " AND c.is_read = FALSE"
	}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT c.id, c.series_id, c.title, c.url, c.published_at, c.is_read, c.created_at,
		       s.title, s.author, s.provider_name, s.rating, s.source_url
		FROM chapters c
		JOIN series s ON c.series_id = s.id
		WHERE %s
		ORDER BY %s DESC
		LIMIT ? OFFSET ?
	`, whereClause, orderCol), pageSize, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.ChapterWithSeries
	for rows.Next() {
		var cws models.ChapterWithSeries
		if err := rows.Scan(&cws.ID, &cws.SeriesID, &cws.Title, &cws.URL, &cws.PublishedAt,
			&cws.IsRead, &cws.CreatedAt, &cws.SeriesTitle, &cws.SeriesAuthor,
			&cws.ProviderName, &cws.SeriesRating, &cws.SeriesSourceURL); err != nil {
			return nil, err
		}
		result = append(result, cws)
	}
	return result, nil
}

func (s *Store) getRecentChapters(seriesID int64, limit int) ([]models.Chapter, error) {
	rows, err := s.db.Query(`
		SELECT id, series_id, title, url, published_at, is_read, created_at
		FROM chapters
		WHERE series_id = ?
		ORDER BY published_at DESC
		LIMIT ?
	`, seriesID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chapters []models.Chapter
	for rows.Next() {
		var ch models.Chapter
		if err := rows.Scan(&ch.ID, &ch.SeriesID, &ch.Title, &ch.URL, &ch.PublishedAt, &ch.IsRead, &ch.CreatedAt); err != nil {
			return nil, err
		}
		chapters = append(chapters, ch)
	}
	return chapters, nil
}

func (s *Store) GetChapterWithProvider(chapterID int64) (*models.ChapterWithSeries, error) {
	var cws models.ChapterWithSeries
	err := s.db.QueryRow(`
		SELECT c.id, c.series_id, c.title, c.url, c.published_at, c.is_read, c.created_at,
		       c.preview_html,
		       s.title, s.author, s.provider_name, s.rating, s.source_url
		FROM chapters c
		JOIN series s ON c.series_id = s.id
		WHERE c.id = ?
	`, chapterID).Scan(&cws.ID, &cws.SeriesID, &cws.Title, &cws.URL, &cws.PublishedAt,
		&cws.IsRead, &cws.CreatedAt, &cws.PreviewHTML,
		&cws.SeriesTitle, &cws.SeriesAuthor, &cws.ProviderName, &cws.SeriesRating, &cws.SeriesSourceURL)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cws, nil
}

func (s *Store) SavePreviewHTML(chapterID int64, html string) error {
	_, err := s.db.Exec("UPDATE chapters SET preview_html = ? WHERE id = ?", html, chapterID)
	return err
}

func (s *Store) MarkChapterRead(chapterID int64) (string, error) {
	var url string
	err := s.db.QueryRow("UPDATE chapters SET is_read = TRUE WHERE id = ? RETURNING url", chapterID).Scan(&url)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("chapter not found")
	}
	return url, err
}

func (s *Store) InsertChapters(seriesID int64, chapters []models.Chapter) (int, error) {
	inserted := 0
	for _, ch := range chapters {
		var publishedAt interface{}
		if !ch.PublishedAt.IsZero() {
			publishedAt = ch.PublishedAt
		} else {
			publishedAt = time.Now()
		}
		result, err := s.db.Exec(`
			INSERT INTO chapters (series_id, title, url, published_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT DO NOTHING
		`, seriesID, ch.Title, ch.URL, publishedAt)
		if err != nil {
			return inserted, err
		}
		if n, _ := result.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

func (s *Store) AddSeries(series models.Series) (int64, error) {
	result, err := s.db.Exec(`
		INSERT INTO series (title, author, source_url, provider_name, rating, status, summary, image_url, archive)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, series.Title, series.Author, series.SourceURL, series.ProviderName, series.Rating, series.Status, series.Summary, series.ImageURL, series.Archive)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return id, err
	}
	// Maintain the series_sources invariant: every series has >=1 source,
	// the first one being the primary. When AddSeries is a no-op due to an
	// existing source_url, result.LastInsertId may be 0 or a stale id — in
	// that case AddSource's ON CONFLICT clause handles the dup.
	if id > 0 && series.SourceURL != "" {
		_, _ = s.db.Exec(`
			INSERT INTO series_sources (series_id, provider_name, source_url, priority, is_primary)
			VALUES (?, ?, ?, 0, 1)
			ON CONFLICT DO NOTHING
		`, id, series.ProviderName, series.SourceURL)
	}
	return id, nil
}

func (s *Store) MarkAllSeriesRead(seriesID int64) error {
	_, err := s.db.Exec("UPDATE chapters SET is_read = TRUE WHERE series_id = ?", seriesID)
	return err
}

func (s *Store) MarkAllChaptersRead() error {
	_, err := s.db.Exec("UPDATE chapters SET is_read = TRUE WHERE is_read = FALSE")
	return err
}
