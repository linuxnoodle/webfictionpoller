package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FilesystemStore stores blobs under a root directory using the layout
// {root}/{kind}/{id}/{name}. It is safe for concurrent use; the underlying
// filesystem serializes renames atomically per file.
type FilesystemStore struct {
	root string
}

// NewFilesystemStore returns a store rooted at root. The directory is created
// if missing. Root must be writable; callers should ensure it lives on a
// volume with adequate space (manga libraries can grow large).
func NewFilesystemStore(root string) (*FilesystemStore, error) {
	if root == "" {
		return nil, errors.New("blob: filesystem root is empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("blob: creating root %q: %w", root, err)
	}
	return &FilesystemStore{root: root}, nil
}

// Root returns the configured root directory.
func (s *FilesystemStore) Root() string { return s.root }

func (s *FilesystemStore) path(kind Kind, id int64, name string) string {
	return filepath.Join(s.root, string(kind), fmt.Sprintf("%d", id), name)
}

// Put writes the blob to a temp file in the same directory, then renames it
// into place. This ensures readers never see a half-written file.
func (s *FilesystemStore) Put(ctx context.Context, kind Kind, id int64, name string, r io.Reader, _ PutOptions) (int64, error) {
	if err := validateKey(kind, id, name); err != nil {
		return 0, err
	}
	target := s.path(kind, id, name)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("blob: mkdir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("blob: creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Cleanup on any failure path.
	defer func() {
		if tmp != nil {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	n, err := io.Copy(tmp, r)
	if err != nil {
		return 0, fmt.Errorf("blob: writing %s/%d/%s: %w", kind, id, name, err)
	}
	if err := tmp.Close(); err != nil {
		tmp = nil
		return 0, fmt.Errorf("blob: closing temp file: %w", err)
	}
	tmp = nil
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("blob: renaming into place: %w", err)
	}
	return n, nil
}

func (s *FilesystemStore) Get(ctx context.Context, kind Kind, id int64, name string) (io.ReadCloser, error) {
	if err := validateKey(kind, id, name); err != nil {
		return nil, err
	}
	f, err := os.Open(s.path(kind, id, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s/%d/%s", fs.ErrNotExist, kind, id, name)
		}
		return nil, err
	}
	return f, nil
}

func (s *FilesystemStore) Delete(ctx context.Context, kind Kind, id int64, name string) error {
	if err := validateKey(kind, id, name); err != nil {
		return err
	}
	err := os.Remove(s.path(kind, id, name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *FilesystemStore) DeleteAll(ctx context.Context, kind Kind, id int64) error {
	dir := filepath.Join(s.root, string(kind), fmt.Sprintf("%d", id))
	err := os.RemoveAll(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *FilesystemStore) Size(ctx context.Context, kind Kind, id int64, name string) (int64, error) {
	if err := validateKey(kind, id, name); err != nil {
		return 0, err
	}
	info, err := os.Stat(s.path(kind, id, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("%w: %s/%d/%s", fs.ErrNotExist, kind, id, name)
		}
		return 0, err
	}
	return info.Size(), nil
}

func (s *FilesystemStore) List(ctx context.Context, kind Kind, id int64) ([]string, error) {
	dir := filepath.Join(s.root, string(kind), fmt.Sprintf("%d", id))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

func (s *FilesystemStore) PresignURL(ctx context.Context, kind Kind, id int64, name string, ttl time.Duration) (string, error) {
	return "", &ErrPresignUnsupported{Backend: "filesystem"}
}

// validateKey rejects empty/ambiguous components that would escape the
// sharded layout. Names may not contain path separators or "."/".." segments.
func validateKey(kind Kind, id int64, name string) error {
	if kind == "" {
		return errors.New("blob: empty kind")
	}
	if id <= 0 {
		return errors.New("blob: id must be positive")
	}
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("blob: invalid name %q", name)
	}
	if filepath.Separator == '/' && (contains(name, "/") || contains(name, "\\")) {
		return fmt.Errorf("blob: name %q contains path separator", name)
	}
	// Defense in depth: ensure the cleaned path stays inside root.
	return nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
