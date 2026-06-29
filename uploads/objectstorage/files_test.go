package objectstorage

import (
	"testing"

	"github.com/primandproper/platform-go/v2/circuitbreaking"
	cbmock "github.com/primandproper/platform-go/v2/circuitbreaking/mock"
	"github.com/primandproper/platform-go/v2/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v2/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"gocloud.dev/blob/memblob"
)

func noopUploaderMetrics(t *testing.T) (saveCounter, readCounter, saveErrCounter, readErrCounter metrics.Int64Counter, latencyHist metrics.Float64Histogram) {
	t.Helper()
	mp := metricsnoop.NewMetricsProvider()

	saveCounter, err := mp.NewInt64Counter("test_saves")
	must.NoError(t, err)

	readCounter, err = mp.NewInt64Counter("test_reads")
	must.NoError(t, err)

	saveErrCounter, err = mp.NewInt64Counter("test_save_errors")
	must.NoError(t, err)

	readErrCounter, err = mp.NewInt64Counter("test_read_errors")
	must.NoError(t, err)

	latencyHist, err = mp.NewFloat64Histogram("test_latency")
	must.NoError(t, err)

	return saveCounter, readCounter, saveErrCounter, readErrCounter, latencyHist
}

func TestUploader_ReadFile(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleFilename := "hello_world.txt"
		expectedContent := []byte(t.Name())

		b := memblob.OpenBucket(&memblob.Options{})
		must.NoError(t, b.WriteAll(ctx, exampleFilename, expectedContent, nil))

		obs := observability.NewRecordingObserver()
		saveCounter, readCounter, saveErrCounter, readErrCounter, latencyHist := noopUploaderMetrics(t)
		u := &Uploader{
			bucket:         b,
			o11y:           obs,
			circuitBreaker: noop.NewCircuitBreaker(),
			saveCounter:    saveCounter,
			readCounter:    readCounter,
			saveErrCounter: saveErrCounter,
			readErrCounter: readErrCounter,
			latencyHist:    latencyHist,
		}

		x, err := u.ReadFile(ctx, exampleFilename)
		test.NoError(t, err)
		test.Eq(t, expectedContent, x)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.FilenameKey: exampleFilename,
		})
	})

	T.Run("with invalid file", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleFilename := "hello_world.txt"

		obs := observability.NewRecordingObserver()
		saveCounter, readCounter, saveErrCounter, readErrCounter, latencyHist := noopUploaderMetrics(t)
		u := &Uploader{
			bucket:         memblob.OpenBucket(&memblob.Options{}),
			o11y:           obs,
			circuitBreaker: noop.NewCircuitBreaker(),
			saveCounter:    saveCounter,
			readCounter:    readCounter,
			saveErrCounter: saveErrCounter,
			readErrCounter: readErrCounter,
			latencyHist:    latencyHist,
		}

		x, err := u.ReadFile(ctx, exampleFilename)
		test.Error(t, err)
		test.Nil(t, x)

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

		saveCounter, readCounter, saveErrCounter, readErrCounter, latencyHist := noopUploaderMetrics(t)
		u := &Uploader{
			bucket:         memblob.OpenBucket(&memblob.Options{}),
			o11y:           observability.NewObserverForTest(t.Name()),
			circuitBreaker: cb,
			saveCounter:    saveCounter,
			readCounter:    readCounter,
			saveErrCounter: saveErrCounter,
			readErrCounter: readErrCounter,
			latencyHist:    latencyHist,
		}

		x, err := u.ReadFile(ctx, "anything.txt")
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.Nil(t, x)
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

		saveCounter, readCounter, saveErrCounter, readErrCounter, latencyHist := noopUploaderMetrics(t)
		u := &Uploader{
			bucket:         b,
			o11y:           observability.NewObserverForTest(t.Name()),
			circuitBreaker: cb,
			saveCounter:    saveCounter,
			readCounter:    readCounter,
			saveErrCounter: saveErrCounter,
			readErrCounter: readErrCounter,
			latencyHist:    latencyHist,
		}

		x, err := u.ReadFile(ctx, exampleFilename)
		test.NoError(t, err)
		test.Eq(t, expectedContent, x)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})
}

func TestUploader_SaveFile(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		obs := observability.NewRecordingObserver()
		saveCounter, readCounter, saveErrCounter, readErrCounter, latencyHist := noopUploaderMetrics(t)
		u := &Uploader{
			bucket:         memblob.OpenBucket(&memblob.Options{}),
			o11y:           obs,
			circuitBreaker: noop.NewCircuitBreaker(),
			saveCounter:    saveCounter,
			readCounter:    readCounter,
			saveErrCounter: saveErrCounter,
			readErrCounter: readErrCounter,
			latencyHist:    latencyHist,
		}

		test.NoError(t, u.SaveFile(ctx, "test_file.txt", []byte(t.Name())))

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

		saveCounter, readCounter, saveErrCounter, readErrCounter, latencyHist := noopUploaderMetrics(t)
		u := &Uploader{
			bucket:         memblob.OpenBucket(&memblob.Options{}),
			o11y:           observability.NewObserverForTest(t.Name()),
			circuitBreaker: cb,
			saveCounter:    saveCounter,
			readCounter:    readCounter,
			saveErrCounter: saveErrCounter,
			readErrCounter: readErrCounter,
			latencyHist:    latencyHist,
		}

		test.ErrorIs(t, u.SaveFile(ctx, "test_file.txt", []byte(t.Name())), circuitbreaking.ErrCircuitBroken)
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
		saveCounter, readCounter, saveErrCounter, readErrCounter, latencyHist := noopUploaderMetrics(t)
		u := &Uploader{
			bucket:         b,
			o11y:           obs,
			circuitBreaker: cb,
			saveCounter:    saveCounter,
			readCounter:    readCounter,
			saveErrCounter: saveErrCounter,
			readErrCounter: readErrCounter,
			latencyHist:    latencyHist,
		}

		test.Error(t, u.SaveFile(ctx, "test_file.txt", []byte(t.Name())))
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

		saveCounter, readCounter, saveErrCounter, readErrCounter, latencyHist := noopUploaderMetrics(t)
		u := &Uploader{
			bucket:         memblob.OpenBucket(&memblob.Options{}),
			o11y:           observability.NewObserverForTest(t.Name()),
			circuitBreaker: noop.NewCircuitBreaker(),
			saveCounter:    saveCounter,
			readCounter:    readCounter,
			saveErrCounter: saveErrCounter,
			readErrCounter: readErrCounter,
			latencyHist:    latencyHist,
		}

		must.NoError(t, u.SaveFile(ctx, "roundtrip.txt", content))

		actual, err := u.ReadFile(ctx, "roundtrip.txt")
		test.NoError(t, err)
		test.Eq(t, content, actual)
	})
}
