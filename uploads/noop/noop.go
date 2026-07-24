package noop

import (
	"context"
	"io"
	"iter"
	"strings"

	"github.com/primandproper/platform-go/v6/uploads"
)

var (
	_ uploads.UploadManager = (*UploadManager)(nil)
	_ uploads.RangeReader   = (*UploadManager)(nil)
	_ uploads.URLSigner     = (*UploadManager)(nil)
	_ uploads.Attributer    = (*UploadManager)(nil)
	_ uploads.Lister        = (*UploadManager)(nil)
)

// UploadManager is a no-op UploadManager.
type UploadManager struct{}

// NewUploadManager returns a no-op UploadManager.
func NewUploadManager() uploads.UploadManager {
	return &UploadManager{}
}

// Save is a no-op that drains r.
func (*UploadManager) Save(_ context.Context, _ string, r io.Reader, _ ...uploads.SaveOption) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// Open is a no-op that returns an empty reader.
func (*UploadManager) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// OpenRange is a no-op that returns an empty reader.
func (*UploadManager) OpenRange(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// Delete is a no-op.
func (*UploadManager) Delete(context.Context, string) error {
	return nil
}

// Exists is a no-op that always reports false.
func (*UploadManager) Exists(context.Context, string) (bool, error) {
	return false, nil
}

// Attributes is a no-op that returns empty attributes.
func (*UploadManager) Attributes(context.Context, string) (*uploads.Attributes, error) {
	return &uploads.Attributes{}, nil
}

// List is a no-op that yields no objects.
func (*UploadManager) List(context.Context, string) iter.Seq2[uploads.ObjectInfo, error] {
	return func(func(uploads.ObjectInfo, error) bool) {}
}

// SignedURL is a no-op that returns an empty URL.
func (*UploadManager) SignedURL(context.Context, string, *uploads.SignedURLOptions) (string, error) {
	return "", nil
}
