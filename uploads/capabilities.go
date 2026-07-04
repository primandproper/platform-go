package uploads

import (
	"context"
	"io"
	"iter"
	"time"
)

// The interfaces below are optional capabilities. The core UploadManager only guarantees
// Save/Open/Delete/Exists; richer backends (e.g. objectstorage.Uploader) also implement these.
// Callers that need them either accept the specific interface or type-assert an UploadManager.
type (
	// RangeReader can open a byte range of an object, for partial reads such as HTTP Range
	// requests (video) or seeking within columnar files (parquet).
	RangeReader interface {
		// OpenRange returns a reader over length bytes of the object at path, starting at offset.
		// A negative length reads to the end of the object. The caller must close the reader.
		OpenRange(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error)
	}

	// URLSigner can mint a signed URL granting temporary, direct access to an object, letting
	// clients read or write storage without proxying bytes through the service.
	URLSigner interface {
		SignedURL(ctx context.Context, path string, opts *SignedURLOptions) (string, error)
	}

	// Attributer can fetch an object's stored metadata.
	Attributer interface {
		Attributes(ctx context.Context, path string) (*Attributes, error)
	}

	// Lister can stream the objects stored under a prefix. The returned iterator yields each
	// object lazily; a non-nil error is yielded once and terminates iteration, and the caller may
	// stop early by breaking out of the range loop.
	Lister interface {
		List(ctx context.Context, prefix string) iter.Seq2[ObjectInfo, error]
	}
)

// ListAll drains a Lister into a slice. It is a convenience for small listings; prefer ranging
// Lister.List directly when a prefix may contain a very large number of objects.
func ListAll(ctx context.Context, l Lister, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	for obj, err := range l.List(ctx, prefix) {
		if err != nil {
			return nil, err
		}

		out = append(out, obj)
	}

	return out, nil
}

type (
	// SignedURLOptions configures a signed URL.
	SignedURLOptions struct {
		_ struct{} `json:"-"`

		// Method is the HTTP method the URL permits: "GET", "PUT", or "DELETE". Empty means "GET".
		Method string
		// ContentType, for PUT URLs, is the exact Content-Type the client must send.
		ContentType string
		// Expiry sets how long the URL is valid. Zero means the provider default.
		Expiry time.Duration
	}

	// Attributes describes a stored object.
	Attributes struct {
		_            struct{} `json:"-"`
		ModTime      time.Time
		ContentType  string
		CacheControl string
		ETag         string
		Size         int64
	}

	// ObjectInfo describes a single entry returned by Lister.List.
	ObjectInfo struct {
		_       struct{} `json:"-"`
		ModTime time.Time
		Path    string
		Size    int64
		IsDir   bool
	}
)
