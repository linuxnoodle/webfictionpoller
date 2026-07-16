// Package blob provides a content-addressable-ish binary store for chapter
// images, comic pages, generated EPUB/CBZ bundles, and any other large
// artifacts that should not live in the relational database.
//
// Two backends:
//
//   - FileSystem (default): data/blobs/{kind}/{id}/{name}
//   - MinIO/S3:             bucket {kind}/{id}/{name}, with presigned URLs
//
// Backends are selected via STORAGE_BACKEND=fs|minio. The interface is small
// enough that future backends (e.g. a single flat zip) fit without touching
// callers.
package blob

import (
	"context"
	"io"
	"time"
)

// Kind labels a blob category. Used as the top-level shard so unrelated blobs
// don't share a directory/object-prefix.
type Kind string

const (
	// KindChapterImage is an inline image extracted from a text chapter's HTML.
	KindChapterImage Kind = "chapter-image"
	// KindComicPage is a single comic page image.
	KindComicPage Kind = "comic-page"
	// KindBundle is a generated archive (EPUB/CBZ) for offline reading.
	KindBundle Kind = "bundle"
)

// PutOptions carries optional metadata. Backends may ignore unsupported fields.
type PutOptions struct {
	// ContentType is the MIME type (e.g. "image/webp"). Always set if known;
	// used to set Content-Type on serving and to pick file extensions.
	ContentType string
}

// Store is the binary blob contract. All methods are context-aware; callers
// should pass a deadline-bearing context for network backends.
type Store interface {
	// Put writes the blob named `name` under (kind, id), replacing any
	// existing blob with the same key. Returns the byte size written.
	Put(ctx context.Context, kind Kind, id int64, name string, r io.Reader, opts PutOptions) (int64, error)

	// Get returns a reader for the blob. The caller must close it.
	// Returns an io/fs.NotExistError-compatible error (fs.ErrNotExist) if
	// the blob is absent so callers can distinguish missing from corrupt.
	Get(ctx context.Context, kind Kind, id int64, name string) (io.ReadCloser, error)

	// Delete removes the blob. Missing blobs are not an error.
	Delete(ctx context.Context, kind Kind, id int64, name string) error

	// DeleteAll removes every blob under (kind, id). Used when a series or
	// chapter is deleted to avoid orphaned bytes.
	DeleteAll(ctx context.Context, kind Kind, id int64) error

	// Size returns the blob's byte size, or an error wrapping fs.ErrNotExist.
	Size(ctx context.Context, kind Kind, id int64, name string) (int64, error)

	// List returns the names of every blob under (kind, id), in undefined order.
	List(ctx context.Context, kind Kind, id int64) ([]string, error)

	// PresignURL returns a time-limited URL granting the holder direct GET
	// access to the blob. Returns ErrPresignUnsupported if the backend cannot
	// presign (e.g. filesystem); callers should fall back to proxying via
	// the Go server.
	PresignURL(ctx context.Context, kind Kind, id int64, name string, ttl time.Duration) (string, error)
}

// ErrPresignUnsupported is returned by Store.PresignURL on backends that
// cannot generate direct-access URLs.
type ErrPresignUnsupported struct{ Backend string }

func (e *ErrPresignUnsupported) Error() string { return "blob: presign unsupported by " + e.Backend }
