package blob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config selects a backend. Empty Backend defaults to BackendFS.
type Config struct {
	Backend string // "fs" or "minio"

	// FS only:
	Root string

	// MinIO only:
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseTLS    bool
	Region    string
}

// BackendFS is the Config.Backend value for the filesystem store.
const (
	BackendFS    = "fs"
	BackendMinIO = "minio"
)

// FromConfig constructs the configured Store.
func FromConfig(ctx context.Context, cfg Config) (Store, error) {
	backend := strings.ToLower(strings.TrimSpace(cfg.Backend))
	if backend == "" {
		backend = BackendFS
	}
	switch backend {
	case BackendFS:
		return NewFilesystemStore(cfg.Root)
	case BackendMinIO:
		return NewMinIOStore(ctx, MinIOConfig{
			Endpoint:  cfg.Endpoint,
			AccessKey: cfg.AccessKey,
			SecretKey: cfg.SecretKey,
			Bucket:    cfg.Bucket,
			UseTLS:    cfg.UseTLS,
			Region:    cfg.Region,
		})
	default:
		return nil, fmt.Errorf("blob: unknown backend %q (want %q or %q)", backend, BackendFS, BackendMinIO)
	}
}

// FromEnv reads Config from environment variables. Conventional names:
//
//	STORAGE_BACKEND       = fs|minio               (default fs)
//	STORAGE_FS_ROOT       = /path/to/blobs         (default "data/blobs")
//	MINIO_ENDPOINT        = minio:9000
//	MINIO_ACCESS_KEY      = ...
//	MINIO_SECRET_KEY      = ...
//	MINIO_BUCKET          = webfictionpoller       (default)
//	MINIO_USE_TLS         = true|false             (default false)
//	MINIO_REGION          = us-east-1              (optional)
func FromEnv() Config {
	cfg := Config{
		Backend:   getenvDefault("STORAGE_BACKEND", BackendFS),
		Root:      getenvDefault("STORAGE_FS_ROOT", "data/blobs"),
		Endpoint:  os.Getenv("MINIO_ENDPOINT"),
		AccessKey: os.Getenv("MINIO_ACCESS_KEY"),
		SecretKey: os.Getenv("MINIO_SECRET_KEY"),
		Bucket:    getenvDefault("MINIO_BUCKET", "webfictionpoller"),
		UseTLS:    strings.EqualFold(os.Getenv("MINIO_USE_TLS"), "true"),
		Region:    os.Getenv("MINIO_REGION"),
	}
	return cfg
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ErrNotConfigured is returned when a backend is selected but its required
// configuration is missing. Used by callers to fall back gracefully.
var ErrNotConfigured = errors.New("blob: backend not configured")
