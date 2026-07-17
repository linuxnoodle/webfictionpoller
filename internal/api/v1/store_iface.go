// Package v1 hosts the canonical, versioned JSON API. All routes are mounted
// under /api/v1 by the parent api package's Router function.
//
// Design notes:
//   - DTOs in dto.go decouple the wire format from internal models so the
//     schema can evolve without breaking mobile clients.
//   - Every handler reads the authenticated userID from the request context
//     (set by api.Authenticator).
//   - Errors use the structured {error, detail} envelope via writeAPIError.
// Package v1 hosts the canonical, versioned JSON API. All routes are mounted
// under /api/v1 by the parent api package's Router function.
//
// Design notes:
//   - DTOs in dto.go decouple the wire format from internal models so the
//     schema can evolve without breaking mobile clients.
//   - Every handler reads the authenticated userID from the request context
//     (set by api.Authenticator).
//   - Errors use the structured {error, detail} envelope via writeAPIError.
//
// The Store interface (in store_iface.go) is consumed by Server rather than
// a concrete handlers.Store, so any persistence backend implementing the
// methods satisfies v1. handlers.Store satisfies it via structural typing.
package v1

import (
	"context"
	"io"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// Store captures every persistence operation the v1 handlers need. Declared
// here (not imported) so this package doesn't depend on handlers; any backend
// implementing these methods satisfies v1. handlers.Store implements it today
// via structural typing; a future split into domain stores can satisfy it
// equally well by composing them.
//
// Methods are grouped by domain for readability — they remain a single
// interface because the handlers freely mix reads across domains (e.g.
// chapter feed joins series metadata).
type Store interface {
	// --- Text series ---
	ListSeries() ([]models.Series, error)
	GetSeriesByID(id int64) (*models.Series, error)
	GetAllActiveSeries() ([]models.Series, error)

	// --- Text chapters ---
	GetReaderChapters(seriesID int64) ([]models.Chapter, error)
	GetTimeView(page, pageSize int, sortBy string, unreadOnly bool) ([]models.ChapterWithSeries, error)
	GetChapterWithProvider(chapterID int64) (*models.ChapterWithSeries, error)
	GetChapterArchivedContent(id int64) (string, error)
	MarkChapterRead(chapterID int64) (string, error)

	// --- Dashboard ---
	GetDashboardStats() (models.DashboardStats, error)

	// --- Comic series / chapters / pages ---
	ListComicSeries() ([]models.ComicSeries, error)
	GetComicSeriesByID(id int64) (*models.ComicSeries, error)
	GetComicChapters(seriesID int64) ([]models.ComicChapter, error)
	GetComicChapterByID(id int64) (*models.ComicChapter, error)
	GetComicChapterPages(chapterID int64) ([]models.ComicPage, error)
	GetComicPageReader(ctx context.Context, chapterID int64, pageIndex int) (io.ReadCloser, string, error)
	ComicPageBlobStored(ctx context.Context, chapterID int64, pageIndex int) bool

	// --- Settings ---
	GetSetting(key string) string

	// --- Comic page writes ---
	SaveComicPage(chapterID int64, pageIndex int, imageURL string, data []byte, contentType string) error

	// --- Multi-source failover ---
	AddSource(seriesID int64, providerName, sourceURL string, priority int) (*models.SeriesSource, error)
	ListSources(seriesID int64) ([]models.SeriesSource, error)
	UpdateSource(id int64, priority int, disabled bool) error
	DeleteSource(id int64) error
	PromoteSource(id int64) error
	GetSourceByID(id int64) (*models.SeriesSource, error)
}

// DashboardCounts is the small summary object surfaced by /unread-count and
// the dashboard. Kept in v1 to avoid coupling to handlers types.
type DashboardCounts struct {
	TotalSeries   int `json:"total_series"`
	ActiveSeries  int `json:"active_series"`
	UnreadChapter int `json:"unread_chapters"`
}

// pollStatus is the worker pool's progress shape; we accept any type that
// JSON-serializes to {active, total, done}. Keeping it as an interface{}
// field on the response avoids importing worker types here.
//
// (Concrete wiring happens in main.go.)
var _ time.Duration // keep the time import alive for dto.go neighbours
