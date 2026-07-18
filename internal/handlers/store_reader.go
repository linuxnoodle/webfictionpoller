package handlers

import (
	"database/sql"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func (s *Store) GetReaderChapters(seriesID int64) ([]models.Chapter, error) {
	rows, err := s.db.Query(`
		SELECT id, series_id, title, url, published_at, is_read,
		       CASE WHEN content_html IS NOT NULL AND content_html != '' THEN 1 ELSE 0 END as has_content,
		       COALESCE(word_count, 0), COALESCE(premium, 0)
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
		var wordCount int
		var premium bool
		if err := rows.Scan(&ch.ID, &ch.SeriesID, &ch.Title, &ch.URL, &ch.PublishedAt, &ch.IsRead, &hasContent, &wordCount, &premium); err != nil {
			return nil, err
		}
		if hasContent {
			ch.ContentHTML = "archived"
		}
		// Stash word count in PreviewHTML field for now (avoids schema-level
		// changes to models.Chapter just to surface word_count; the reader
		// template can read it from the JSON payload directly).
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
		return decompressGzip(contentBytes), seriesID, nil
	}
	return string(contentBytes), seriesID, nil
}

// ChapterContentMeta is the metadata surfaced alongside the body HTML
// when the reader or API requests a chapter.
type ChapterContentMeta struct {
	HTML       string
	WordCount  int
	Premium    bool
	Title      string
	SeriesID   int64
	HasContent bool // true when HTML is cached locally
}

// GetChapterForReader returns the chapter body + metadata in a single query.
// When the chapter isn't cached locally, HasContent is false and HTML is
// empty; the caller (reader handler or API) can then decide to live-fetch
// via the provider's ContentFetcher.
func (s *Store) GetChapterForReader(id int64) (*ChapterContentMeta, error) {
	var m ChapterContentMeta
	var contentBytes []byte
	var compressed bool
	err := s.db.QueryRow(`
		SELECT content_html, COALESCE(content_compressed, 0),
		       COALESCE(word_count, 0), COALESCE(premium, 0),
		       title, series_id
		FROM chapters WHERE id = ?
	`, id).Scan(&contentBytes, &compressed, &m.WordCount, &m.Premium, &m.Title, &m.SeriesID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(contentBytes) > 0 {
		m.HasContent = true
		if compressed {
			m.HTML = decompressGzip(contentBytes)
		} else {
			m.HTML = string(contentBytes)
		}
	}
	return &m, nil
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
		INSERT INTO reading_progress (series_id, chapter_id, scroll_position, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (series_id) DO UPDATE SET chapter_id = EXCLUDED.chapter_id, scroll_position = EXCLUDED.scroll_position, updated_at = EXCLUDED.updated_at
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

func (s *Store) GetReaderSettings() (string, error) {
	var val string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = 'reader_settings'").Scan(&val)
	if err == sql.ErrNoRows {
		return "{}", nil
	}
	return val, err
}

func (s *Store) SaveReaderSettings(jsonStr string) error {
	_, err := s.db.Exec("INSERT INTO settings (key, value) VALUES ('reader_settings', ?) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", jsonStr)
	return err
}
