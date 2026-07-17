package handlers

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/blob"
	"github.com/linuxnoodle/webfictionpoller/internal/database"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// newTestStoreWithBlob builds a Store backed by both a temp SQLite DB and a
// temp filesystem blob store, mirroring production wiring.
func newTestStoreWithBlob(t *testing.T) (*Store, *blob.FilesystemStore) {
	t.Helper()
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	db, err := database.Open(tmp.Name() + "?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	blobRoot, err := os.MkdirTemp("", "test-blob-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(blobRoot) })
	fsBlob, err := blob.NewFilesystemStore(blobRoot)
	if err != nil {
		t.Fatal(err)
	}

	s := NewStore(db)
	s.SetBlobStore(fsBlob)
	return s, fsBlob
}

// addComicSeriesForTest inserts a comic series + chapter and returns the chapter id.
func addComicSeriesForTest(t *testing.T, s *Store, title string) (seriesID, chapterID int64) {
	t.Helper()
	sid, err := s.AddComicSeries(models.ComicSeries{
		SourceID:     "src-" + title,
		Title:        title,
		SourceURL:    "https://mangadex.org/title/src-" + title,
		ProviderName: "mangadex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertComicChapters(sid, []models.ComicChapter{
		{SourceID: "ch-1", Title: "Ch 1", ChapterNum: "1"},
	}); err != nil {
		t.Fatal(err)
	}
	chapters, err := s.GetComicChapters(sid)
	if err != nil || len(chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d (%v)", len(chapters), err)
	}
	return sid, chapters[0].ID
}

func TestSaveComicPageUsesBlobStore(t *testing.T) {
	s, fsBlob := newTestStoreWithBlob(t)
	_, chapterID := addComicSeriesForTest(t, s, "PageStorage")

	payload := []byte("fake-jpeg-bytes")
	if err := s.SaveComicPage(chapterID, 0, "https://up/img/0.jpg", payload, "image/jpeg"); err != nil {
		t.Fatal(err)
	}

	// Data should NOT be in the DB BLOB column (we wrote it to the blob store).
	data, _, err := s.GetComicPageData(chapterID, 999) // nonexistent page
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Error("expected nil for nonexistent page")
	}

	// But the metadata row exists.
	pages, err := s.GetComicChapterPages(chapterID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Index != 0 {
		t.Errorf("expected 1 page metadata row, got %+v", pages)
	}

	// And the blob exists in the filesystem store.
	names, err := fsBlob.List(context.Background(), blob.KindComicPage, chapterID)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != PageBlobName(0) {
		t.Errorf("expected page-000000 in blob store, got %+v", names)
	}
}

func TestGetComicPageDataReadsFromBlobStore(t *testing.T) {
	s, _ := newTestStoreWithBlob(t)
	_, chapterID := addComicSeriesForTest(t, s, "PageRead")

	payload := []byte("roundtrip-payload")
	if err := s.SaveComicPage(chapterID, 5, "https://up/img/5.jpg", payload, "image/jpeg"); err != nil {
		t.Fatal(err)
	}

	got, ct, err := s.GetComicPageData(chapterID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("roundtrip mismatch: got %q want %q", got, payload)
	}
	if ct != "image/jpeg" {
		t.Errorf("content type = %q, want image/jpeg", ct)
	}
}

func TestGetComicPageReaderStreamsBlob(t *testing.T) {
	s, _ := newTestStoreWithBlob(t)
	_, chapterID := addComicSeriesForTest(t, s, "PageStream")

	payload := []byte("streamed")
	if err := s.SaveComicPage(chapterID, 2, "https://up/img/2.jpg", payload, "image/jpeg"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	r, ct, err := s.GetComicPageReader(ctx, chapterID, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if ct != "image/jpeg" {
		t.Errorf("content type = %q", ct)
	}
	buf := make([]byte, len(payload))
	if _, err := r.Read(buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, payload) {
		t.Errorf("stream mismatch: %q", buf)
	}
}

func TestComicPageBlobStoredReflectsCache(t *testing.T) {
	s, _ := newTestStoreWithBlob(t)
	_, chapterID := addComicSeriesForTest(t, s, "PageExists")

	ctx := context.Background()
	if s.ComicPageBlobStored(ctx, chapterID, 0) {
		t.Error("expected not stored before save")
	}
	if err := s.SaveComicPage(chapterID, 0, "u", []byte("x"), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if !s.ComicPageBlobStored(ctx, chapterID, 0) {
		t.Error("expected stored after save")
	}
}

func TestDeleteComicChapterBlobsClearsBlobStore(t *testing.T) {
	s, fsBlob := newTestStoreWithBlob(t)
	_, chapterID := addComicSeriesForTest(t, s, "PageDelete")

	for i := 0; i < 3; i++ {
		if err := s.SaveComicPage(chapterID, i, "u", []byte{byte(i)}, "image/jpeg"); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	names, _ := fsBlob.List(ctx, blob.KindComicPage, chapterID)
	if len(names) != 3 {
		t.Fatalf("expected 3 blobs pre-delete, got %d", len(names))
	}

	if err := s.DeleteComicChapterBlobs(ctx, chapterID); err != nil {
		t.Fatal(err)
	}
	names, _ = fsBlob.List(ctx, blob.KindComicPage, chapterID)
	if len(names) != 0 {
		t.Errorf("expected 0 blobs post-delete, got %d (%v)", len(names), names)
	}
}

func TestLegacyPathStoresInDBWhenNoBlob(t *testing.T) {
	// Construct a Store with NO blob store set. SaveComicPage must fall back
	// to the comic_pages.data BLOB column so existing installs keep working.
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })
	db, err := database.Open(tmp.Name() + "?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Store{db: db, blob: nil} // explicitly no blob store

	_, chapterID := addComicSeriesForTest(t, s, "Legacy")
	payload := []byte("legacy-bytes")
	if err := s.SaveComicPage(chapterID, 0, "u", payload, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	// Round-trip via the legacy column.
	got, _, err := s.GetComicPageData(chapterID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("legacy roundtrip mismatch: %q vs %q", got, payload)
	}
}
