package handlers

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"fmt"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func (s *Store) UpdateSeriesArchive(id int64, archive bool) error {
	_, err := s.db.Exec("UPDATE series SET archive = ? WHERE id = ?", archive, id)
	return err
}

func (s *Store) GetArchivedSeries() ([]models.Series, error) {
	rows, err := s.db.Query(`
		SELECT id, title, author, source_url, provider_name, rating, status, summary, image_url, archive, created_at
		FROM series WHERE archive = TRUE AND status IN ('active', 'binge')
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
		SELECT id, series_id, title, url, published_at, is_read, content_html, COALESCE(content_compressed, FALSE), created_at
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
			ch.ContentHTML = decompressGzip(contentBytes)
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
		SELECT content_html, COALESCE(content_compressed, FALSE) FROM chapters WHERE id = ?
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
		return decompressGzip(contentBytes), nil
	}
	return string(contentBytes), nil
}

func (s *Store) SaveChapterContent(id int64, content string) error {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write([]byte(content))
	gw.Close()
	_, err := s.db.Exec("UPDATE chapters SET content_html = ?, content_compressed = TRUE WHERE id = ?", buf.Bytes(), id)
	return err
}

// MarkChapterPremium flags a chapter as paywalled so the UI can show a
// "locked" badge and the scheduler stops retrying the fetch.
func (s *Store) MarkChapterPremium(id int64) error {
	_, err := s.db.Exec("UPDATE chapters SET premium = TRUE WHERE id = ?", id)
	return err
}

// SetChapterWordCount records the prose word count returned by the
// provider's ContentFetcher.
func (s *Store) SetChapterWordCount(id int64, n int) error {
	_, err := s.db.Exec("UPDATE chapters SET word_count = ? WHERE id = ?", n, id)
	return err
}

// GetChapterWordCount returns the stored word count (0 when unset).
func (s *Store) GetChapterWordCount(id int64) (int, error) {
	var n int
	err := s.db.QueryRow("SELECT word_count FROM chapters WHERE id = ?", id).Scan(&n)
	return n, err
}

// UpdateChapterTitle overrides the chapter title. Called by the archiver
// when the provider's ContentFetcher returns a richer title than what the
// discovery poll stored.
func (s *Store) UpdateChapterTitle(id int64, title string) error {
	_, err := s.db.Exec("UPDATE chapters SET title = ? WHERE id = ?", title, id)
	return err
}

func (s *Store) GetChaptersNeedingContent(seriesID int64) ([]models.Chapter, error) {
	rows, err := s.db.Query(`
		SELECT id, series_id, title, url, published_at, is_read, content_html, created_at
		FROM chapters WHERE series_id = ? AND (content_html IS NULL OR content_html = '') AND COALESCE(premium, FALSE) = FALSE
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
		INSERT INTO chapter_images (chapter_id, url, data, content_type)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (chapter_id, url) DO UPDATE SET data = EXCLUDED.data, content_type = EXCLUDED.content_type
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
		where = "s.archive = TRUE AND " + where
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

	_, err = tx.Exec(`UPDATE chapters SET content_html = '', content_compressed = FALSE, preview_html = '' WHERE series_id = ?`, seriesID)
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

	_, err = tx.Exec(`UPDATE chapters SET content_html = '', content_compressed = FALSE, preview_html = '' WHERE id = ?`, chapterID)
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
	_, err := s.db.Exec(`UPDATE chapters SET content_html = '', content_compressed = FALSE WHERE series_id = ?`, seriesID)
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
