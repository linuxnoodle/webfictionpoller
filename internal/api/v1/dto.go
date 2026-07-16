package v1

import (
	"strconv"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// DTOs decouple the v1 wire format from internal model types. Adding a column
// to models.Series doesn't break mobile clients unless we explicitly add the
// field here.

// --- Text series ---

type seriesSummary struct {
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	Author       string    `json:"author,omitempty"`
	SourceURL    string    `json:"source_url"`
	ProviderName string    `json:"provider_name"`
	Rating       float64   `json:"rating"`
	Status       string    `json:"status"`
	Summary      string    `json:"summary,omitempty"`
	ImageURL     string    `json:"image_url,omitempty"`
	Archive      bool      `json:"archive"`
	CreatedAt    time.Time `json:"created_at"`
}

func toSeriesSummary(s models.Series) seriesSummary {
	return seriesSummary{
		ID:           s.ID,
		Title:        s.Title,
		Author:       s.Author,
		SourceURL:    s.SourceURL,
		ProviderName: s.ProviderName,
		Rating:       s.Rating,
		Status:       s.Status,
		Summary:      s.Summary,
		ImageURL:     s.ImageURL,
		Archive:      s.Archive,
		CreatedAt:    s.CreatedAt,
	}
}

// --- Comic series ---

type comicSeriesSummary struct {
	ID           int64   `json:"id"`
	SourceID     string  `json:"source_id"`
	Title        string  `json:"title"`
	Author       string  `json:"author,omitempty"`
	Artist       string  `json:"artist,omitempty"`
	Description  string  `json:"description,omitempty"`
	CoverURL     string  `json:"cover_url,omitempty"`
	SourceURL    string  `json:"source_url"`
	ProviderName string  `json:"provider_name"`
	Status       string  `json:"status"`
	Genres       string  `json:"genres,omitempty"`
	Rating       float64 `json:"rating"`
	CreatedAt    string  `json:"created_at,omitempty"`
}

func toComicSeriesSummary(s models.ComicSeries) comicSeriesSummary {
	return comicSeriesSummary{
		ID:           s.ID,
		SourceID:     s.SourceID,
		Title:        s.Title,
		Author:       s.Author,
		Artist:       s.Artist,
		Description:  s.Description,
		CoverURL:     s.CoverURL,
		SourceURL:    s.SourceURL,
		ProviderName: s.ProviderName,
		Status:       s.Status,
		Genres:       s.Genres,
		Rating:       s.Rating,
		CreatedAt:    s.CreatedAt,
	}
}

// --- Chapters (text) ---

type chapterFeedItem struct {
	ID          int64     `json:"id"`
	SeriesID    int64     `json:"series_id"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
	IsRead      bool      `json:"is_read"`
	SeriesTitle string    `json:"series_title,omitempty"`
	Provider    string    `json:"provider,omitempty"`
}

type readerChapter struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	PublishedAt time.Time `json:"published_at"`
	IsRead      bool      `json:"is_read"`
}

func toReaderChapters(in []models.Chapter) []readerChapter {
	out := make([]readerChapter, 0, len(in))
	for _, c := range in {
		out = append(out, readerChapter{
			ID:          c.ID,
			Title:       c.Title,
			PublishedAt: c.PublishedAt,
			IsRead:      c.IsRead,
		})
	}
	return out
}

// --- Comic chapters ---

type comicChapter struct {
	ID          int64  `json:"id"`
	SeriesID    int64  `json:"series_id"`
	Title       string `json:"title"`
	ChapterNum  string `json:"chapter_num,omitempty"`
	VolumeNum   string `json:"volume_num,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	Pages       int    `json:"pages"`
	IsRead      bool   `json:"is_read"`
	Downloaded  bool   `json:"downloaded"`
	PublishedAt string `json:"published_at,omitempty"`
}

func toComicChapters(in []models.ComicChapter) []comicChapter {
	out := make([]comicChapter, 0, len(in))
	for _, c := range in {
		out = append(out, comicChapter{
			ID:          c.ID,
			SeriesID:    c.SeriesID,
			Title:       c.Title,
			ChapterNum:  c.ChapterNum,
			VolumeNum:   c.VolumeNum,
			SourceURL:   c.SourceURL,
			Pages:       c.Pages,
			IsRead:      c.IsRead,
			Downloaded:  c.Downloaded,
			PublishedAt: c.PublishedAt,
		})
	}
	return out
}

// --- Parsing helpers (kept here so the server file imports stay minimal) ---

func parseID(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func parseIntParam(s string) (int, error) {
	return strconv.Atoi(s)
}
