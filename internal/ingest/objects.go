package ingest

import (
	"bytes"
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const MaxManifestBytes = 8 << 20

var objectKey = regexp.MustCompile(`^repositories/[0-9a-f-]{36}/(blobs/[0-9a-f]{64}|manifests/[0-9a-f]{64}\.json)$`)

func keyOK(k string) bool {
	p := strings.Split(k, "/")
	return len(p) == 4 && uuidPattern.MatchString(p[1]) && objectKey.MatchString(k)
}
func blobKey(repo, digest string) string { return "repositories/" + repo + "/blobs/" + digest }
func manifestKey(repo, digest string) string {
	return "repositories/" + repo + "/manifests/" + digest + ".json"
}
func expectedDigest(k string) string { return strings.TrimSuffix(path.Base(k), ".json") }
func verifyStored(k string, b []byte, max int64) error {
	if !keyOK(k) || max < 0 || max > MaxManifestBytes || int64(len(b)) > max || Digest(b) != expectedDigest(k) {
		return ErrIntegrity
	}
	return nil
}

// LocalStore is a rooted private store. No Git path is ever used as a host path.
type LocalStore struct{ root *os.Root }

func OpenLocal(dir string) (*LocalStore, error) {
	if dir == "" {
		return nil, ErrStorage
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, ErrStorage
	}
	r, e := os.OpenRoot(dir)
	if e != nil {
		return nil, ErrStorage
	}
	return &LocalStore{r}, nil
}
func (s *LocalStore) Close() error { return s.root.Close() }
func (s *LocalStore) Get(ctx context.Context, k string, max int64) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !keyOK(k) || max < 0 || max > MaxManifestBytes {
		return nil, ErrIntegrity
	}
	f, e := s.root.Open(k)
	if e != nil {
		return nil, ErrStorage
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() || i.Size() > max {
		return nil, ErrIntegrity
	}
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil {
		return nil, ErrStorage
	}
	if e = verifyStored(k, b, max); e != nil {
		return nil, e
	}
	return b, nil
}
func (s *LocalStore) Put(ctx context.Context, k string, b []byte) error {
	if e := verifyStored(k, b, MaxManifestBytes); e != nil {
		return e
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	dir := path.Dir(k)
	if e := s.root.MkdirAll(dir, 0700); e != nil {
		return ErrStorage
	}
	tmp := dir + "/.staging-" + uuid.NewString()
	f, e := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrStorage
	}
	defer s.root.Remove(tmp)
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return ErrStorage
	}
	if e = s.root.Link(tmp, k); e != nil && !errors.Is(e, fs.ErrExist) {
		return ErrStorage
	}
	got, e := s.Get(ctx, k, int64(len(b)))
	if e != nil {
		return e
	}
	if !bytes.Equal(got, b) {
		return ErrIntegrity
	}
	return nil
}
func (s *LocalStore) Inventory(ctx context.Context, repo string, max int) ([]string, error) {
	if !uuidPattern.MatchString(repo) || max < 1 || max > 20000 {
		return nil, ErrBudget
	}
	keys := []string{}
	prefix := "repositories/" + repo
	e := boundedWalk(ctx, s.root, prefix, 8, max*8+16, func(p string, de fs.DirEntry, e error) error {
		if errors.Is(e, fs.ErrNotExist) && p == prefix {
			return nil
		}
		if e != nil {
			return ErrStorage
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !de.IsDir() {
			if len(keys) >= max {
				return ErrBudget
			}
			keys = append(keys, p)
		}
		return nil
	})
	sort.Strings(keys)
	return keys, e
}

type S3Store struct {
	client *minio.Client
	bucket string
}

func OpenS3(endpoint, bucket, access, secret string, tls bool) (*S3Store, error) {
	if endpoint == "" || bucket == "" || access == "" || secret == "" {
		return nil, ErrStorage
	}
	transport, e := minio.DefaultTransport(tls)
	if e != nil {
		return nil, ErrStorage
	}
	transport.Proxy = nil
	c, e := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Secure: tls, Transport: transport})
	if e != nil {
		return nil, ErrStorage
	}
	return &S3Store{c, bucket}, nil
}
func (s *S3Store) Initialize(ctx context.Context) error {
	exists, e := s.client.BucketExists(ctx, s.bucket)
	if e != nil {
		return ErrStorage
	}
	if !exists {
		if e = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); e != nil {
			return ErrStorage
		}
	}
	if e = s.client.SetBucketVersioning(ctx, s.bucket, minio.BucketVersioningConfiguration{Status: "Enabled"}); e != nil {
		return ErrStorage
	}
	p, e := s.client.GetBucketPolicy(ctx, s.bucket)
	if e != nil && minio.ToErrorResponse(e).Code != "NoSuchBucketPolicy" {
		return ErrStorage
	}
	if p != "" {
		return ErrIntegrity
	}
	return nil
}
func (s *S3Store) Get(ctx context.Context, k string, max int64) ([]byte, error) {
	if !keyOK(k) || max < 0 || max > MaxManifestBytes {
		return nil, ErrIntegrity
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	o, e := s.client.GetObject(ctx, s.bucket, k, minio.GetObjectOptions{})
	if e != nil {
		return nil, ErrStorage
	}
	defer o.Close()
	i, e := o.Stat()
	if e != nil {
		return nil, ErrStorage
	}
	if i.Size < 0 || i.Size > max || i.VersionID == "" {
		return nil, ErrIntegrity
	}
	b, e := io.ReadAll(io.LimitReader(o, max+1))
	if e != nil {
		return nil, ErrStorage
	}
	if int64(len(b)) != i.Size {
		return nil, ErrIntegrity
	}
	if e = verifyStored(k, b, max); e != nil {
		return nil, e
	}
	return b, nil
}
func (s *S3Store) Put(ctx context.Context, k string, b []byte) error {
	if e := verifyStored(k, b, MaxManifestBytes); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	v, e := s.client.GetBucketVersioning(ctx, s.bucket)
	if e != nil {
		return ErrStorage
	}
	if v.Status != "Enabled" {
		return ErrIntegrity
	}
	opts := minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true}
	opts.SetMatchETagExcept("*")
	_, e = s.client.PutObject(ctx, s.bucket, k, bytes.NewReader(b), int64(len(b)), opts)
	if e != nil && minio.ToErrorResponse(e).Code != "PreconditionFailed" {
		return ErrStorage
	}
	got, e := s.Get(ctx, k, int64(len(b)))
	if e != nil {
		return e
	}
	if !bytes.Equal(got, b) {
		return ErrIntegrity
	}
	return nil
}
func (s *S3Store) Inventory(ctx context.Context, repo string, max int) ([]string, error) {
	if !uuidPattern.MatchString(repo) || max < 1 || max > 20000 {
		return nil, ErrBudget
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	keys := []string{}
	for o := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: "repositories/" + repo + "/", Recursive: true}) {
		if o.Err != nil {
			return nil, ErrStorage
		}
		if len(keys) >= max {
			return nil, ErrBudget
		}
		keys = append(keys, o.Key)
	}
	sort.Strings(keys)
	return keys, nil
}
