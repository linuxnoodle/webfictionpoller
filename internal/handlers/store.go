package handlers

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func decompressGzip(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return string(data)
	}
	defer reader.Close()
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return string(data)
	}
	return string(decompressed)
}

func scanSeries(rows *sql.Rows) (models.Series, error) {
	var s models.Series
	err := rows.Scan(&s.ID, &s.Title, &s.Author, &s.SourceURL, &s.ProviderName, &s.Rating, &s.Status, &s.Summary, &s.ImageURL, &s.Archive, &s.CreatedAt)
	return s, err
}

type DashboardStats struct {
	TotalSeries   int `json:"total_series"`
	ActiveSeries  int `json:"active_series"`
	UnreadChapter int `json:"unread_chapters"`
}
