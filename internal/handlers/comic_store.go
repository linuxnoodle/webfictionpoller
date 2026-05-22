package handlers

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/comics"
)

func (s *Store) AddComicSeries(series comics.ComicSeries) (int64, error) {
	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO comic_series (source_id, title, author, artist, description, cover_url, source_url, provider_name, status, genres, rating)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, series.SourceID, series.Title, series.Author, series.Artist, series.Description, series.CoverURL, series.SourceURL, series.ProviderName, series.Status, series.Genres, series.Rating)
	if err != nil {
		return 0, err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		var id int64
		s.db.QueryRow("SELECT id FROM comic_series WHERE source_url = ?", series.SourceURL).Scan(&id)
		return id, nil
	}
	return result.LastInsertId()
}

func (s *Store) GetComicSeriesBySourceURL(sourceURL string) (*comics.ComicSeries, error) {
	var cs comics.ComicSeries
	err := s.db.QueryRow(`
		SELECT id, source_id, title, author, artist, description, cover_url, source_url, provider_name, status, genres, rating, created_at
		FROM comic_series WHERE source_url = ?
	`, sourceURL).Scan(&cs.ID, &cs.SourceID, &cs.Title, &cs.Author, &cs.Artist, &cs.Description, &cs.CoverURL, &cs.SourceURL, &cs.ProviderName, &cs.Status, &cs.Genres, &cs.Rating, &cs.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cs, nil
}

func (s *Store) GetComicSeriesByID(id int64) (*comics.ComicSeries, error) {
	var cs comics.ComicSeries
	err := s.db.QueryRow(`
		SELECT id, source_id, title, author, artist, description, cover_url, source_url, provider_name, status, genres, rating, created_at
		FROM comic_series WHERE id = ?
	`, id).Scan(&cs.ID, &cs.SourceID, &cs.Title, &cs.Author, &cs.Artist, &cs.Description, &cs.CoverURL, &cs.SourceURL, &cs.ProviderName, &cs.Status, &cs.Genres, &cs.Rating, &cs.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &cs, nil
}

func (s *Store) ListComicSeries() ([]comics.ComicSeries, error) {
	rows, err := s.db.Query(`
		SELECT id, source_id, title, author, artist, description, cover_url, source_url, provider_name, status, genres, rating, created_at
		FROM comic_series ORDER BY title ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var series []comics.ComicSeries
	for rows.Next() {
		var cs comics.ComicSeries
		if err := rows.Scan(&cs.ID, &cs.SourceID, &cs.Title, &cs.Author, &cs.Artist, &cs.Description, &cs.CoverURL, &cs.SourceURL, &cs.ProviderName, &cs.Status, &cs.Genres, &cs.Rating, &cs.CreatedAt); err != nil {
			return nil, err
		}
		series = append(series, cs)
	}
	return series, nil
}

func (s *Store) GetComicChapterCounts(seriesID int64) (total, read, downloaded int, err error) {
	err = s.db.QueryRow(`
		SELECT COUNT(*), SUM(CASE WHEN is_read THEN 1 ELSE 0 END), SUM(CASE WHEN downloaded THEN 1 ELSE 0 END)
		FROM comic_chapters WHERE series_id = ?
	`, seriesID).Scan(&total, &read, &downloaded)
	return
}

func (s *Store) UpsertComicChapters(seriesID int64, chapters []comics.ComicChapter) (int, error) {
	inserted := 0
	for _, ch := range chapters {
		result, err := s.db.Exec(`
			INSERT OR IGNORE INTO comic_chapters (series_id, source_id, title, chapter_num, volume_num, source_url, pages, published_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, seriesID, ch.SourceID, ch.Title, ch.ChapterNum, ch.VolumeNum, ch.SourceURL, ch.Pages, ch.PublishedAt)
		if err != nil {
			return inserted, err
		}
		if n, _ := result.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

func (s *Store) GetComicChapters(seriesID int64) ([]comics.ComicChapter, error) {
	rows, err := s.db.Query(`
		SELECT id, series_id, source_id, title, chapter_num, volume_num, source_url, pages, is_read, downloaded, published_at
		FROM comic_chapters WHERE series_id = ?
		ORDER BY chapter_num ASC, id ASC
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chapters []comics.ComicChapter
	for rows.Next() {
		var ch comics.ComicChapter
		if err := rows.Scan(&ch.ID, &ch.SeriesID, &ch.SourceID, &ch.Title, &ch.ChapterNum, &ch.VolumeNum, &ch.SourceURL, &ch.Pages, &ch.IsRead, &ch.Downloaded, &ch.PublishedAt); err != nil {
			return nil, err
		}
		chapters = append(chapters, ch)
	}
	return chapters, nil
}

func (s *Store) GetComicChapterByID(id int64) (*comics.ComicChapter, error) {
	var ch comics.ComicChapter
	err := s.db.QueryRow(`
		SELECT id, series_id, source_id, title, chapter_num, volume_num, source_url, pages, is_read, downloaded, published_at
		FROM comic_chapters WHERE id = ?
	`, id).Scan(&ch.ID, &ch.SeriesID, &ch.SourceID, &ch.Title, &ch.ChapterNum, &ch.VolumeNum, &ch.SourceURL, &ch.Pages, &ch.IsRead, &ch.Downloaded, &ch.PublishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

func (s *Store) MarkComicChapterRead(chapterID int64) error {
	_, err := s.db.Exec("UPDATE comic_chapters SET is_read = 1 WHERE id = ?", chapterID)
	return err
}

func (s *Store) MarkComicChapterDownloaded(chapterID int64) error {
	_, err := s.db.Exec("UPDATE comic_chapters SET downloaded = 1 WHERE id = ?", chapterID)
	return err
}

func (s *Store) SaveComicPage(chapterID int64, pageIndex int, imageURL string, data []byte, contentType string) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO comic_pages (chapter_id, page_index, image_url, data, content_type)
		VALUES (?, ?, ?, ?, ?)
	`, chapterID, pageIndex, imageURL, data, contentType)
	return err
}

func (s *Store) GetComicPageData(chapterID int64, pageIndex int) ([]byte, string, error) {
	var data []byte
	var contentType string
	err := s.db.QueryRow(`
		SELECT data, content_type FROM comic_pages WHERE chapter_id = ? AND page_index = ?
	`, chapterID, pageIndex).Scan(&data, &contentType)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}

func (s *Store) GetComicChapterPages(chapterID int64) ([]comics.ComicPage, error) {
	rows, err := s.db.Query(`
		SELECT page_index, image_url FROM comic_pages WHERE chapter_id = ?
		ORDER BY page_index ASC
	`, chapterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pages []comics.ComicPage
	for rows.Next() {
		var p comics.ComicPage
		if err := rows.Scan(&p.Index, &p.ImageURL); err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	return pages, nil
}

func (s *Store) GetComicReadingProgress(seriesID int64) (int64, int, error) {
	var chapterID int64
	var pageIndex int
	err := s.db.QueryRow(`
		SELECT chapter_id, page_index FROM comic_reading_progress WHERE series_id = ?
	`, seriesID).Scan(&chapterID, &pageIndex)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return chapterID, pageIndex, nil
}

func (s *Store) SaveComicReadingProgress(seriesID, chapterID int64, pageIndex int) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO comic_reading_progress (series_id, chapter_id, page_index, updated_at)
		VALUES (?, ?, ?, ?)
	`, seriesID, chapterID, pageIndex, time.Now())
	return err
}

func (s *Store) UpdateComicSeriesRating(id int64, rating float64) error {
	_, err := s.db.Exec("UPDATE comic_series SET rating = ? WHERE id = ?", rating, id)
	return err
}

func (s *Store) UpdateComicSeriesStatus(id int64, status string) error {
	_, err := s.db.Exec("UPDATE comic_series SET status = ? WHERE id = ?", status, id)
	return err
}

func (s *Store) DeleteComicSeries(id int64) error {
	_, err := s.db.Exec("DELETE FROM comic_series WHERE id = ?", id)
	return err
}

func (s *Store) MarkComicAllRead(seriesID int64) error {
	_, err := s.db.Exec("UPDATE comic_chapters SET is_read = 1 WHERE series_id = ?", seriesID)
	return err
}

func (s *Store) GetAdjacentComicChapterIDs(chapterID int64) (prev, next int64, err error) {
	var seriesID int64
	var chNum string
	err = s.db.QueryRow(`SELECT series_id, chapter_num FROM comic_chapters WHERE id = ?`, chapterID).Scan(&seriesID, &chNum)
	if err != nil {
		return 0, 0, err
	}
	s.db.QueryRow(`SELECT id FROM comic_chapters WHERE series_id = ? AND (CAST(chapter_num AS REAL) < CAST(? AS REAL) OR id < ?) ORDER BY CAST(chapter_num AS REAL) DESC, id DESC LIMIT 1`, seriesID, chNum, chapterID).Scan(&prev)
	s.db.QueryRow(`SELECT id FROM comic_chapters WHERE series_id = ? AND (CAST(chapter_num AS REAL) > CAST(? AS REAL) OR id > ?) ORDER BY CAST(chapter_num AS REAL) ASC, id ASC LIMIT 1`, seriesID, chNum, chapterID).Scan(&next)
	return prev, next, nil
}

func (s *Store) RefreshComicChapters(seriesID int64, provider comics.ComicProvider, sourceID string) (int, error) {
	chapters, err := provider.ChapterList(sourceID)
	if err != nil {
		return 0, fmt.Errorf("fetching chapters: %w", err)
	}
	if len(chapters) == 0 {
		return 0, nil
	}

	n, err := s.UpsertComicChapters(seriesID, chapters)
	if err != nil {
		return 0, err
	}

	var total int
	s.db.QueryRow("SELECT COUNT(*) FROM comic_chapters WHERE series_id = ?", seriesID).Scan(&total)

	return n, nil
}
