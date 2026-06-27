package objectstorage

import (
	"context"
	"io"
	"time"

	"github.com/primandproper/platform-go/circuitbreaking"
	"github.com/primandproper/platform-go/observability/keys"
)

// SaveFile saves a file to the blob.
func (u *Uploader) SaveFile(ctx context.Context, path string, content []byte) error {
	ctx, op := u.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, path)

	if u.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	if err := u.bucket.WriteAll(ctx, path, content, nil); err != nil {
		u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
		u.saveErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return op.Error(err, "writing file content")
	}

	u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	u.saveCounter.Add(ctx, 1)
	u.circuitBreaker.Succeeded()
	return nil
}

// ReadFile reads a file from the blob.
func (u *Uploader) ReadFile(ctx context.Context, path string) ([]byte, error) {
	ctx, op := u.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, path)

	if u.circuitBreaker.CannotProceed() {
		return nil, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()

	r, err := u.bucket.NewReader(ctx, path, nil)
	if err != nil {
		u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
		u.readErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return nil, op.Error(err, "fetching file")
	}

	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "error closing file reader")
		}
	}()

	fileBytes, err := io.ReadAll(r)
	if err != nil {
		u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
		u.readErrCounter.Add(ctx, 1)
		u.circuitBreaker.Failed()
		return nil, op.Error(err, "reading file")
	}

	u.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	u.readCounter.Add(ctx, 1)
	u.circuitBreaker.Succeeded()
	return fileBytes, nil
}
