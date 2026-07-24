package objectstorage

import (
	"bytes"
	"io"
	"testing"

	"github.com/primandproper/platform-go/v6/circuitbreaking"
	cbmock "github.com/primandproper/platform-go/v6/circuitbreaking/mock"
	"github.com/primandproper/platform-go/v6/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v6/observability/metrics/noop"
	"github.com/primandproper/platform-go/v6/uploads"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"gocloud.dev/blob"
	"gocloud.dev/blob/memblob"
)

type testUploaderMetrics struct {
	saveCounter      metrics.Int64Counter
	readCounter      metrics.Int64Counter
	deleteCounter    metrics.Int64Counter
	saveErrCounter   metrics.Int64Counter
	readErrCounter   metrics.Int64Counter
	deleteErrCounter metrics.Int64Counter
	latencyHist      metrics.Float64Histogram
}

func noopUploaderMetrics(t *testing.T) testUploaderMetrics {
	t.Helper()
	mp := metricsnoop.NewMetricsProvider()

	saveCounter, err := mp.NewInt64Counter("test_saves")
	must.NoError(t, err)

	readCounter, err := mp.NewInt64Counter("test_reads")
	must.NoError(t, err)

	deleteCounter, err := mp.NewInt64Counter("test_deletes")
	must.NoError(t, err)

	saveErrCounter, err := mp.NewInt64Counter("test_save_errors")
	must.NoError(t, err)

	readErrCounter, err := mp.NewInt64Counter("test_read_errors")
	must.NoError(t, err)

	deleteErrCounter, err := mp.NewInt64Counter("test_delete_errors")
	must.NoError(t, err)

	latencyHist, err := mp.NewFloat64Histogram("test_latency")
	must.NoError(t, err)

	return testUploaderMetrics{
		saveCounter:      saveCounter,
		readCounter:      readCounter,
		deleteCounter:    deleteCounter,
		saveErrCounter:   saveErrCounter,
		readErrCounter:   readErrCounter,
		deleteErrCounter: deleteErrCounter,
		latencyHist:      latencyHist,
	}
}

// newTestUploader builds an Uploader over the given bucket and observer with no-op metrics.
func newTestUploader(t *testing.T, b *blob.Bucket, obs observability.Observer, cb circuitbreaking.CircuitBreaker) *Uploader {
	t.Helper()

	m := noopUploaderMetrics(t)

	return &Uploader{
		bucket:           b,
		o11y:             obs,
		circuitBreaker:   cb,
		saveCounter:      m.saveCounter,
		readCounter:      m.readCounter,
		deleteCounter:    m.deleteCounter,
		saveErrCounter:   m.saveErrCounter,
		readErrCounter:   m.readErrCounter,
		deleteErrCounter: m.deleteErrCounter,
		latencyHist:      m.latencyHist,
	}
}

func readAll(t *testing.T, r io.ReadCloser) []byte {
	t.Helper()

	b, err := io.ReadAll(r)
	must.NoError(t, err)
	must.NoError(t, r.Close())

	return b
}

func TestUploader_Open(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleFilename := "hello_world.txt"
		expectedContent := []byte(t.Name())

		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.WriteAll(ctx, exampleFilename, expectedContent, nil))

		obs := observability.NewRecordingObserver()
		u := newTestUploader(t, b, obs, noop.NewCircuitBreaker())

		r, err := u.Open(ctx, exampleFilename)
		test.NoError(t, err)
		must.NotNil(t, r)
		test.Eq(t, expectedContent, readAll(t, r))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.FilenameKey: exampleFilename,
		})
	})

	T.Run("with invalid file", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleFilename := "hello_world.txt"

		obs := observability.NewRecordingObserver()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), obs, noop.NewCircuitBreaker())

		r, err := u.Open(ctx, exampleFilename)
		test.Error(t, err)
		test.Nil(t, r)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.FilenameKey: exampleFilename,
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), cb)

		r, err := u.Open(ctx, "anything.txt")
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.Nil(t, r)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with mock circuit breaker on successful read", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleFilename := "hello_world.txt"
		expectedContent := []byte(t.Name())

		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.WriteAll(ctx, exampleFilename, expectedContent, nil))

		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		u := newTestUploader(t, b, observability.NewObserverForTest(t.Name()), cb)

		r, err := u.Open(ctx, exampleFilename)
		test.NoError(t, err)
		test.Eq(t, expectedContent, readAll(t, r))
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})
}

func TestUploader_Save(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		obs := observability.NewRecordingObserver()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), obs, noop.NewCircuitBreaker())

		test.NoError(t, u.Save(ctx, "test_file.txt", bytes.NewReader([]byte(t.Name()))))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.FilenameKey: "test_file.txt",
		})
	})

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), cb)

		test.ErrorIs(t, u.Save(ctx, "test_file.txt", bytes.NewReader([]byte(t.Name()))), circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with write error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.Close())

		obs := observability.NewRecordingObserver()
		u := newTestUploader(t, b, obs, cb)

		test.Error(t, u.Save(ctx, "test_file.txt", bytes.NewReader([]byte(t.Name()))))
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.FilenameKey: "test_file.txt",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("can be read back after save", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		content := []byte("hello world")

		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		must.NoError(t, u.Save(ctx, "roundtrip.txt", bytes.NewReader(content)))

		r, err := u.Open(ctx, "roundtrip.txt")
		test.NoError(t, err)
		test.Eq(t, content, readAll(t, r))
	})
}

func TestUploader_Delete(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		obs := observability.NewRecordingObserver()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), obs, noop.NewCircuitBreaker())

		must.NoError(t, u.Save(ctx, "doomed.txt", bytes.NewReader([]byte(t.Name()))))
		test.NoError(t, u.Delete(ctx, "doomed.txt"))

		exists, err := u.Exists(ctx, "doomed.txt")
		test.NoError(t, err)
		test.False(t, exists)
	})

	T.Run("with missing file", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		obs := observability.NewRecordingObserver()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), obs, noop.NewCircuitBreaker())

		test.Error(t, u.Delete(ctx, "never_existed.txt"))

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.FilenameKey: "never_existed.txt",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), cb)

		test.ErrorIs(t, u.Delete(ctx, "anything.txt"), circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})
}

func TestUploader_Exists(T *testing.T) {
	T.Parallel()

	T.Run("present", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		must.NoError(t, u.Save(ctx, "here.txt", bytes.NewReader([]byte(t.Name()))))

		exists, err := u.Exists(ctx, "here.txt")
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("absent", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		exists, err := u.Exists(ctx, "nope.txt")
		test.NoError(t, err)
		test.False(t, exists)
	})

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), cb)

		exists, err := u.Exists(ctx, "anything.txt")
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.False(t, exists)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})
}

func TestUploader_Save_withContentType(T *testing.T) {
	T.Parallel()

	T.Run("stores an explicit content type", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		must.NoError(t, u.Save(ctx, "note.txt", bytes.NewReader([]byte("hello")), uploads.WithContentType("text/plain")))

		attrs, err := u.Attributes(ctx, "note.txt")
		test.NoError(t, err)
		must.NotNil(t, attrs)
		test.StrContains(t, attrs.ContentType, "text/plain")
	})

	T.Run("sniffs the content type when unset", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		must.NoError(t, u.Save(ctx, "note.txt", bytes.NewReader([]byte("hello world"))))

		attrs, err := u.Attributes(ctx, "note.txt")
		test.NoError(t, err)
		must.NotNil(t, attrs)
		test.StrContains(t, attrs.ContentType, "text/plain")
	})
}

func TestUploader_OpenRange(T *testing.T) {
	T.Parallel()

	T.Run("reads a suffix range", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.WriteAll(ctx, "greeting.txt", []byte("hello world"), nil))

		u := newTestUploader(t, b, observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		r, err := u.OpenRange(ctx, "greeting.txt", 6, -1)
		test.NoError(t, err)
		test.EqOp(t, "world", string(readAll(t, r)))
	})

	T.Run("reads a bounded range", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.WriteAll(ctx, "greeting.txt", []byte("hello world"), nil))

		u := newTestUploader(t, b, observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		r, err := u.OpenRange(ctx, "greeting.txt", 0, 5)
		test.NoError(t, err)
		test.EqOp(t, "hello", string(readAll(t, r)))
	})

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &cbmock.CircuitBreakerMock{CannotProceedFunc: func() bool { return true }}
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), cb)

		r, err := u.OpenRange(ctx, "anything.txt", 0, -1)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.Nil(t, r)
	})
}

func TestUploader_Attributes(T *testing.T) {
	T.Parallel()

	T.Run("reports size", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		content := []byte("hello world")
		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.WriteAll(ctx, "greeting.txt", content, nil))

		u := newTestUploader(t, b, observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		attrs, err := u.Attributes(ctx, "greeting.txt")
		test.NoError(t, err)
		must.NotNil(t, attrs)
		test.EqOp(t, int64(len(content)), attrs.Size)
	})

	T.Run("with missing file", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		obs := observability.NewRecordingObserver()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), obs, noop.NewCircuitBreaker())

		attrs, err := u.Attributes(ctx, "nope.txt")
		test.Error(t, err)
		test.Nil(t, attrs)

		op := obs.ObservedOperationWithData(t, map[string]any{keys.FilenameKey: "nope.txt"})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &cbmock.CircuitBreakerMock{CannotProceedFunc: func() bool { return true }}
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), cb)

		attrs, err := u.Attributes(ctx, "anything.txt")
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.Nil(t, attrs)
	})
}

func TestUploader_List(T *testing.T) {
	T.Parallel()

	T.Run("streams objects under a prefix", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.WriteAll(ctx, "data/a.txt", []byte("a"), nil))
		must.NoError(t, b.WriteAll(ctx, "data/b.txt", []byte("b"), nil))
		must.NoError(t, b.WriteAll(ctx, "other/c.txt", []byte("c"), nil))

		u := newTestUploader(t, b, observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		seen := map[string]bool{}
		for obj, err := range u.List(ctx, "data/") {
			must.NoError(t, err)
			seen[obj.Path] = true
		}

		test.EqOp(t, 2, len(seen))
		test.True(t, seen["data/a.txt"])
		test.True(t, seen["data/b.txt"])
	})

	T.Run("ListAll drains into a slice", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.WriteAll(ctx, "a.txt", []byte("a"), nil))
		must.NoError(t, b.WriteAll(ctx, "b.txt", []byte("b"), nil))

		u := newTestUploader(t, b, observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		objs, err := uploads.ListAll(ctx, u, "")
		test.NoError(t, err)
		test.SliceLen(t, 2, objs)
	})

	T.Run("stops early when the caller breaks", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.WriteAll(ctx, "a.txt", []byte("a"), nil))
		must.NoError(t, b.WriteAll(ctx, "b.txt", []byte("b"), nil))
		must.NoError(t, b.WriteAll(ctx, "c.txt", []byte("c"), nil))

		u := newTestUploader(t, b, observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		seen := 0
		for _, err := range u.List(ctx, "") {
			must.NoError(t, err)
			seen++
			break
		}

		test.EqOp(t, 1, seen)
	})

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &cbmock.CircuitBreakerMock{CannotProceedFunc: func() bool { return true }}
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), cb)

		var gotErr error
		for _, err := range u.List(ctx, "") {
			gotErr = err
			break
		}

		test.ErrorIs(t, gotErr, circuitbreaking.ErrCircuitBroken)
	})
}

func TestUploader_SignedURL(T *testing.T) {
	T.Parallel()

	T.Run("memory provider does not support signing", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), noop.NewCircuitBreaker())

		signedURL, err := u.SignedURL(ctx, "greeting.txt", nil)
		test.Error(t, err)
		test.EqOp(t, "", signedURL)
	})

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &cbmock.CircuitBreakerMock{CannotProceedFunc: func() bool { return true }}
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), observability.NewObserverForTest(t.Name()), cb)

		signedURL, err := u.SignedURL(ctx, "greeting.txt", &uploads.SignedURLOptions{Expiry: 0})
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.EqOp(t, "", signedURL)
	})
}

// failingReader always errors, to exercise write-failure paths.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// partialReader yields some bytes on its first read and then errors, simulating a body that
// fails part-way through a copy.
type partialReader struct{ done bool }

func (p *partialReader) Read(b []byte) (int, error) {
	if p.done {
		return 0, io.ErrUnexpectedEOF
	}
	p.done = true

	return copy(b, "partial content"), io.ErrUnexpectedEOF
}

func TestUploader_Save_copyError(T *testing.T) {
	T.Parallel()

	T.Run("reports a failure when the source read fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		obs := observability.NewRecordingObserver()
		u := newTestUploader(t, memblob.OpenBucket(&memblob.Options{}), obs, cb)

		test.Error(t, u.Save(ctx, "broken.txt", failingReader{}))
		test.SliceLen(t, 1, cb.FailedCalls())

		op := obs.ObservedOperationWithData(t, map[string]any{keys.FilenameKey: "broken.txt"})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("leaves no committed object when the copy fails mid-stream", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		b := memblob.OpenBucket(&memblob.Options{})
		u := newTestUploader(t, b, observability.NewObserverForTest(t.Name()), cb)

		test.Error(t, u.Save(ctx, "truncated.txt", &partialReader{}))

		// The aborted write must not commit a truncated object at the key.
		exists, err := b.Exists(ctx, "truncated.txt")
		test.NoError(t, err)
		test.False(t, exists)
	})
}

func TestUploader_Exists_error(T *testing.T) {
	T.Parallel()

	T.Run("reports errors from the bucket", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.Close())

		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		u := newTestUploader(t, b, observability.NewObserverForTest(t.Name()), cb)

		exists, err := u.Exists(ctx, "whatever.txt")
		test.Error(t, err)
		test.False(t, exists)
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}
