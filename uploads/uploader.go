package uploads

import (
	"bytes"
	"context"
	"errors"
	"io"
)

// UploadManager reads and writes objects in a storage provider.
type UploadManager interface {
	// Save writes the contents of r to the object at path.
	Save(ctx context.Context, path string, r io.Reader, opts ...SaveOption) error
	// Open returns a reader for the object at path. The caller must close it.
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// Delete removes the object at path.
	Delete(ctx context.Context, path string) error
	// Exists reports whether an object exists at path.
	Exists(ctx context.Context, path string) (bool, error)
}

type (
	// SaveOption customizes a Save call.
	SaveOption func(*SaveOptions)

	// SaveOptions are the resolved settings for a Save call. Implementations obtain one via
	// BuildSaveOptions.
	SaveOptions struct {
		_ struct{} `json:"-"`

		// ContentType sets the stored Content-Type. When empty, the provider sniffs it from the
		// content on write.
		ContentType string
		// CacheControl sets the stored Cache-Control header for served objects.
		CacheControl string
	}
)

// WithContentType sets the stored Content-Type for a saved object.
func WithContentType(contentType string) SaveOption {
	return func(o *SaveOptions) { o.ContentType = contentType }
}

// WithCacheControl sets the stored Cache-Control header for a saved object.
func WithCacheControl(cacheControl string) SaveOption {
	return func(o *SaveOptions) { o.CacheControl = cacheControl }
}

// BuildSaveOptions resolves SaveOptions from a list of options. UploadManager implementations call
// this to read the requested settings.
func BuildSaveOptions(opts ...SaveOption) SaveOptions {
	var o SaveOptions
	for _, fn := range opts {
		fn(&o)
	}

	return o
}

// SaveFile is a convenience helper that saves a byte slice via UploadManager.Save.
func SaveFile(ctx context.Context, m UploadManager, path string, content []byte, opts ...SaveOption) error {
	return m.Save(ctx, path, bytes.NewReader(content), opts...)
}

// ReadFile is a convenience helper that reads an entire object into memory via UploadManager.Open.
func ReadFile(ctx context.Context, m UploadManager, path string) (b []byte, err error) {
	r, err := m.Open(ctx, path)
	if err != nil {
		return nil, err
	}

	defer func() {
		err = errors.Join(err, r.Close())
	}()

	b, err = io.ReadAll(r)

	return b, err
}
