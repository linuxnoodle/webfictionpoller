package handlers

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

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

func (s *Store) GetTimeView(page, pageSize int, sortBy string) ([]models.ChapterWithSeries, error) {
	orderCol := "c.published_at"
	if sortBy == "received" {
		orderCol = "c.created_at"
	}
	offset := page * pageSize
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT c.id, c.series_id, c.title, c.url, c.published_at, c.is_read, c.created_at,
		       s.title, s.author, s.provider_name, s.rating, s.source_url
		FROM chapters c
		JOIN series s ON c.series_id = s.id
		WHERE s.status IN ('active', 'binge')
		ORDER BY %s DESC
		LIMIT ? OFFSET ?
	`, orderCol), pageSize, offset)
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
	err := s.db.QueryRow("UPDATE chapters SET is_read = 1 WHERE id = ? RETURNING url", chapterID).Scan(&url)
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
			INSERT OR IGNORE INTO chapters (series_id, title, url, published_at)
			VALUES (?, ?, ?, ?)
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
		INSERT OR IGNORE INTO series (title, author, source_url, provider_name, rating, status, summary, image_url, archive)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, series.Title, series.Author, series.SourceURL, series.ProviderName, series.Rating, series.Status, series.Summary, series.ImageURL, series.Archive)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
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

func (s *Store) GetProviderConfig(name string) (*models.ProviderConfig, error) {
	var pc models.ProviderConfig
	err := s.db.QueryRow(`
		SELECT id, provider_name, cookie_data, last_polled, username, encrypted_password, login_tested
		FROM provider_configs WHERE provider_name = ?
	`, name).Scan(&pc.ID, &pc.ProviderName, &pc.CookieData, &pc.LastPolled, &pc.Username, &pc.EncryptedPassword, &pc.LoginTested)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pc, nil
}

func (s *Store) UpsertProviderConfig(name, cookieData, username, encryptedPassword string) error {
	_, err := s.db.Exec(`
		INSERT INTO provider_configs (provider_name, cookie_data, last_polled, username, encrypted_password, login_tested)
		VALUES (?, ?, ?, ?, ?, 0)
		ON CONFLICT(provider_name) DO UPDATE SET cookie_data = ?, last_polled = ?, username = ?, encrypted_password = ?, login_tested = 0
	`, name, cookieData, time.Now(), username, encryptedPassword, cookieData, time.Now(), username, encryptedPassword)
	return err
}

func (s *Store) UpdateLastPolled(name string) error {
	_, err := s.db.Exec("UPDATE provider_configs SET last_polled = ? WHERE provider_name = ?", time.Now(), name)
	return err
}

func (s *Store) SetLoginTested(name string, tested bool) error {
	_, err := s.db.Exec("UPDATE provider_configs SET login_tested = ? WHERE provider_name = ?", tested, name)
	return err
}

func (s *Store) MarkAllSeriesRead(seriesID int64) error {
	_, err := s.db.Exec("UPDATE chapters SET is_read = 1 WHERE series_id = ?", seriesID)
	return err
}

func (s *Store) MarkAllChaptersRead() error {
	_, err := s.db.Exec("UPDATE chapters SET is_read = 1 WHERE is_read = 0")
	return err
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

type DashboardStats struct {
	TotalSeries   int `json:"total_series"`
	ActiveSeries  int `json:"active_series"`
	UnreadChapter int `json:"unread_chapters"`
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

func (s *Store) ExportBackup() (*models.Backup, error) {
	seriesList, err := s.ListSeries()
	if err != nil {
		return nil, err
	}

	backup := &models.Backup{
		Version:    1,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Series:     make([]models.SeriesBackup, 0, len(seriesList)),
	}

	for _, ser := range seriesList {
		rows, err := s.db.Query(`
			SELECT title, url, published_at, is_read
			FROM chapters WHERE series_id = ?
			ORDER BY published_at ASC
		`, ser.ID)
		if err != nil {
			return nil, err
		}

		var chapters []models.ChapterBackup
		for rows.Next() {
			var ch models.ChapterBackup
			var publishedAt sql.NullString
			var isRead bool
			if err := rows.Scan(&ch.Title, &ch.URL, &publishedAt, &isRead); err != nil {
				rows.Close()
				return nil, err
			}
			if publishedAt.Valid {
				ch.PublishedAt = publishedAt.String
			}
			ch.IsRead = isRead
			chapters = append(chapters, ch)
		}
		rows.Close()

		backup.Series = append(backup.Series, models.SeriesBackup{
			Title:        ser.Title,
			Author:       ser.Author,
			SourceURL:    ser.SourceURL,
			ProviderName: ser.ProviderName,
			Rating:       ser.Rating,
			Status:       ser.Status,
			Summary:      ser.Summary,
			ImageURL:     ser.ImageURL,
			Archive:      ser.Archive,
			Chapters:     chapters,
		})
	}

	prows, err := s.db.Query(`SELECT provider_name, cookie_data FROM provider_configs`)
	if err == nil {
		backup.Providers = make(map[string]string)
		for prows.Next() {
			var name, data string
			if prows.Scan(&name, &data) == nil && data != "" {
				backup.Providers[name] = data
			}
		}
		prows.Close()
	}

	return backup, nil
}

func (s *Store) ImportBackup(backup *models.Backup) (imported, skipped int, err error) {
	for _, sb := range backup.Series {
		existing, qerr := s.GetSeriesBySourceURL(sb.SourceURL)
		if qerr != nil {
			return imported, skipped, qerr
		}
		if existing != nil {
			if err := s.UpdateSeriesRating(existing.ID, sb.Rating); err != nil {
				return imported, skipped, err
			}
			if err := s.UpdateSeriesStatus(existing.ID, sb.Status); err != nil {
				return imported, skipped, err
			}
			skipped++
			continue
		}

		ser := models.Series{
			Title:        sb.Title,
			Author:       sb.Author,
			SourceURL:    sb.SourceURL,
			ProviderName: sb.ProviderName,
			Rating:       sb.Rating,
			Status:       sb.Status,
			Summary:      sb.Summary,
			ImageURL:     sb.ImageURL,
			Archive:      sb.Archive,
		}
		id, aerr := s.AddSeries(ser)
		if aerr != nil {
			if strings.Contains(aerr.Error(), "UNIQUE constraint") {
				skipped++
				continue
			}
			return imported, skipped, aerr
		}

		for _, cb := range sb.Chapters {
			var publishedAt interface{}
			if cb.PublishedAt != "" {
				if t, parseErr := time.Parse(time.RFC3339, cb.PublishedAt); parseErr == nil {
					publishedAt = t
				} else {
					publishedAt = cb.PublishedAt
				}
			}
			if publishedAt == nil {
				publishedAt = time.Now()
			}
			_, cerr := s.db.Exec(`
				INSERT OR IGNORE INTO chapters (series_id, title, url, published_at, is_read)
				VALUES (?, ?, ?, ?, ?)
			`, id, cb.Title, cb.URL, publishedAt, cb.IsRead)
			if cerr != nil {
				return imported, skipped, cerr
			}
		}
		imported++
	}

	for name, data := range backup.Providers {
		s.UpsertProviderConfig(name, data, "", "")
	}

	return imported, skipped, nil
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

func (s *Store) UpdateSeriesArchive(id int64, archive bool) error {
	_, err := s.db.Exec("UPDATE series SET archive = ? WHERE id = ?", archive, id)
	return err
}

func (s *Store) GetArchivedSeries() ([]models.Series, error) {
	rows, err := s.db.Query(`
		SELECT id, title, author, source_url, provider_name, rating, status, summary, image_url, archive, created_at
		FROM series WHERE archive = 1 AND status IN ('active', 'binge')
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

func (s *Store) GetChaptersForArchive(seriesID int64) ([]models.Chapter, error) {
	rows, err := s.db.Query(`
		SELECT id, series_id, title, url, published_at, is_read, content_html, COALESCE(content_compressed, 0), created_at
		FROM chapters WHERE series_id = ?
		ORDER BY published_at ASC
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chapters []models.Chapter
	for rows.Next() {
		var ch models.Chapter
		var contentBytes []byte
		var compressed bool
		if err := rows.Scan(&ch.ID, &ch.SeriesID, &ch.Title, &ch.URL, &ch.PublishedAt, &ch.IsRead, &contentBytes, &compressed, &ch.CreatedAt); err != nil {
			return nil, err
		}
		if compressed && len(contentBytes) > 0 {
			reader, err := gzip.NewReader(bytes.NewReader(contentBytes))
			if err == nil {
				decompressed, err := io.ReadAll(reader)
				reader.Close()
				if err == nil {
					ch.ContentHTML = string(decompressed)
				}
			}
			if ch.ContentHTML == "" {
				ch.ContentHTML = string(contentBytes)
			}
		} else {
			ch.ContentHTML = string(contentBytes)
		}
		chapters = append(chapters, ch)
	}
	return chapters, nil
}

func (s *Store) GetChapterArchivedContent(id int64) (string, error) {
	var contentBytes []byte
	var compressed bool
	err := s.db.QueryRow(`
		SELECT content_html, COALESCE(content_compressed, 0) FROM chapters WHERE id = ?
	`, id).Scan(&contentBytes, &compressed)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(contentBytes) == 0 {
		return "", nil
	}
	if compressed {
		reader, err := gzip.NewReader(bytes.NewReader(contentBytes))
		if err == nil {
			decompressed, err := io.ReadAll(reader)
			reader.Close()
			if err == nil {
				return string(decompressed), nil
			}
		}
	}
	return string(contentBytes), nil
}

func (s *Store) SaveChapterContent(id int64, content string) error {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write([]byte(content))
	gw.Close()
	_, err := s.db.Exec("UPDATE chapters SET content_html = ?, content_compressed = 1 WHERE id = ?", buf.Bytes(), id)
	return err
}

func (s *Store) GetChaptersNeedingContent(seriesID int64) ([]models.Chapter, error) {
	rows, err := s.db.Query(`
		SELECT id, series_id, title, url, published_at, is_read, content_html, created_at
		FROM chapters WHERE series_id = ? AND (content_html IS NULL OR content_html = '')
		ORDER BY published_at ASC
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chapters []models.Chapter
	for rows.Next() {
		var ch models.Chapter
		if err := rows.Scan(&ch.ID, &ch.SeriesID, &ch.Title, &ch.URL, &ch.PublishedAt, &ch.IsRead, &ch.ContentHTML, &ch.CreatedAt); err != nil {
			return nil, err
		}
		chapters = append(chapters, ch)
	}
	return chapters, nil
}

func (s *Store) SaveChapterImage(chapterID int64, url string, data []byte, contentType string) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO chapter_images (chapter_id, url, data, content_type)
		VALUES (?, ?, ?, ?)
	`, chapterID, url, data, contentType)
	return err
}

func (s *Store) GetChapterImage(chapterID int64, url string) ([]byte, string, error) {
	var data []byte
	var contentType string
	err := s.db.QueryRow(`
		SELECT data, content_type FROM chapter_images WHERE chapter_id = ? AND url = ?
	`, chapterID, url).Scan(&data, &contentType)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}

func (s *Store) GetArchiveStats(archiveAll bool) ([]models.ArchiveStat, error) {
	where := "s.status IN ('active', 'binge')"
	if !archiveAll {
		where = "s.archive = 1 AND " + where
	}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT s.id, s.title,
			(SELECT COUNT(*) FROM chapters c WHERE c.series_id = s.id),
			(SELECT COUNT(*) FROM chapters c WHERE c.series_id = s.id AND c.content_html IS NOT NULL AND c.content_html != ''),
			COALESCE((SELECT SUM(LENGTH(c.content_html)) FROM chapters c WHERE c.series_id = s.id AND c.content_html IS NOT NULL AND c.content_html != ''), 0)
		FROM series s
		WHERE %s
		ORDER BY s.title ASC
	`, where))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []models.ArchiveStat
	for rows.Next() {
		var st models.ArchiveStat
		if err := rows.Scan(&st.SeriesID, &st.SeriesTitle, &st.TotalChapters, &st.ArchivedChapters, &st.StorageBytes); err != nil {
			return nil, err
		}
		if st.TotalChapters > 0 {
			st.Percent = int(float64(st.ArchivedChapters) / float64(st.TotalChapters) * 100)
		}
		st.Complete = st.ArchivedChapters == st.TotalChapters && st.TotalChapters > 0
		stats = append(stats, st)
	}
	return stats, nil
}

func (s *Store) GetSetting(key string) string {
	var value string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err != nil {
		return ""
	}
	return value
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value)
	return err
}

func (s *Store) GetReaderChapters(seriesID int64) ([]models.Chapter, error) {
	rows, err := s.db.Query(`
		SELECT id, series_id, title, url, published_at, is_read,
		       CASE WHEN content_html IS NOT NULL AND content_html != '' THEN 1 ELSE 0 END as has_content
		FROM chapters WHERE series_id = ?
		ORDER BY published_at ASC
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chapters []models.Chapter
	for rows.Next() {
		var ch models.Chapter
		var hasContent bool
		if err := rows.Scan(&ch.ID, &ch.SeriesID, &ch.Title, &ch.URL, &ch.PublishedAt, &ch.IsRead, &hasContent); err != nil {
			return nil, err
		}
		if hasContent {
			ch.ContentHTML = "archived"
		}
		chapters = append(chapters, ch)
	}
	return chapters, nil
}

func (s *Store) GetReaderChapterContent(id int64) (string, int64, error) {
	var contentBytes []byte
	var compressed bool
	var seriesID int64
	err := s.db.QueryRow(`
		SELECT content_html, COALESCE(content_compressed, 0), series_id FROM chapters WHERE id = ?
	`, id).Scan(&contentBytes, &compressed, &seriesID)
	if err == sql.ErrNoRows {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if len(contentBytes) == 0 {
		return "", seriesID, nil
	}
	if compressed {
		reader, err := gzip.NewReader(bytes.NewReader(contentBytes))
		if err == nil {
			decompressed, err := io.ReadAll(reader)
			reader.Close()
			if err == nil {
				return string(decompressed), seriesID, nil
			}
		}
	}
	return string(contentBytes), seriesID, nil
}

func (s *Store) GetReadingProgress(seriesID int64) (int64, float64, error) {
	var chapterID int64
	var scrollPos float64
	err := s.db.QueryRow(`
		SELECT chapter_id, scroll_position FROM reading_progress WHERE series_id = ?
	`, seriesID).Scan(&chapterID, &scrollPos)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return chapterID, scrollPos, nil
}

func (s *Store) SaveReadingProgress(seriesID, chapterID int64, scrollPos float64) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO reading_progress (series_id, chapter_id, scroll_position, updated_at)
		VALUES (?, ?, ?, ?)
	`, seriesID, chapterID, scrollPos, time.Now())
	return err
}

func (s *Store) GetAdjacentChapterIDs(chapterID int64) (prev, next int64, err error) {
	var seriesID int64
	var publishedAt time.Time
	err = s.db.QueryRow(`SELECT series_id, published_at FROM chapters WHERE id = ?`, chapterID).Scan(&seriesID, &publishedAt)
	if err != nil {
		return 0, 0, err
	}
	s.db.QueryRow(`SELECT id FROM chapters WHERE series_id = ? AND published_at < ? ORDER BY published_at DESC LIMIT 1`, seriesID, publishedAt).Scan(&prev)
	s.db.QueryRow(`SELECT id FROM chapters WHERE series_id = ? AND published_at > ? ORDER BY published_at ASC LIMIT 1`, seriesID, publishedAt).Scan(&next)
	return prev, next, nil
}

func (s *Store) GetChapterCount(seriesID int64) (total, archived int, err error) {
	err = s.db.QueryRow(`
		SELECT COUNT(*), SUM(CASE WHEN content_html IS NOT NULL AND content_html != '' THEN 1 ELSE 0 END)
		FROM chapters WHERE series_id = ?
	`, seriesID).Scan(&total, &archived)
	return
}

func (s *Store) DeleteSeriesArchive(seriesID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`DELETE FROM chapter_images WHERE chapter_id IN (SELECT id FROM chapters WHERE series_id = ?)`, seriesID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`UPDATE chapters SET content_html = '', content_compressed = 0, preview_html = '' WHERE series_id = ?`, seriesID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) DeleteChapterArchive(chapterID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`DELETE FROM chapter_images WHERE chapter_id = ?`, chapterID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`UPDATE chapters SET content_html = '', content_compressed = 0, preview_html = '' WHERE id = ?`, chapterID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) GetStorageInfo() (*models.StorageInfo, error) {
	info := &models.StorageInfo{}

	err := s.db.QueryRow(`
		SELECT
			COALESCE(SUM(LENGTH(content_html)), 0),
			COALESCE(SUM(LENGTH(preview_html)), 0),
			COUNT(CASE WHEN content_html IS NOT NULL AND content_html != '' THEN 1 END),
			COUNT(CASE WHEN preview_html IS NOT NULL AND preview_html != '' THEN 1 END)
		FROM chapters
	`).Scan(&info.ContentBytes, &info.PreviewBytes, &info.ChapterCount, &info.ImageCount)
	if err != nil {
		return nil, err
	}

	err = s.db.QueryRow(`
		SELECT COALESCE(SUM(LENGTH(data)), 0), COUNT(*)
		FROM chapter_images
	`).Scan(&info.ImageBytes, &info.ImageCount)
	if err != nil {
		return nil, err
	}

	info.TotalBytes = info.ContentBytes + info.PreviewBytes + info.ImageBytes

	rows, err := s.db.Query(`
		SELECT s.id, s.title,
			(SELECT COUNT(*) FROM chapters c WHERE c.series_id = s.id AND c.content_html IS NOT NULL AND c.content_html != ''),
			(SELECT COUNT(*) FROM chapters c WHERE c.series_id = s.id),
			COALESCE((SELECT SUM(LENGTH(c.content_html)) FROM chapters c WHERE c.series_id = s.id AND c.content_html IS NOT NULL AND c.content_html != ''), 0),
			COALESCE((SELECT COUNT(*) FROM chapter_images ci WHERE ci.chapter_id IN (SELECT id FROM chapters WHERE series_id = s.id)), 0),
			COALESCE((SELECT SUM(LENGTH(ci.data)) FROM chapter_images ci WHERE ci.chapter_id IN (SELECT id FROM chapters WHERE series_id = s.id)), 0)
		FROM series s
		WHERE s.status IN ('active', 'binge')
		HAVING COALESCE((SELECT SUM(LENGTH(c.content_html)) FROM chapters c WHERE c.series_id = s.id AND c.content_html IS NOT NULL AND c.content_html != ''), 0) > 0
		OR COALESCE((SELECT SUM(LENGTH(ci.data)) FROM chapter_images ci WHERE ci.chapter_id IN (SELECT id FROM chapters WHERE series_id = s.id)), 0) > 0
		ORDER BY COALESCE((SELECT SUM(LENGTH(c.content_html)) FROM chapters c WHERE c.series_id = s.id AND c.content_html IS NOT NULL AND c.content_html != ''), 0) DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ss models.SeriesStorage
		if err := rows.Scan(&ss.SeriesID, &ss.SeriesTitle, &ss.ArchivedChapters, &ss.TotalChapters, &ss.ContentBytes, &ss.ImageCount, &ss.ImageBytes); err != nil {
			return nil, err
		}
		info.PerSeries = append(info.PerSeries, ss)
	}

	return info, nil
}

func (s *Store) TriggerReArchive(seriesID int64) (int, error) {
	_, err := s.db.Exec(`UPDATE chapters SET content_html = '', content_compressed = 0 WHERE series_id = ?`, seriesID)
	if err != nil {
		return 0, err
	}
	var count int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM chapters WHERE series_id = ?`, seriesID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) GetReaderSettings() (string, error) {
	var val string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = 'reader_settings'").Scan(&val)
	if err == sql.ErrNoRows {
		return "{}", nil
	}
	return val, err
}

func (s *Store) SaveReaderSettings(jsonStr string) error {
	_, err := s.db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('reader_settings', ?)", jsonStr)
	return err
}
