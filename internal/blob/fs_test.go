package blob

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestFS(t *testing.T) *FilesystemStore {
	t.Helper()
	dir, err := os.MkdirTemp("", "blob-fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s, err := NewFilesystemStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFSPutAndGetRoundtrip(t *testing.T) {
	s := newTestFS(t)
	ctx := context.Background()
	payload := []byte("hello world")

	n, err := s.Put(ctx, KindComicPage, 42, "page-0.jpg", bytes.NewReader(payload), PutOptions{ContentType: "image/jpeg"})
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) {
		t.Errorf("Put returned size %d, want %d", n, len(payload))
	}

	r, err := s.Get(ctx, KindComicPage, 42, "page-0.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := make([]byte, len(payload))
	if _, err := r.Read(got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("roundtrip mismatch: got %q want %q", got, payload)
	}
}

func TestFSGetMissingReturnsFsErrNotExist(t *testing.T) {
	s := newTestFS(t)
	_, err := s.Get(context.Background(), KindComicPage, 99, "nope.jpg")
	if err == nil {
		t.Fatal("expected error for missing blob")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestFSPutOverwritesExisting(t *testing.T) {
	s := newTestFS(t)
	ctx := context.Background()

	if _, err := s.Put(ctx, KindChapterImage, 1, "img.png", bytes.NewReader([]byte("old")), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, KindChapterImage, 1, "img.png", bytes.NewReader([]byte("new content")), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(ctx, KindChapterImage, 1, "img.png")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := make([]byte, 11)
	n, _ := r.Read(got)
	if string(got[:n]) != "new content" {
		t.Errorf("expected overwrite, got %q", got[:n])
	}
}

func TestFSDeleteIsIdempotent(t *testing.T) {
	s := newTestFS(t)
	ctx := context.Background()
	if _, err := s.Put(ctx, KindBundle, 1, "x.cbz", bytes.NewReader([]byte("data")), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, KindBundle, 1, "x.cbz"); err != nil {
		t.Fatal(err)
	}
	// Second delete should not error.
	if err := s.Delete(ctx, KindBundle, 1, "x.cbz"); err != nil {
		t.Errorf("idempotent delete failed: %v", err)
	}
}

func TestFSDeleteAllRemovesDirectory(t *testing.T) {
	s := newTestFS(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b", "c"} {
		if _, err := s.Put(ctx, KindComicPage, 7, name, bytes.NewReader([]byte(name)), PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(s.Root(), string(KindComicPage), "7")
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAll(ctx, KindComicPage, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected dir removed, got err=%v", err)
	}
}

func TestFSListReturnsNames(t *testing.T) {
	s := newTestFS(t)
	ctx := context.Background()
	for _, name := range []string{"page-0.jpg", "page-1.jpg", "page-2.jpg"} {
		if _, err := s.Put(ctx, KindComicPage, 3, name, bytes.NewReader([]byte("x")), PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// Different id should not leak in.
	if _, err := s.Put(ctx, KindComicPage, 4, "page-0.jpg", bytes.NewReader([]byte("x")), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ctx, KindComicPage, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 entries, got %d (%v)", len(got), got)
	}
}

func TestFSListMissingIDReturnsNil(t *testing.T) {
	s := newTestFS(t)
	got, err := s.List(context.Background(), KindComicPage, 999)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for missing id, got %v", got)
	}
}

func TestFSSizeReturnsByteCount(t *testing.T) {
	s := newTestFS(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("a"), 4096)
	if _, err := s.Put(ctx, KindBundle, 1, "big.epub", bytes.NewReader(payload), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Size(ctx, KindBundle, 1, "big.epub")
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) {
		t.Errorf("Size = %d, want %d", n, len(payload))
	}
}

func TestFSPresignUnsupported(t *testing.T) {
	s := newTestFS(t)
	_, err := s.PresignURL(context.Background(), KindBundle, 1, "x", time.Minute)
	if _, ok := err.(*ErrPresignUnsupported); !ok {
		t.Errorf("expected ErrPresignUnsupported, got %T: %v", err, err)
	}
}

func TestFSValidateKeyRejectsBadInput(t *testing.T) {
	s := newTestFS(t)
	ctx := context.Background()
	cases := []struct {
		kind Kind
		id   int64
		name string
	}{
		{"", 1, "ok"},
		{KindComicPage, 0, "ok"},
		{KindComicPage, -1, "ok"},
		{KindComicPage, 1, ""},
		{KindComicPage, 1, "."},
		{KindComicPage, 1, ".."},
		{KindComicPage, 1, "a/b"},
		{KindComicPage, 1, "a\\b"},
	}
	for i, c := range cases {
		_, err := s.Put(ctx, c.kind, c.id, c.name, bytes.NewReader([]byte("x")), PutOptions{})
		if err == nil {
			t.Errorf("case %d: expected error for %+v, got nil", i, c)
		}
	}
}

func TestFSNoPathEscapeViaDots(t *testing.T) {
	s := newTestFS(t)
	// "page-0.jpg" but stored via ".." attempt — validateKey blocks it.
	_, err := s.Put(context.Background(), KindComicPage, 1, "..\\evil", bytes.NewReader([]byte("x")), PutOptions{})
	if err == nil {
		t.Fatal("expected path-escape rejection")
	}
}
