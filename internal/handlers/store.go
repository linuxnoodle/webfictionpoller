package handlers

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"

	"github.com/linuxnoodle/webfictionpoller/internal/blob"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

type Store struct {
	db   *sql.DB
	blob blob.Store // optional; when nil, comic pages fall back to DB BLOBs
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, blob: blobStore}
}

// SetStoreBlobStore overrides the blob backend on an existing Store. Used by
// tests that construct a Store after calling SetBlobStore, and by main.go to
// rebind after initialization order changes.
func (s *Store) SetBlobStore(b blob.Store) { s.blob = b }

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
