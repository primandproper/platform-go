package objectstorage

import (
	"context"
	"errors"
	"io"
	"iter"
	"time"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/uploads"

	"gocloud.dev/blob"
)

var (
	_ uploads.UploadManager = (*Uploader)(nil)
	_ uploads.RangeReader   = (*Uploader)(nil)
	_ uploads.URLSigner     = (*Uploader)(nil)
	_ uploads.Attributer    = (*Uploader)(nil)
	_ uploads.Lister        = (*Uploader)(nil)
)

// rejected reports an operation the circuit breaker refused to attempt: it
// counts the rejection and puts it on the span, then returns the sentinel
// wrapped with what was being attempted.
//
// All eight methods used to return a bare circuitbreaking.ErrCircuitBroken here,
// which left the span green and no counter moved. errors.Is still matches the
// sentinel through the wrap, so callers branching on it are unaffected.
func (u *Uploader) rejected(ctx context.Context, op observability.Operation, operation string) error {
	u.instruments.rejected(ctx, operation)

	return op.Error(circuitbreaking.ErrCircuitBroken, "%s rejected by open circuit breaker", operation)
}

// Save writes the contents of r to the object at path.
func (u *Uploader) Save(ctx context.Context, path string, r io.Reader, opts ...uploads.SaveOption) error {
	ctx, op := u.o11y.Begin(ctx, observability.WithValue(keys.FilenameKey, path))
	defer op.End()

	if u.circuitBreaker.CannotProceed() {
		return u.rejected(ctx, op, opSave)
	}

	so := uploads.BuildSaveOptions(opts...)

	startTime := time.Now()

	// gocloud commits the write when Close returns without error, so a mid-copy failure must
	// abort the write by cancelling its context before Close; otherwise a truncated object is
	// committed at path even though Save reports an error.
	writeCtx, cancelWrite := context.WithCancel(ctx)
	defer cancelWrite()

	writer, err := u.bucket.NewWriter(writeCtx, path, &blob.WriterOptions{
		ContentType:  so.ContentType,
		CacheControl: so.CacheControl,
	})
	if err != nil {
		u.instruments.failed(ctx, opSave, startTime)
		u.circuitBreaker.Failed()
		return op.Error(err, "creating object writer")
	}

	written, copyErr := io.Copy(writer, r)
	if copyErr != nil {
		cancelWrite()
	}
	if err = errors.Join(copyErr, writer.Close()); err != nil {
		u.instruments.failed(ctx, opSave, startTime)
		u.circuitBreaker.Failed()
		return op.Error(err, "writing object content")
	}

	op.Set(keys.LengthKey, written)

	u.instruments.succeeded(ctx, opSave, startTime)
	u.circuitBreaker.Succeeded()
	return nil
}

// Open returns a reader for the object at path. The caller is responsible for closing it.
func (u *Uploader) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return u.openRange(ctx, path, 0, -1, opOpen, "opening object reader")
}

// OpenRange returns a reader over length bytes of the object at path, starting at offset. A
// negative length reads to the end. The caller is responsible for closing it.
func (u *Uploader) OpenRange(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error) {
	return u.openRange(ctx, path, offset, length, opOpenRange, "opening ranged object reader")
}

func (u *Uploader) openRange(ctx context.Context, path string, offset, length int64, operation, failureDesc string) (io.ReadCloser, error) {
	ctx, op := u.o11y.Begin(ctx, observability.WithValue(keys.FilenameKey, path))
	defer op.End()

	if u.circuitBreaker.CannotProceed() {
		return nil, u.rejected(ctx, op, operation)
	}

	startTime := time.Now()

	reader, err := u.bucket.NewRangeReader(ctx, path, offset, length, nil)
	if err != nil {
		u.instruments.failed(ctx, operation, startTime)
		u.circuitBreaker.Failed()
		return nil, op.Error(err, "%s", failureDesc)
	}

	op.Set(keys.LengthKey, reader.Size())

	u.instruments.succeeded(ctx, operation, startTime)
	u.circuitBreaker.Succeeded()
	return reader, nil
}

// Delete removes the object at path.
func (u *Uploader) Delete(ctx context.Context, path string) error {
	ctx, op := u.o11y.Begin(ctx, observability.WithValue(keys.FilenameKey, path))
	defer op.End()

	if u.circuitBreaker.CannotProceed() {
		return u.rejected(ctx, op, opDelete)
	}

	startTime := time.Now()

	err := u.bucket.Delete(ctx, path)
	if err != nil {
		u.instruments.failed(ctx, opDelete, startTime)
		u.circuitBreaker.Failed()
		return op.Error(err, "deleting object")
	}

	u.instruments.succeeded(ctx, opDelete, startTime)
	u.circuitBreaker.Succeeded()
	return nil
}

// Exists reports whether an object exists at path.
func (u *Uploader) Exists(ctx context.Context, path string) (bool, error) {
	ctx, op := u.o11y.Begin(ctx, observability.WithValue(keys.FilenameKey, path))
	defer op.End()

	if u.circuitBreaker.CannotProceed() {
		return false, u.rejected(ctx, op, opExists)
	}

	startTime := time.Now()

	exists, err := u.bucket.Exists(ctx, path)
	if err != nil {
		u.instruments.failed(ctx, opExists, startTime)
		u.circuitBreaker.Failed()
		return false, op.Error(err, "checking object existence")
	}

	u.instruments.succeeded(ctx, opExists, startTime)
	u.circuitBreaker.Succeeded()
	return exists, nil
}

// Attributes fetches the stored metadata for the object at path.
func (u *Uploader) Attributes(ctx context.Context, path string) (*uploads.Attributes, error) {
	ctx, op := u.o11y.Begin(ctx, observability.WithValue(keys.FilenameKey, path))
	defer op.End()

	if u.circuitBreaker.CannotProceed() {
		return nil, u.rejected(ctx, op, opAttributes)
	}

	startTime := time.Now()

	attrs, err := u.bucket.Attributes(ctx, path)
	if err != nil {
		u.instruments.failed(ctx, opAttributes, startTime)
		u.circuitBreaker.Failed()
		return nil, op.Error(err, "fetching object attributes")
	}

	u.instruments.succeeded(ctx, opAttributes, startTime)
	u.circuitBreaker.Succeeded()
	return &uploads.Attributes{
		ContentType:  attrs.ContentType,
		CacheControl: attrs.CacheControl,
		ETag:         attrs.ETag,
		ModTime:      attrs.ModTime,
		Size:         attrs.Size,
	}, nil
}

// List streams the objects stored under prefix. Objects are fetched lazily as the returned
// iterator is consumed; the caller may stop early by breaking out of the range loop.
func (u *Uploader) List(ctx context.Context, prefix string) iter.Seq2[uploads.ObjectInfo, error] {
	return func(yield func(uploads.ObjectInfo, error) bool) {
		spanCtx, op := u.o11y.Begin(ctx, observability.WithValue("prefix", prefix))
		defer op.End()

		if u.circuitBreaker.CannotProceed() {
			yield(uploads.ObjectInfo{}, u.rejected(spanCtx, op, opList))
			return
		}

		startTime := time.Now()

		it := u.bucket.List(&blob.ListOptions{Prefix: prefix})

		count := 0
		for {
			obj, err := it.Next(spanCtx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				u.instruments.failed(spanCtx, opList, startTime)
				u.circuitBreaker.Failed()
				yield(uploads.ObjectInfo{}, op.Error(err, "listing objects"))
				return
			}

			count++
			if !yield(uploads.ObjectInfo{
				Path:    obj.Key,
				ModTime: obj.ModTime,
				Size:    obj.Size,
				IsDir:   obj.IsDir,
			}, nil) {
				break
			}
		}

		op.Set("object.count", count)

		u.instruments.succeeded(spanCtx, opList, startTime)
		u.circuitBreaker.Succeeded()
	}
}

// SignedURL mints a signed URL granting temporary, direct access to the object at path. Not all
// providers support signing (e.g. the in-memory and unsigned filesystem backends return an error).
func (u *Uploader) SignedURL(ctx context.Context, path string, opts *uploads.SignedURLOptions) (string, error) {
	ctx, op := u.o11y.Begin(ctx, observability.WithValue(keys.FilenameKey, path))
	defer op.End()

	if u.circuitBreaker.CannotProceed() {
		return "", u.rejected(ctx, op, opSignedURL)
	}

	signOpts := &blob.SignedURLOptions{}
	if opts != nil {
		signOpts.Expiry = opts.Expiry
		signOpts.Method = opts.Method
		signOpts.ContentType = opts.ContentType
	}

	startTime := time.Now()

	signedURL, err := u.bucket.SignedURL(ctx, path, signOpts)
	if err != nil {
		u.instruments.failed(ctx, opSignedURL, startTime)
		u.circuitBreaker.Failed()
		return "", op.Error(err, "signing object URL")
	}

	u.instruments.succeeded(ctx, opSignedURL, startTime)
	u.circuitBreaker.Succeeded()
	return signedURL, nil
}

// Close releases the underlying gocloud bucket, and with it whatever client
// the provider opened. It does not delete anything.
//
// It is safe to call more than once; gocloud's Close reports an error only on
// the first call that actually closes.
func (u *Uploader) Close() error {
	if u.bucket == nil {
		return nil
	}

	return u.bucket.Close()
}
