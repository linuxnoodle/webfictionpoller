package handlers

import "github.com/linuxnoodle/webfictionpoller/internal/blob"

// blobStore is the process-global binary blob store. Wired by cmd/main.go
// via SetBlobStore at startup. Handlers that produce/consume large binaries
// (chapter images, comic pages, generated EPUB/CBZ bundles) read this via
// BlobStore().
//
// Phase 2 wiring: currently only initialized. The archiver, comic handlers,
// and OPDS bundle endpoints migrate onto it incrementally.
var blobStore blob.Store

// SetBlobStore wires the process-global blob store. Called once at startup
// from cmd/main.go.
func SetBlobStore(s blob.Store) { blobStore = s }

// BlobStore returns the global blob store, or nil if not initialized.
// Callers must nil-check; legacy paths still use DB BLOB columns.
func BlobStore() blob.Store { return blobStore }
