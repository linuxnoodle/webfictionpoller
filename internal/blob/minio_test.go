package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"time"
)

// TestMinIOStore exercises a live MinIO instance. Skipped unless
// BLOB_TEST_MINIO_ENDPOINT is set. Spin one up with:
//
//	docker run -p 9000:9000 -e MINIO_ROOT_USER=minio -e MINIO_ROOT_PASSWORD=minio123 minio/minio server /data
//
// Then:
//
//	BLOB_TEST_MINIO_ENDPOINT=localhost:9000 \
//	BLOB_TEST_MINIO_ACCESS_KEY=minio \
//	BLOB_TEST_MINIO_SECRET_KEY=minio123 \
//	go test ./internal/blob/... -run TestMinIOStore -v
func TestMinIOStore(t *testing.T) {
	endpoint := os.Getenv("BLOB_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BLOB_TEST_MINIO_ENDPOINT to run live MinIO test")
	}
	bucket := fmt.Sprintf("blob-test-%d", time.Now().UnixNano())
	ctx := context.Background()

	s, err := NewMinIOStore(ctx, MinIOConfig{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("BLOB_TEST_MINIO_ACCESS_KEY"),
		SecretKey: os.Getenv("BLOB_TEST_MINIO_SECRET_KEY"),
		Bucket:    bucket,
		UseTLS:    false,
	})
	if err != nil {
		t.Fatalf("NewMinIOStore: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort bucket cleanup.
		_ = s.DeleteAll(ctx, KindComicPage, 0)
		_ = s.DeleteAll(ctx, KindComicPage, 42)
	})

	t.Run("PutGetRoundtrip", func(t *testing.T) {
		payload := []byte("hello minio")
		if _, err := s.Put(ctx, KindComicPage, 42, "p0.jpg", bytes.NewReader(payload), PutOptions{ContentType: "image/jpeg"}); err != nil {
			t.Fatal(err)
		}
		r, err := s.Get(ctx, KindComicPage, 42, "p0.jpg")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		got, _ := readAll(r)
		if !bytes.Equal(got, payload) {
			t.Errorf("roundtrip mismatch: got %q want %q", got, payload)
		}
	})

	t.Run("GetMissing", func(t *testing.T) {
		_, err := s.Get(ctx, KindComicPage, 42, "missing.jpg")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expected fs.ErrNotExist, got %v", err)
		}
	})

	t.Run("Size", func(t *testing.T) {
		payload := bytes.Repeat([]byte("a"), 2048)
		if _, err := s.Put(ctx, KindBundle, 42, "b.epub", bytes.NewReader(payload), PutOptions{ContentType: "application/epub+zip"}); err != nil {
			t.Fatal(err)
		}
		n, err := s.Size(ctx, KindBundle, 42, "b.epub")
		if err != nil {
			t.Fatal(err)
		}
		if n != int64(len(payload)) {
			t.Errorf("Size=%d want %d", n, len(payload))
		}
	})

	t.Run("List", func(t *testing.T) {
		for _, n := range []string{"l1", "l2", "l3"} {
			if _, err := s.Put(ctx, KindChapterImage, 42, n, bytes.NewReader([]byte("x")), PutOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.List(ctx, KindChapterImage, 42)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Errorf("List len=%d want 3 (%v)", len(got), got)
		}
	})

	t.Run("DeleteAll", func(t *testing.T) {
		for _, n := range []string{"d1", "d2"} {
			if _, err := s.Put(ctx, KindComicPage, 7, n, bytes.NewReader([]byte("x")), PutOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DeleteAll(ctx, KindComicPage, 7); err != nil {
			t.Fatal(err)
		}
		got, err := s.List(ctx, KindComicPage, 7)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("DeleteAll left %d blobs: %v", len(got), got)
		}
	})

	t.Run("PresignURL", func(t *testing.T) {
		if _, err := s.Put(ctx, KindBundle, 99, "x.epub", bytes.NewReader([]byte("x")), PutOptions{}); err != nil {
			t.Fatal(err)
		}
		url, err := s.PresignURL(ctx, KindBundle, 99, "x.epub", 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if url == "" {
			t.Error("empty presigned URL")
		}
	})
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
	}
}
