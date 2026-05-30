package handlers

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func (s *Store) GetSeriesView() ([]models.SeriesWithChapters, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.title, s.author, s.source_url, s.provider_name, s.rating, s.status, s.summary, s.image_url, s.archive, s.created_at
		FROM series s
		WHERE s.status IN ('active', 'binge')
		  AND EXISTS (SELECT 1 FROM chapters c WHERE c.series_id = s.id)
		ORDER BY s.rating ASC, s.title ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.SeriesWithChapters
	for rows.Next() {
		var swc models.SeriesWithChapters
		if err := rows.Scan(&swc.Series.ID, &swc.Series.Title, &swc.Series.Author,
			&swc.Series.SourceURL, &swc.Series.ProviderName, &swc.Series.Rating,
			&swc.Series.Status, &swc.Series.Summary, &swc.Series.ImageURL, &swc.Series.Archive, &swc.Series.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, swc)
	}

	for i := range result {
		chapters, err := s.getRecentChapters(result[i].Series.ID, 30)
		if err != nil {
			return nil, err
		}
		result[i].Chapters = chapters
	}

	return result, nil
}

func (s *Store) GetSeriesBySourceURL(sourceURL string) (*models.Series, error) {
	var ser models.Series
	err := s.db.QueryRow(`
		SELECT id, title, author, source_url, provider_name, rating, status, summary, image_url, archive, created_at
		FROM series WHERE source_url = ?
	`, sourceURL).Scan(&ser.ID, &ser.Title, &ser.Author, &ser.SourceURL, &ser.ProviderName, &ser.Rating, &ser.Status, &ser.Summary, &ser.ImageURL, &ser.Archive, &ser.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ser, nil
}

func (s *Store) ListSeries() ([]models.Series, error) {
	return s.ListSeriesSorted("title")
}

func (s *Store) ListSeriesSorted(sortKey string) ([]models.Series, error) {
	orderBy := "s.title ASC"
	switch sortKey {
	case "provider":
		orderBy = "s.provider_name ASC, s.title ASC"
	case "rating":
		orderBy = "s.rating DESC, s.title ASC"
	case "status":
		orderBy = "s.status ASC, s.title ASC"
	case "author":
		orderBy = "s.author ASC, s.title ASC"
	}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT id, title, author, source_url, provider_name, rating, status, summary, image_url, archive, created_at
		FROM series s ORDER BY %s
	`, orderBy))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var series []models.Series
	for rows.Next() {
		var s models.Series
		if err := rows.Scan(&s.ID, &s.Title, &s.Author, &s.SourceURL, &s.ProviderName, &s.Rating, &s.Status, &s.Summary, &s.ImageURL, &s.Archive, &s.CreatedAt); err != nil {
			return nil, err
		}
		series = append(series, s)
	}
	return series, nil
}

func (s *Store) GetRatingDistribution() ([]models.RatingBucket, error) {
	rows, err := s.db.Query(`
		SELECT ROUND(rating, 1), COUNT(*)
		FROM series
		WHERE rating >= 0
		GROUP BY ROUND(rating, 1)
		ORDER BY ROUND(rating, 1) ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var buckets []models.RatingBucket
	for rows.Next() {
		var b models.RatingBucket
		if err := rows.Scan(&b.Rating, &b.Count); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}
	return buckets, nil
}

func (s *Store) UpdateSeriesStatus(id int64, status string) error {
	_, err := s.db.Exec("UPDATE series SET status = ? WHERE id = ?", status, id)
	return err
}

func (s *Store) UpdateSeriesRating(id int64, rating float64) error {
	_, err := s.db.Exec("UPDATE series SET rating = ? WHERE id = ?", rating, id)
	return err
}

func (s *Store) GetAllActiveSeries() ([]models.Series, error) {
	rows, err := s.db.Query(`
		SELECT id, title, author, source_url, provider_name, rating, status, summary, image_url, archive, created_at
		FROM series WHERE status IN ('active', 'binge')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var series []models.Series
	for rows.Next() {
		var s models.Series
		if err := rows.Scan(&s.ID, &s.Title, &s.Author, &s.SourceURL, &s.ProviderName, &s.Rating, &s.Status, &s.Summary, &s.ImageURL, &s.Archive, &s.CreatedAt); err != nil {
			return nil, err
		}
		series = append(series, s)
	}
	return series, nil
}

func (s *Store) GetSeriesByID(id int64) (*models.Series, error) {
	var ser models.Series
	err := s.db.QueryRow(`
		SELECT id, title, author, source_url, provider_name, rating, status, summary, image_url, archive, created_at
		FROM series WHERE id = ?
	`, id).Scan(&ser.ID, &ser.Title, &ser.Author, &ser.SourceURL, &ser.ProviderName, &ser.Rating, &ser.Status, &ser.Summary, &ser.ImageURL, &ser.Archive, &ser.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &ser, nil
}

func (s *Store) DeleteSeries(id int64) error {
	_, err := s.db.Exec("DELETE FROM chapters WHERE series_id = ?", id)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM series WHERE id = ?", id)
	return err
}

func (s *Store) GetDashboardStats() (*DashboardStats, error) {
	var stats DashboardStats
	err := s.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM series) as total_series,
			(SELECT COUNT(*) FROM series WHERE status IN ('active', 'binge')) as active_series,
			(SELECT COUNT(*) FROM chapters WHERE is_read = 0) as unread_chapters
	`).Scan(&stats.TotalSeries, &stats.ActiveSeries, &stats.UnreadChapter)
	if err != nil {
		return nil, err
	}
	return &stats, nil
}

func (s *Store) SearchSeries(query string) ([]models.Series, error) {
	q := "%" + strings.ToLower(query) + "%"
	rows, err := s.db.Query(`
		SELECT id, title, author, source_url, provider_name, rating, status, summary, image_url, archive, created_at
		FROM series WHERE LOWER(title) LIKE ? OR LOWER(author) LIKE ?
		ORDER BY title ASC LIMIT 20
	`, q, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var series []models.Series
	for rows.Next() {
		var s models.Series
		if err := rows.Scan(&s.ID, &s.Title, &s.Author, &s.SourceURL, &s.ProviderName, &s.Rating, &s.Status, &s.Summary, &s.ImageURL, &s.Archive, &s.CreatedAt); err != nil {
			return nil, err
		}
		series = append(series, s)
	}
	return series, nil
}
