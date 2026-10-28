// Package objects is a trusted, bounded immutable byte-store adapter. It never enters workloads.
package objects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"io"
	"regexp"
	"time"
)

const MaxBytes = 16 << 20

var ErrIntegrity = errors.New("immutable object integrity failure")
var validKey = regexp.MustCompile(`^(bundles/[0-9a-f]{64}\.json|artifacts/[0-9a-f]{64}\.bin)$`)

type Store interface {
	Get(context.Context, string, int64) ([]byte, error)
	Put(context.Context, string, []byte) error
}
type S3 struct {
	client *minio.Client
	bucket string
}

// Inventory is a bounded operator-only view of latest keys. It never deletes objects or versions.
func (s *S3) Inventory(ctx context.Context, max int) ([]string, error) {
	if max < 1 || max > 128 {
		return nil, ErrIntegrity
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	keys := []string{}
	for item := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if item.Err != nil {
			return nil, item.Err
		}
		if len(keys) == max {
			return nil, errors.New("object inventory exceeds explicit limit")
		}
		keys = append(keys, item.Key)
	}
	return keys, nil
}

func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func New(endpoint, bucket, access, secret string, secure bool) (*S3, error) {
	if endpoint == "" || bucket == "" || access == "" || secret == "" {
		return nil, ErrIntegrity
	}
	c, e := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Secure: secure})
	if e != nil {
		return nil, e
	}
	return &S3{c, bucket}, nil
}

// Initialize is privileged operator setup, never invoked as fallback by readers.
func (s *S3) Initialize(ctx context.Context) error {
	exists, e := s.client.BucketExists(ctx, s.bucket)
	if e != nil {
		return e
	}
	if !exists {
		if e = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); e != nil {
			return e
		}
	}
	if e = s.client.SetBucketVersioning(ctx, s.bucket, minio.BucketVersioningConfiguration{Status: "Enabled"}); e != nil {
		return e
	}
	policy, e := s.client.GetBucketPolicy(ctx, s.bucket)
	if e != nil && minio.ToErrorResponse(e).Code != "NoSuchBucketPolicy" {
		return e
	}
	if policy != "" {
		return errors.New("private bucket must have no anonymous policy")
	}
	return nil
}
func (s *S3) Get(ctx context.Context, key string, max int64) ([]byte, error) {
	if !validKey.MatchString(key) || max < 1 || max > MaxBytes {
		return nil, ErrIntegrity
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	o, e := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if e != nil {
		return nil, e
	}
	defer o.Close()
	info, e := o.Stat()
	if e != nil {
		return nil, e
	}
	if info.Size < 1 || info.Size > max || info.VersionID == "" {
		return nil, ErrIntegrity
	}
	b, e := io.ReadAll(io.LimitReader(o, max+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) != info.Size {
		return nil, ErrIntegrity
	}
	return b, nil
}
func (s *S3) Put(ctx context.Context, key string, b []byte) error {
	if !validKey.MatchString(key) || len(b) == 0 || len(b) > MaxBytes {
		return ErrIntegrity
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	v, e := s.client.GetBucketVersioning(ctx, s.bucket)
	if e != nil {
		return e
	}
	if v.Status != "Enabled" {
		return ErrIntegrity
	}
	opts := minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true}
	opts.SetMatchETagExcept("*")
	_, e = s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(b), int64(len(b)), opts)
	if e != nil && minio.ToErrorResponse(e).Code != "PreconditionFailed" {
		return e
	}
	got, e := s.Get(ctx, key, int64(len(b)))
	if e != nil {
		return e
	}
	if !bytes.Equal(got, b) {
		return ErrIntegrity
	}
	return nil
}
