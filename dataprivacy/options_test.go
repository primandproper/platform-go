package dataprivacy

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	auditmock "github.com/primandproper/platform-go/v13/audit/mock"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/compression"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// Every option here follows the same contract: a usable value is applied, and a
// zero value leaves the existing setting alone rather than clearing it. The
// second half is the part worth testing — an option that nils out a dependency
// when handed a nil turns "I did not configure this" into a panic at the first
// request, and the dependencies here are the ones that write a person's data to
// storage.

func TestServiceOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithServiceClock", func(t *testing.T) {
		t.Parallel()

		original := clock.NewClock()
		s := &StoreService{clock: original}

		replacement := clock.NewClock()
		WithServiceClock(replacement)(s)
		test.True(t, s.clock == replacement)

		WithServiceClock(nil)(s)
		test.True(t, s.clock == replacement)
	})

	T.Run("WithServiceLogger", func(t *testing.T) {
		t.Parallel()

		s := &StoreService{}

		logger := loggingnoop.NewLogger()
		WithServiceLogger(logger)(s)
		test.NotNil(t, s.logger)
	})

	T.Run("WithServiceTracerProvider", func(t *testing.T) {
		t.Parallel()

		s := &StoreService{}

		WithServiceTracerProvider(tracingnoop.NewTracerProvider())(s)
		test.NotNil(t, s.tracerProvider)
	})

	T.Run("WithServiceMetricsProvider", func(t *testing.T) {
		t.Parallel()

		s := &StoreService{}

		WithServiceMetricsProvider(metrics.EnsureMetricsProvider(nil))(s)
		test.NotNil(t, s.metricsProvider)
	})

	T.Run("WithServiceUploadManager", func(t *testing.T) {
		t.Parallel()

		uploader := newMemoryUploader()
		s := &StoreService{}

		WithServiceUploadManager(uploader)(s)
		test.NotNil(t, s.uploader)

		// Nilling this out would turn every Download into an unavailable
		// artifact rather than a configuration error.
		WithServiceUploadManager(nil)(s)
		test.NotNil(t, s.uploader)
	})

	T.Run("WithServiceCompressor", func(t *testing.T) {
		t.Parallel()

		compressor, err := compression.NewCompressor(compression.AlgorithmZstd)
		must.NoError(t, err)

		s := &StoreService{}

		WithServiceCompressor(compressor)(s)
		test.NotNil(t, s.packager.compressor)

		// A cleared compressor reads a compressed artifact as garbage.
		WithServiceCompressor(nil)(s)
		test.NotNil(t, s.packager.compressor)
	})

	T.Run("WithServiceDecryptor", func(t *testing.T) {
		t.Parallel()

		decryptor, err := newTestEncryptorDecryptor([]byte("0123456789abcdef0123456789abcdef"))
		must.NoError(t, err)

		s := &StoreService{}

		WithServiceDecryptor(decryptor)(s)
		test.NotNil(t, s.packager.decryptor)

		// Setting a decryptor also records that artifacts are encrypted, which
		// is what makes Download refuse rather than hand out ciphertext.
		test.True(t, s.packager.encrypts())

		WithServiceDecryptor(nil)(s)
		test.NotNil(t, s.packager.decryptor)
	})

	T.Run("WithServiceAuditRecorder", func(t *testing.T) {
		t.Parallel()

		s := &StoreService{}

		WithServiceAuditRecorder(&auditmock.RecorderMock{})(s)
		test.NotNil(t, s.recorder)

		// Clearing it would silently stop recording who exported whose data.
		WithServiceAuditRecorder(nil)(s)
		test.NotNil(t, s.recorder)
	})

	T.Run("WithActorResolver", func(t *testing.T) {
		t.Parallel()

		s := &StoreService{actor: func(context.Context) audit.Actor {
			return audit.Actor{ID: "original"}
		}}

		WithActorResolver(func(context.Context) audit.Actor {
			return audit.Actor{ID: "replacement"}
		})(s)
		test.EqOp(t, "replacement", s.actor(t.Context()).ID)

		WithActorResolver(nil)(s)
		test.EqOp(t, "replacement", s.actor(t.Context()).ID)
	})

	T.Run("the marker encryptor never encrypts", func(t *testing.T) {
		t.Parallel()

		// It exists only so Download can tell that artifacts are ciphertext;
		// the Service has no business encrypting anything.
		_, err := encryptorPresent{}.Encrypt(t.Context(), []byte("plaintext"), nil)
		test.Error(t, err)
	})
}

func TestFulfillerOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithFulfillerClock", func(t *testing.T) {
		t.Parallel()

		original := clock.NewClock()
		w := &Fulfiller{clock: original}

		replacement := clock.NewClock()
		WithFulfillerClock(replacement)(w)
		test.True(t, w.clock == replacement)

		WithFulfillerClock(nil)(w)
		test.True(t, w.clock == replacement)
	})

	T.Run("WithFulfillerLogger", func(t *testing.T) {
		t.Parallel()

		w := &Fulfiller{}

		WithFulfillerLogger(loggingnoop.NewLogger())(w)
		test.NotNil(t, w.logger)
	})

	T.Run("WithFulfillerTracerProvider", func(t *testing.T) {
		t.Parallel()

		w := &Fulfiller{}

		WithFulfillerTracerProvider(tracingnoop.NewTracerProvider())(w)
		test.NotNil(t, w.tracerProvider)
	})

	T.Run("WithFulfillerMetricsProvider", func(t *testing.T) {
		t.Parallel()

		w := &Fulfiller{}

		WithFulfillerMetricsProvider(metrics.EnsureMetricsProvider(nil))(w)
		test.NotNil(t, w.metricsProvider)
	})

	T.Run("WithFulfillerUploadManager", func(t *testing.T) {
		t.Parallel()

		w := &Fulfiller{}

		WithFulfillerUploadManager(newMemoryUploader())(w)
		test.NotNil(t, w.uploader)

		WithFulfillerUploadManager(nil)(w)
		test.NotNil(t, w.uploader)
	})

	T.Run("WithFulfillerCompressor", func(t *testing.T) {
		t.Parallel()

		compressor, err := compression.NewCompressor(compression.AlgorithmZstd)
		must.NoError(t, err)

		w := &Fulfiller{}

		WithFulfillerCompressor(compressor)(w)
		test.NotNil(t, w.packager.compressor)

		WithFulfillerCompressor(nil)(w)
		test.NotNil(t, w.packager.compressor)
	})

	T.Run("WithFulfillerEncryptor", func(t *testing.T) {
		t.Parallel()

		encryptor, err := newTestEncryptorDecryptor([]byte("0123456789abcdef0123456789abcdef"))
		must.NoError(t, err)

		w := &Fulfiller{}

		WithFulfillerEncryptor(encryptor)(w)
		test.True(t, w.packager.encrypts())

		// Clearing it would silently start writing plaintext exports to a
		// bucket an operator believes is encrypted.
		WithFulfillerEncryptor(nil)(w)
		test.True(t, w.packager.encrypts())
	})

	T.Run("WithFulfillerNotifier", func(t *testing.T) {
		t.Parallel()

		w := &Fulfiller{}

		WithFulfillerNotifier(NotifierFunc(func(context.Context, *Notification) error { return nil }))(w)
		test.NotNil(t, w.notifier)

		WithFulfillerNotifier(nil)(w)
		test.NotNil(t, w.notifier)
	})

	T.Run("WithFulfillerAuditRecorder", func(t *testing.T) {
		t.Parallel()

		w := &Fulfiller{}

		WithFulfillerAuditRecorder(&auditmock.RecorderMock{})(w)
		test.NotNil(t, w.recorder)

		WithFulfillerAuditRecorder(nil)(w)
		test.NotNil(t, w.recorder)
	})

	T.Run("WithFulfillerActorResolver", func(t *testing.T) {
		t.Parallel()

		w := &Fulfiller{actor: func(context.Context) audit.Actor {
			return audit.Actor{ID: "original"}
		}}

		WithFulfillerActorResolver(func(context.Context) audit.Actor {
			return audit.Actor{ID: "replacement"}
		})(w)
		test.EqOp(t, "replacement", w.actor(t.Context()).ID)

		WithFulfillerActorResolver(nil)(w)
		test.EqOp(t, "replacement", w.actor(t.Context()).ID)
	})

	T.Run("WithFulfillerURLSigner", func(t *testing.T) {
		t.Parallel()

		w := &Fulfiller{}

		WithFulfillerURLSigner(func(context.Context, *Request) (string, time.Time) {
			return "https://example/x", time.Time{}
		})(w)
		must.NotNil(t, w.signer)

		url, _ := w.signer(t.Context(), &Request{})
		test.EqOp(t, "https://example/x", url)

		WithFulfillerURLSigner(nil)(w)
		must.NotNil(t, w.signer)
	})
}

func TestSweeperOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithSweeperClock", func(t *testing.T) {
		t.Parallel()

		original := clock.NewClock()
		s := &Sweeper{clock: original}

		replacement := clock.NewClock()
		WithSweeperClock(replacement)(s)
		test.True(t, s.clock == replacement)

		WithSweeperClock(nil)(s)
		test.True(t, s.clock == replacement)
	})

	T.Run("WithSweeperLogger", func(t *testing.T) {
		t.Parallel()

		s := &Sweeper{}

		WithSweeperLogger(loggingnoop.NewLogger())(s)
		test.NotNil(t, s.logger)
	})

	T.Run("WithSweeperTracerProvider", func(t *testing.T) {
		t.Parallel()

		s := &Sweeper{}

		WithSweeperTracerProvider(tracingnoop.NewTracerProvider())(s)
		test.NotNil(t, s.tracerProvider)
	})

	T.Run("WithSweeperMetricsProvider", func(t *testing.T) {
		t.Parallel()

		s := &Sweeper{}

		WithSweeperMetricsProvider(metrics.EnsureMetricsProvider(nil))(s)
		test.NotNil(t, s.metricsProvider)
	})

	T.Run("WithSweeperUploadManager", func(t *testing.T) {
		t.Parallel()

		s := &Sweeper{}

		WithSweeperUploadManager(newMemoryUploader())(s)
		test.NotNil(t, s.uploader)

		// Without it the sweeper expires nothing, so clearing it would silently
		// stop artifacts ever being deleted.
		WithSweeperUploadManager(nil)(s)
		test.NotNil(t, s.uploader)
	})
}

func TestSQLStoreOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithTablePrefix", func(t *testing.T) {
		t.Parallel()

		s := &SQLStore{tables: newTables(DefaultTablePrefix)}

		WithTablePrefix("custom")(s)
		test.EqOp(t, "custom", s.tables.prefix())
		test.EqOp(t, "custom_dataprivacy_requests", s.tables.requests)

		// An empty prefix would render a table named "_requests".
		WithTablePrefix("")(s)
		test.EqOp(t, "custom", s.tables.prefix())
	})

	T.Run("observability options", func(t *testing.T) {
		t.Parallel()

		s := &SQLStore{tables: newTables(DefaultTablePrefix)}

		WithStoreLogger(loggingnoop.NewLogger())(s)
		test.NotNil(t, s.logger)

		WithStoreTracerProvider(tracingnoop.NewTracerProvider())(s)
		test.NotNil(t, s.tracerProvider)

		WithStoreMetricsProvider(metrics.EnsureMetricsProvider(nil))(s)
		test.NotNil(t, s.metricsProvider)
	})
}
