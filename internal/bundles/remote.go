package bundles

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Amankumarsingh23/JudgeGrid/internal/objects"
	"io"
	"os"
	"time"
)

type Publisher interface {
	Store
	Put(Bundle) (Reference, error)
}
type Remote struct{ Objects objects.Store }

func (r *Remote) Put(b Bundle) (Reference, error) {
	ref, e := b.Reference()
	if e != nil {
		return ref, e
	}
	data, _ := json.Marshal(b)
	if len(data) > MaxBytes {
		return ref, ErrIntegrity
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if e = r.Objects.Put(ctx, "bundles/"+ref.Hash+".json", data); e != nil {
		return ref, e
	}
	got, e := r.Load(ref.Hash)
	if e != nil {
		return ref, e
	}
	return ref, got.Verify(ref)
}
func (r *Remote) Load(hash string) (Bundle, error) {
	var b Bundle
	if !hashPattern.MatchString(hash) {
		return b, ErrIntegrity
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, e := r.Objects.Get(ctx, "bundles/"+hash+".json", MaxBytes)
	if e != nil {
		return b, e
	}
	if Hash(data) != hash {
		return b, ErrIntegrity
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&b) != nil {
		return b, ErrIntegrity
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return b, ErrIntegrity
	}
	ref, e := b.Reference()
	if e != nil || ref.Hash != hash {
		return b, ErrIntegrity
	}
	return b, nil
}

// Configured requires a named backend. S3 failure never opens a local store.
func Configured(local string) (Publisher, error) {
	switch os.Getenv("BUNDLE_BACKEND") {
	case "", "local":
		return Open(local)
	case "s3":
		s, e := objects.New(os.Getenv("S3_ENDPOINT"), os.Getenv("S3_BUNDLE_BUCKET"), os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), os.Getenv("S3_TLS") != "false")
		if e != nil {
			return nil, e
		}
		return &Remote{s}, nil
	default:
		return nil, ErrIntegrity
	}
}
