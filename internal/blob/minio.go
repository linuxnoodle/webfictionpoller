package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIOConfig configures a MinIOStore. Endpoint may point at any S3-compatible
// service (MinIO, SeaweedFS, R2, actual S3). Bucket is created on first use
// if missing and credentials permit.
type MinIOConfig struct {
	Endpoint  string // e.g. "minio:9000" (no scheme)
	AccessKey string
	SecretKey string
	Bucket    string
	UseTLS    bool
	Region    string // optional; some S3-compatible stores require it
}

// MinIOStore implements Store over any S3-compatible API. Object keys follow
// the same {kind}/{id}/{name} layout as the filesystem backend so the two
// are interchangeable.
type MinIOStore struct {
	client *minio.Client
	bucket string
}

// NewMinIOStore dials the endpoint and ensures the bucket exists. Returns an
// error if the bucket cannot be created or bucket-exists checks fail.
func NewMinIOStore(ctx context.Context, cfg MinIOConfig) (*MinIOStore, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("blob: MinIO endpoint empty")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("blob: MinIO bucket empty")
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseTLS,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("blob: minio client: %w", err)
	}
	exists, err := cli.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("blob: minio BucketExists: %w", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("blob: minio MakeBucket %q: %w", cfg.Bucket, err)
		}
	}
	return &MinIOStore{client: cli, bucket: cfg.Bucket}, nil
}

func (s *MinIOStore) objectKey(kind Kind, id int64, name string) string {
	return fmt.Sprintf("%s/%d/%s", kind, id, name)
}

func (s *MinIOStore) Put(ctx context.Context, kind Kind, id int64, name string, r io.Reader, opts PutOptions) (int64, error) {
	if err := validateKey(kind, id, name); err != nil {
		return 0, err
	}
	// minio's PutObject wants a size hint; using -1 streams unknown-length input
	// but disables multipart for large uploads. For images and bundles the size
	// is always small enough that we read it into memory to get the length.
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("blob: minio read source: %w", err)
	}
	ct := opts.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	_, err = s.client.PutObject(ctx, s.bucket, s.objectKey(kind, id, name), bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: ct})
	if err != nil {
		return 0, fmt.Errorf("blob: minio PutObject: %w", err)
	}
	return int64(len(data)), nil
}

func (s *MinIOStore) Get(ctx context.Context, kind Kind, id int64, name string) (io.ReadCloser, error) {
	if err := validateKey(kind, id, name); err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, s.objectKey(kind, id, name), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("blob: minio GetObject: %w", err)
	}
	// Probe: stat so we surface a not-exist error here rather than on first read.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		if isMinioNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%d/%s", fs.ErrNotExist, kind, id, name)
		}
		return nil, fmt.Errorf("blob: minio Stat: %w", err)
	}
	return obj, nil
}

func (s *MinIOStore) Delete(ctx context.Context, kind Kind, id int64, name string) error {
	if err := validateKey(kind, id, name); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, s.objectKey(kind, id, name), minio.RemoveObjectOptions{}); err != nil {
		if isMinioNotFound(err) {
			return nil
		}
		return fmt.Errorf("blob: minio RemoveObject: %w", err)
	}
	return nil
}

func (s *MinIOStore) DeleteAll(ctx context.Context, kind Kind, id int64) error {
	prefix := fmt.Sprintf("%s/%d/", kind, id)
	objectsCh := make(chan minio.ObjectInfo)
	go func() {
		defer close(objectsCh)
		// ListObjects sends object metadata on objectsCh.
		for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if object.Err != nil {
				return
			}
			objectsCh <- object
		}
	}()
	for err := range s.client.RemoveObjects(ctx, s.bucket, objectsCh, minio.RemoveObjectsOptions{}) {
		if err.Err != nil && !isMinioNotFound(err.Err) {
			return fmt.Errorf("blob: minio RemoveObjects: %w", err.Err)
		}
	}
	return nil
}

func (s *MinIOStore) Size(ctx context.Context, kind Kind, id int64, name string) (int64, error) {
	if err := validateKey(kind, id, name); err != nil {
		return 0, err
	}
	info, err := s.client.StatObject(ctx, s.bucket, s.objectKey(kind, id, name), minio.StatObjectOptions{})
	if err != nil {
		if isMinioNotFound(err) {
			return 0, fmt.Errorf("%w: %s/%d/%s", fs.ErrNotExist, kind, id, name)
		}
		return 0, fmt.Errorf("blob: minio StatObject: %w", err)
	}
	return info.Size, nil
}

func (s *MinIOStore) List(ctx context.Context, kind Kind, id int64) ([]string, error) {
	prefix := fmt.Sprintf("%s/%d/", kind, id)
	var out []string
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return nil, fmt.Errorf("blob: minio ListObjects: %w", object.Err)
		}
		// Trim the prefix to get just the name component.
		name := strings.TrimPrefix(object.Key, prefix)
		if name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

func (s *MinIOStore) PresignURL(ctx context.Context, kind Kind, id int64, name string, ttl time.Duration) (string, error) {
	if err := validateKey(kind, id, name); err != nil {
		return "", err
	}
	// Set expiration via a presigned GET; backend requires UseTLS=true for most
	// real deployments because presigned URLs over plain HTTP leak the signature.
	presignedURL, err := s.client.PresignedGetObject(ctx, s.bucket, s.objectKey(kind, id, name), ttl, nil)
	if err != nil {
		return "", fmt.Errorf("blob: minio PresignedGetObject: %w", err)
	}
	return presignedURL.String(), nil
}

func isMinioNotFound(err error) bool {
	if err == nil {
		return false
	}
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.Code == "NoSuchObject"
}
