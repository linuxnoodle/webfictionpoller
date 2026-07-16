package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/blob"
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
	// Always record metadata (page_index + image_url + content_type) in the DB
	// so listings stay queryable. Image bytes go to the blob store when present;
	// we fall back to the legacy comic_pages.data BLOB column only when no
	// blob store is wired (tests, very old installs).
	hasBlob := s.blob != nil && len(data) > 0
	var dataArg interface{}
	if !hasBlob {
		dataArg = data // legacy path: store bytes inline
	}
	if _, err := s.db.Exec(`
		INSERT OR REPLACE INTO comic_pages (chapter_id, page_index, image_url, data, content_type)
		VALUES (?, ?, ?, ?, ?)
	`, chapterID, pageIndex, imageURL, dataArg, contentType); err != nil {
		return err
	}
	if hasBlob {
		ctx := context.Background()
		if _, err := s.blob.Put(ctx, blob.KindComicPage, chapterID, PageBlobName(pageIndex), bytes.NewReader(data), blob.PutOptions{ContentType: contentType}); err != nil {
			return fmt.Errorf("writing comic page blob: %w", err)
		}
	}
	return nil
}

func (s *Store) GetComicPageData(chapterID int64, pageIndex int) ([]byte, string, error) {
	// Preferred path: blob store.
	if s.blob != nil {
		ctx := context.Background()
		r, err := s.blob.Get(ctx, blob.KindComicPage, chapterID, PageBlobName(pageIndex))
		if err == nil {
			defer r.Close()
			data, readErr := io.ReadAll(r)
			if readErr != nil {
				return nil, "", readErr
			}
			ct := s.comicPageContentType(chapterID, pageIndex)
			if ct == "" {
				ct = http.DetectContentType(data)
			}
			return data, ct, nil
		}
		// Fall through to legacy BLOB only on NotExist; surface other errors.
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, "", err
		}
	}
	// Legacy path: read bytes from comic_pages.data.
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

// GetComicPageReader returns a streaming reader for a cached page, preferring
// the blob store. Caller closes the reader. Returns (nil, "", nil) when the
// page is not cached locally — caller should fetch from upstream.
func (s *Store) GetComicPageReader(ctx context.Context, chapterID int64, pageIndex int) (io.ReadCloser, string, error) {
	if s.blob != nil {
		r, err := s.blob.Get(ctx, blob.KindComicPage, chapterID, PageBlobName(pageIndex))
		if err == nil {
			ct := s.comicPageContentType(chapterID, pageIndex)
			return r, ct, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, "", err
		}
	}
	data, ct, err := s.GetComicPageData(chapterID, pageIndex)
	if err != nil {
		return nil, "", err
	}
	if data == nil {
		return nil, "", nil
	}
	return io.NopCloser(bytes.NewReader(data)), ct, nil
}

// comicPageContentType looks up the stored content_type for a page. Used to
// avoid re-detecting MIME on every serve.
func (s *Store) comicPageContentType(chapterID int64, pageIndex int) string {
	var ct string
	_ = s.db.QueryRow(`SELECT content_type FROM comic_pages WHERE chapter_id = ? AND page_index = ?`, chapterID, pageIndex).Scan(&ct)
	return ct
}

// ComicPageBlobStored reports whether the page image lives in the blob store
// (vs needing upstream fetch). Used by download-status to count cached pages.
func (s *Store) ComicPageBlobStored(ctx context.Context, chapterID int64, pageIndex int) bool {
	if s.blob == nil {
		return false
	}
	_, err := s.blob.Size(ctx, blob.KindComicPage, chapterID, PageBlobName(pageIndex))
	return err == nil
}

// DeleteComicChapterBlobs removes every cached page image for a chapter from
// the blob store. Called when a chapter is deleted or re-downloaded.
func (s *Store) DeleteComicChapterBlobs(ctx context.Context, chapterID int64) error {
	if s.blob == nil {
		return nil
	}
	return s.blob.DeleteAll(ctx, blob.KindComicPage, chapterID)
}

// PageBlobName returns the stable blob-store object name for a page index.
// Exported so handlers and tests share the same naming scheme.
func PageBlobName(pageIndex int) string {
	return fmt.Sprintf("page-%06d", pageIndex)
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

	return n, nil
}
