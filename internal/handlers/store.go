package handlers

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"

	"github.com/linuxnoodle/webfictionpoller/internal/blob"
	"github.com/linuxnoodle/webfictionpoller/internal/db"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

type Store struct {
	db   *db.DB
	blob blob.Store // optional; when nil, comic pages fall back to DB BLOBs
}

func NewStore(database *db.DB) *Store {
	return &Store{db: database, blob: blobStore}
}

// SetStoreBlobStore overrides the blob backend on an existing Store. Used by
// tests that construct a Store after calling SetBlobStore, and by main.go to
// rebind after initialization order changes.
func (s *Store) SetBlobStore(b blob.Store) { s.blob = b }

// DB exposes the underlying *db.DB for callers (rare) that need lower-level
// access — e.g. database/sql.Exec of dialect-specific helpers. Most code
// should stay on Store methods.
func (s *Store) DB() *db.DB { return s.db }

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
