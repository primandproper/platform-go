// Package noop is the uploads.UploadManager that stores nothing, and it is the
// only implementation here that can still return an error.
//
// Save reads r to completion and discards it, reporting whatever the reader
// reports. Draining is deliberate: an unread request body is a connection that
// cannot be reused, and a caller who wants to know their upload stream was
// intact still finds out. Everything downstream of that is empty rather than
// absent — Open and OpenRange return readers over zero bytes, Attributes
// returns a zero-valued struct, List yields no objects, and SignedURL returns
// an empty string, which is not a URL a browser will resolve to anything useful.
//
// Exists is the honest one and reports false, so a caller that checks before
// reading learns the truth, while a caller that reads without checking gets a
// successful empty file. The type also satisfies uploads.RangeReader, URLSigner,
// Attributer, and Lister, so a capability type switch finds them all present and
// all inert.
//
// uploads/config selects among the objectstorage providers and never this one;
// a caller who wants it names it in code.
package noop

import (
	"context"
	"io"
	"iter"
	"strings"

	"github.com/primandproper/platform-go/v13/uploads"
)

var (
	_ uploads.UploadManager = (*UploadManager)(nil)
	_ uploads.RangeReader   = (*UploadManager)(nil)
	_ uploads.URLSigner     = (*UploadManager)(nil)
	_ uploads.Attributer    = (*UploadManager)(nil)
	_ uploads.Lister        = (*UploadManager)(nil)
)

// UploadManager is a no-op UploadManager.
var _ uploads.UploadManager = (*UploadManager)(nil)

type UploadManager struct{}

// NewUploadManager returns a no-op UploadManager.
func NewUploadManager() *UploadManager {
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

// Close satisfies the interface and releases nothing.
func (u *UploadManager) Close() error {
	return nil
}
