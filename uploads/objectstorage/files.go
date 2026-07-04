package objectstorage

import (
	"context"
	"errors"
	"io"
	"iter"
	"time"

	"github.com/primandproper/platform-go/v2/circuitbreaking"
	"github.com/primandproper/platform-go/v2/observability/keys"
	"github.com/primandproper/platform-go/v2/uploads"

	"gocloud.dev/blob"
)

var (
	_ uploads.UploadManager = (*Uploader)(nil)
	_ uploads.RangeReader   = (*Uploader)(nil)
	_ uploads.URLSigner     = (*Uploader)(nil)
	_ uploads.Attributer    = (*Uploader)(nil)
	_ uploads.Lister        = (*Uploader)(nil)
)

// Save writes the contents of r to the object at path.
func (u *Uploader) Save(ctx context.Context, path string, r io.Reader, opts ...uploads.SaveOption) error {
	ctx, op := u.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, path)

	if u.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	so := uploads.BuildSaveOptions(opts...)

	startTime := time.Now()

	writer, err := u.bucket.NewWriter(ctx, path, &blob.WriterOptions{
		ContentType:  so.ContentType,
		CacheControl: so.CacheControl,
	})
	if err != nil {
		u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
		u.saveErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return op.Error(err, "creating object writer")
	}

	written, copyErr := io.Copy(writer, r)
	if err = errors.Join(copyErr, writer.Close()); err != nil {
		u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
		u.saveErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return op.Error(err, "writing object content")
	}

	op.Set(keys.LengthKey, written)

	u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	u.saveCounter.Add(ctx, 1)
	u.circuitBreaker.Succeeded()
	return nil
}

// Open returns a reader for the object at path. The caller is responsible for closing it.
func (u *Uploader) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return u.openRange(ctx, path, 0, -1, "opening object reader")
}

// OpenRange returns a reader over length bytes of the object at path, starting at offset. A
// negative length reads to the end. The caller is responsible for closing it.
func (u *Uploader) OpenRange(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error) {
	return u.openRange(ctx, path, offset, length, "opening ranged object reader")
}

func (u *Uploader) openRange(ctx context.Context, path string, offset, length int64, failureDesc string) (io.ReadCloser, error) {
	ctx, op := u.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, path)

	if u.circuitBreaker.CannotProceed() {
		return nil, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()

	reader, err := u.bucket.NewRangeReader(ctx, path, offset, length, nil)
	u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		u.readErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return nil, op.Error(err, "%s", failureDesc)
	}

	op.Set(keys.LengthKey, reader.Size())

	u.readCounter.Add(ctx, 1)
	u.circuitBreaker.Succeeded()
	return reader, nil
}

// Delete removes the object at path.
func (u *Uploader) Delete(ctx context.Context, path string) error {
	ctx, op := u.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, path)

	if u.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()

	err := u.bucket.Delete(ctx, path)
	u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		u.saveErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return op.Error(err, "deleting object")
	}

	u.saveCounter.Add(ctx, 1)
	u.circuitBreaker.Succeeded()
	return nil
}

// Exists reports whether an object exists at path.
func (u *Uploader) Exists(ctx context.Context, path string) (bool, error) {
	ctx, op := u.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, path)

	if u.circuitBreaker.CannotProceed() {
		return false, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()

	exists, err := u.bucket.Exists(ctx, path)
	u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		u.readErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return false, op.Error(err, "checking object existence")
	}

	u.readCounter.Add(ctx, 1)
	u.circuitBreaker.Succeeded()
	return exists, nil
}

// Attributes fetches the stored metadata for the object at path.
func (u *Uploader) Attributes(ctx context.Context, path string) (*uploads.Attributes, error) {
	ctx, op := u.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, path)

	if u.circuitBreaker.CannotProceed() {
		return nil, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()

	attrs, err := u.bucket.Attributes(ctx, path)
	u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		u.readErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return nil, op.Error(err, "fetching object attributes")
	}

	u.readCounter.Add(ctx, 1)
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
		spanCtx, op := u.o11y.Begin(ctx)
		defer op.End()

		op.Set("prefix", prefix)

		if u.circuitBreaker.CannotProceed() {
			yield(uploads.ObjectInfo{}, circuitbreaking.ErrCircuitBroken)
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
				u.latencyHist.Record(spanCtx, float64(time.Since(startTime).Milliseconds()))
				u.readErrCounter.Add(spanCtx, 1)
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

		u.latencyHist.Record(spanCtx, float64(time.Since(startTime).Milliseconds()))
		u.readCounter.Add(spanCtx, 1)
		u.circuitBreaker.Succeeded()
	}
}

// SignedURL mints a signed URL granting temporary, direct access to the object at path. Not all
// providers support signing (e.g. the in-memory and unsigned filesystem backends return an error).
func (u *Uploader) SignedURL(ctx context.Context, path string, opts *uploads.SignedURLOptions) (string, error) {
	ctx, op := u.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, path)

	if u.circuitBreaker.CannotProceed() {
		return "", circuitbreaking.ErrCircuitBroken
	}

	signOpts := &blob.SignedURLOptions{}
	if opts != nil {
		signOpts.Expiry = opts.Expiry
		signOpts.Method = opts.Method
		signOpts.ContentType = opts.ContentType
	}

	startTime := time.Now()

	signedURL, err := u.bucket.SignedURL(ctx, path, signOpts)
	u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		u.readErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return "", op.Error(err, "signing object URL")
	}

	u.readCounter.Add(ctx, 1)
	u.circuitBreaker.Succeeded()
	return signedURL, nil
}
