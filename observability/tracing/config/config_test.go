package tracingcfg

import (
	"os"
	"path/filepath"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/tracing/cloudtrace"
	"github.com/primandproper/platform-go/v6/observability/tracing/oteltrace"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_NewTracerProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		tracerProvider, err := cfg.NewTracerProvider(
			t.Context(),
			loggingnoop.NewLogger(),
		)

		test.NoError(t, err)
		test.NotNil(t, tracerProvider)
	})

	T.Run("with otel provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:                  ProviderOtel,
			ServiceName:               t.Name(),
			SpanCollectionProbability: 1,
			Otel: &oteltrace.Config{
				CollectorEndpoint: "localhost:4317",
				Insecure:          true,
			},
		}

		tracerProvider, err := cfg.NewTracerProvider(
			t.Context(),
			loggingnoop.NewLogger(),
		)

		test.NoError(t, err)
		test.NotNil(t, tracerProvider)
	})
}

// TestConfig_NewTracerProvider_CloudTrace covers the cloudtrace branch.
// It must not run in parallel because it sets GOOGLE_APPLICATION_CREDENTIALS.
func TestConfig_NewTracerProvider_CloudTrace(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "creds.json")
	must.NoError(t, os.WriteFile(credPath, []byte(`{"type":"authorized_user","client_id":"x","client_secret":"y","refresh_token":"z"}`), 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credPath)

	cfg := &Config{
		Provider:                  ProviderCloudTrace,
		ServiceName:               t.Name(),
		SpanCollectionProbability: 1,
		CloudTrace: &cloudtrace.Config{
			ProjectID: "fake-project",
		},
	}

	tracerProvider, err := cfg.NewTracerProvider(
		t.Context(),
		loggingnoop.NewLogger(),
	)

	must.NoError(t, err)
	test.NotNil(t, tracerProvider)
}

// TestConfig_NewTracerProvider_CloudTraceError covers the cloudtrace error branch.
// It must not run in parallel because it sets GOOGLE_APPLICATION_CREDENTIALS.
func TestConfig_NewTracerProvider_CloudTraceError(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "nonexistent.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credPath)

	cfg := &Config{
		Provider:                  ProviderCloudTrace,
		ServiceName:               t.Name(),
		SpanCollectionProbability: 1,
		CloudTrace: &cloudtrace.Config{
			ProjectID: "fake-project",
		},
	}

	tracerProvider, err := cfg.NewTracerProvider(
		t.Context(),
		loggingnoop.NewLogger(),
	)

	test.Error(t, err)
	test.Nil(t, tracerProvider)
}

// TestConfig_NewTracerProvider_OtelError covers the otelgrpc error branch.
func TestConfig_NewTracerProvider_OtelError(T *testing.T) {
	T.Parallel()

	T.Run("with invalid otel endpoint", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:                  ProviderOtel,
			ServiceName:               t.Name(),
			SpanCollectionProbability: 1,
			Otel: &oteltrace.Config{
				// Control character in endpoint causes URL parse failure inside otlptracegrpc.
				CollectorEndpoint: "\x00bad",
			},
		}

		tracerProvider, err := cfg.NewTracerProvider(t.Context(), loggingnoop.NewLogger())
		test.Error(t, err)
		test.Nil(t, tracerProvider)
	})
}

// TestConfig_NewTracer_Error covers the error wrap branch in NewTracer.
func TestConfig_NewTracer_Error(T *testing.T) {
	T.Parallel()

	T.Run("propagates provider error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:                  ProviderOtel,
			ServiceName:               t.Name(),
			SpanCollectionProbability: 1,
			Otel: &oteltrace.Config{
				CollectorEndpoint: "\x00bad",
			},
		}

		tracer, err := cfg.NewTracer(t.Context(), loggingnoop.NewLogger(), t.Name())
		test.Error(t, err)
		test.Nil(t, tracer)
	})
}

func TestConfig_NewTracer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		tracer, err := cfg.NewTracer(t.Context(), loggingnoop.NewLogger(), t.Name())
		test.NoError(t, err)
		test.NotNil(t, tracer)
	})

	T.Run("with otel provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:                  ProviderOtel,
			ServiceName:               t.Name(),
			SpanCollectionProbability: 1,
			Otel: &oteltrace.Config{
				CollectorEndpoint: "localhost:4317",
				Insecure:          true,
			},
		}

		tracer, err := cfg.NewTracer(t.Context(), loggingnoop.NewLogger(), t.Name())
		test.NoError(t, err)
		test.NotNil(t, tracer)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:                  ProviderOtel,
			ServiceName:               t.Name(),
			SpanCollectionProbability: 1,
			Otel: &oteltrace.Config{
				CollectorEndpoint: t.Name(),
			},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with cloudtrace provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:                  ProviderCloudTrace,
			ServiceName:               t.Name(),
			SpanCollectionProbability: 1,
			CloudTrace: &cloudtrace.Config{
				ProjectID: t.Name(),
			},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("missing required service name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:                  ProviderOtel,
			SpanCollectionProbability: 1,
			Otel: &oteltrace.Config{
				CollectorEndpoint: t.Name(),
			},
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:                  "bogus",
			ServiceName:               t.Name(),
			SpanCollectionProbability: 1,
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewTracerProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		tracerProvider, err := NewTracerProvider(t.Context(), cfg, loggingnoop.NewLogger())
		test.NoError(t, err)
		test.NotNil(t, tracerProvider)
	})
}
