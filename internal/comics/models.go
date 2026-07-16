// Package comics hosts comic-provider implementations (currently MangaDex).
//
// Domain types (ComicSeries, ComicChapter, ComicPage, MangasPage) have moved
// to internal/models to break import cycles with internal/plugin. They are
// re-exported here as type aliases so existing callers
// (comics.ComicSeries etc.) continue to compile unchanged.
package comics

import "github.com/linuxnoodle/webfictionpoller/internal/models"

type ComicSeries = models.ComicSeries
type ComicChapter = models.ComicChapter
type ComicPage = models.ComicPage
type MangasPage = models.MangasPage
