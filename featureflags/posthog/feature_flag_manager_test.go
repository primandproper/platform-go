package posthog

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingmock "github.com/primandproper/platform-go/v13/circuitbreaking/mock"
	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/featureflags/internal/openfeatureflags"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	openfeatureposthog "github.com/dhaus67/openfeature-posthog-go"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/posthog/posthog-go"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

var testFlags = map[string]any{
	"bool-flag":   true,
	"string-flag": "hello-world",
	"int-flag":    "42",
	"float-flag":  "3.14",
	"object-flag": `{"key":"value"}`,
}

func evalCtx(targetingKey string) featureflags.EvaluationContext {
	return featureflags.EvaluationContext{TargetingKey: targetingKey}
}

func posthogFlagsHandler(flags map[string]any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"flags":              []any{},
				"group_type_mapping": map[string]string{},
			})
		case strings.HasPrefix(r.URL.Path, "/flags/"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"featureFlags":        flags,
				"featureFlagPayloads": map[string]any{},
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}

func posthogErrorHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"flags":              []any{},
				"group_type_mapping": map[string]string{},
			})
		case strings.HasPrefix(r.URL.Path, "/flags/"):
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}

func buildTestManager(t *testing.T, cb circuitbreaking.CircuitBreaker, configModifiers ...func(config *posthog.Config)) *FeatureFlagManager {
	t.Helper()

	cfg := &Config{
		ProjectAPIKey:  t.Name(),
		PersonalAPIKey: t.Name(),
	}

	ffm, err := NewFeatureFlagManager(cfg, cb, WithConfigModifiers(configModifiers...))
	must.NoError(t, err)
	must.NotNil(t, ffm)

	return ffm
}

func buildTestManagerWithHandler(t *testing.T, cb circuitbreaking.CircuitBreaker, handler http.Handler) *FeatureFlagManager {
	t.Helper()

	ts := httptest.NewServer(handler)

	phConfig := posthog.Config{
		PersonalApiKey: t.Name(),
		Endpoint:       ts.URL,
	}

	client, err := posthog.NewWithConfig(t.Name(), phConfig)
	must.NoError(t, err)

	t.Cleanup(func() {
		client.Close()
		ts.Close()
	})

	// Use a unique domain per test to avoid global OpenFeature provider conflicts.
	domain := "test_" + strings.ReplaceAll(t.Name(), "/", "_")
	provider := openfeatureposthog.NewProvider(client)
	err = openfeature.SetNamedProviderAndWait(domain, provider)
	must.NoError(t, err)

	ofClient := openfeature.NewClient(domain)

	mp := metrics.EnsureMetricsProvider(nil)
	evalCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_evaluations", serviceName))
	must.NoError(t, err)
	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", serviceName))
	must.NoError(t, err)
	notFoundCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_flags_not_found", serviceName))
	must.NoError(t, err)
	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName))
	must.NoError(t, err)

	return &FeatureFlagManager{
		posthogClient: client,
		Evaluator: openfeatureflags.Evaluator{
			Client:          ofClient,
			CircuitBreaker:  cb,
			O11y:            observability.NewObserver(serviceName, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider()),
			Domain:          domain,
			EvalCounter:     evalCounter,
			ErrorCounter:    errorCounter,
			NotFoundCounter: notFoundCounter,
			LatencyHist:     latencyHist,
		},
	}
}

// withRecordingObserver swaps a RecordingObserver into the manager and returns it,
// so a test can assert which fields an evaluation observed.
func withRecordingObserver(ffm *FeatureFlagManager) *observability.RecordingObserver {
	obs := observability.NewRecordingObserver()
	ffm.O11y = obs

	return obs
}

func TestNewFeatureFlagManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			ProjectAPIKey:  t.Name(),
			PersonalAPIKey: t.Name(),
		}

		actual, err := NewFeatureFlagManager(cfg, cbnoop.NewCircuitBreaker())
		test.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		actual, err := NewFeatureFlagManager(nil, cbnoop.NewCircuitBreaker())
		test.Error(t, err)
		test.Nil(t, actual)
	})

	T.Run("with missing project API key", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		actual, err := NewFeatureFlagManager(cfg, cbnoop.NewCircuitBreaker())
		test.Error(t, err)
		test.Nil(t, actual)
	})

	T.Run("with missing personal API key", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			ProjectAPIKey: t.Name(),
		}

		actual, err := NewFeatureFlagManager(cfg, cbnoop.NewCircuitBreaker())
		test.Error(t, err)
		test.Nil(t, actual)
	})

	T.Run("with invalid config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			ProjectAPIKey:  t.Name(),
			PersonalAPIKey: t.Name(),
		}

		actual, err := NewFeatureFlagManager(
			cfg,
			cbnoop.NewCircuitBreaker(),
			WithConfigModifiers(func(config *posthog.Config) {
				config.Interval = -1
			}),
		)
		test.Error(t, err)
		test.Nil(t, actual)
	})
}

func TestNewFeatureFlagManager_metricInitErrors(T *testing.T) {
	T.Parallel()

	T.Run("with error creating eval counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, serviceName+"_evaluations", counterName)
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		actual, err := NewFeatureFlagManager(
			&Config{ProjectAPIKey: t.Name(), PersonalAPIKey: t.Name()},
			cbnoop.NewCircuitBreaker(),
			WithMetricsProvider(mp),
		)
		must.Error(t, err)
		must.Nil(t, actual)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case serviceName + "_evaluations":
					return metricstest.Int64Counter(t, "x"), nil
				case serviceName + "_errors":
					return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		actual, err := NewFeatureFlagManager(
			&Config{ProjectAPIKey: t.Name(), PersonalAPIKey: t.Name()},
			cbnoop.NewCircuitBreaker(),
			WithMetricsProvider(mp),
		)
		must.Error(t, err)
		must.Nil(t, actual)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), nil
			},
			NewFloat64HistogramFunc: func(histName string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				test.EqOp(t, serviceName+"_latency_ms", histName)
				return nil, errors.New("arbitrary")
			},
		}

		actual, err := NewFeatureFlagManager(
			&Config{ProjectAPIKey: t.Name(), PersonalAPIKey: t.Name()},
			cbnoop.NewCircuitBreaker(),
			WithMetricsProvider(mp),
		)
		must.Error(t, err)
		must.Nil(t, actual)
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func TestFeatureFlagManager_CanUseFeature(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithHandler(t, cbnoop.NewCircuitBreaker(), posthogFlagsHandler(testFlags))
		obs := withRecordingObserver(ffm)

		actual, err := ffm.CanUseFeature(ctx, "bool-flag", evalCtx("user123"))
		test.NoError(t, err)
		test.True(t, actual)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "bool-flag",
		})
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManagerWithHandler(t, cb, posthogErrorHandler())
		obs := withRecordingObserver(ffm)

		actual, err := ffm.CanUseFeature(ctx, "bool-flag", evalCtx("user123"))
		test.Error(t, err)
		test.False(t, actual)
		test.False(t, errors.Is(err, featureflags.ErrFlagNotFound))

		// A provider that cannot answer is exactly what the breaker is for.
		test.SliceLen(t, 1, cb.FailedCalls())
		test.SliceLen(t, 0, cb.SucceededCalls())

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "bool-flag",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	// Unlike its four typed siblings, a missing boolean flag is not reported as
	// missing at all. PostHog's API answers false for a flag it does not know, so
	// the OpenFeature provider skips the found check for booleans entirely rather
	// than call every false a not-found. The evaluation therefore takes the
	// ordinary success path — a decided false, and a success for the breaker —
	// which is the inert outcome the sentinel exists to produce, reached without
	// needing it.
	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManagerWithHandler(t, cb, posthogFlagsHandler(testFlags))

		actual, err := ffm.CanUseFeature(ctx, "nonexistent-flag", evalCtx("user123"))
		test.NoError(t, err)
		test.False(t, actual)
		test.SliceLen(t, 0, cb.FailedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("with broken circuit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return false },
		}

		ffm := buildTestManager(t, cb)

		result, err := ffm.CanUseFeature(ctx, "some-flag", evalCtx("user123"))
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.False(t, result)
		test.SliceLen(t, 1, cb.CanProceedCalls())
	})
}

func TestFeatureFlagManager_GetStringValue(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithHandler(t, cbnoop.NewCircuitBreaker(), posthogFlagsHandler(testFlags))
		obs := withRecordingObserver(ffm)

		result, err := ffm.GetStringValue(ctx, "string-flag", "fallback", evalCtx("user123"))
		test.NoError(t, err)
		test.EqOp(t, "hello-world", result)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "string-flag",
		})
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithHandler(t, cbnoop.NewCircuitBreaker(), posthogErrorHandler())
		obs := withRecordingObserver(ffm)

		result, err := ffm.GetStringValue(ctx, "string-flag", "fallback", evalCtx("user123"))
		test.Error(t, err)
		test.EqOp(t, "fallback", result)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "string-flag",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManagerWithHandler(t, cb, posthogFlagsHandler(testFlags))

		result, err := ffm.GetStringValue(ctx, "nonexistent-flag", "fallback", evalCtx("user123"))
		test.ErrorIs(t, err, featureflags.ErrFlagNotFound)
		test.EqOp(t, "fallback", result)
		test.SliceLen(t, 0, cb.FailedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("with broken circuit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return false },
		}

		ffm := buildTestManager(t, cb)

		result, err := ffm.GetStringValue(ctx, "some-flag", "fallback", evalCtx("user123"))
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.EqOp(t, "fallback", result)
		test.SliceLen(t, 1, cb.CanProceedCalls())
	})
}

func TestFeatureFlagManager_GetInt64Value(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithHandler(t, cbnoop.NewCircuitBreaker(), posthogFlagsHandler(testFlags))
		obs := withRecordingObserver(ffm)

		result, err := ffm.GetInt64Value(ctx, "int-flag", int64(0), evalCtx("user123"))
		test.NoError(t, err)
		test.EqOp(t, int64(42), result)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "int-flag",
		})
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithHandler(t, cbnoop.NewCircuitBreaker(), posthogErrorHandler())
		obs := withRecordingObserver(ffm)

		result, err := ffm.GetInt64Value(ctx, "int-flag", int64(42), evalCtx("user123"))
		test.Error(t, err)
		test.EqOp(t, int64(42), result)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "int-flag",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManagerWithHandler(t, cb, posthogFlagsHandler(testFlags))

		result, err := ffm.GetInt64Value(ctx, "nonexistent-flag", int64(42), evalCtx("user123"))
		test.ErrorIs(t, err, featureflags.ErrFlagNotFound)
		test.EqOp(t, int64(42), result)
		test.SliceLen(t, 0, cb.FailedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("with broken circuit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return false },
		}

		ffm := buildTestManager(t, cb)

		result, err := ffm.GetInt64Value(ctx, "some-flag", int64(42), evalCtx("user123"))
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.EqOp(t, int64(42), result)
		test.SliceLen(t, 1, cb.CanProceedCalls())
	})
}

func TestFeatureFlagManager_GetFloat64Value(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithHandler(t, cbnoop.NewCircuitBreaker(), posthogFlagsHandler(testFlags))
		obs := withRecordingObserver(ffm)

		result, err := ffm.GetFloat64Value(ctx, "float-flag", 0.0, evalCtx("user123"))
		test.NoError(t, err)
		test.InDelta(t, 3.14, result, 1e-9)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "float-flag",
		})
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithHandler(t, cbnoop.NewCircuitBreaker(), posthogErrorHandler())
		obs := withRecordingObserver(ffm)

		result, err := ffm.GetFloat64Value(ctx, "float-flag", 3.14, evalCtx("user123"))
		test.Error(t, err)
		test.InDelta(t, 3.14, result, 1e-9)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "float-flag",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManagerWithHandler(t, cb, posthogFlagsHandler(testFlags))

		result, err := ffm.GetFloat64Value(ctx, "nonexistent-flag", 3.14, evalCtx("user123"))
		test.ErrorIs(t, err, featureflags.ErrFlagNotFound)
		test.InDelta(t, 3.14, result, 1e-9)
		test.SliceLen(t, 0, cb.FailedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("with broken circuit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return false },
		}

		ffm := buildTestManager(t, cb)

		result, err := ffm.GetFloat64Value(ctx, "some-flag", 3.14, evalCtx("user123"))
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.InDelta(t, 3.14, result, 1e-9)
		test.SliceLen(t, 1, cb.CanProceedCalls())
	})
}

func TestFeatureFlagManager_GetObjectValue(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithHandler(t, cbnoop.NewCircuitBreaker(), posthogFlagsHandler(testFlags))
		obs := withRecordingObserver(ffm)

		def := map[string]any{"default": true}
		result, err := ffm.GetObjectValue(ctx, "object-flag", def, evalCtx("user123"))
		test.NoError(t, err)
		test.Eq[any](t, map[string]any{"key": "value"}, result)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "object-flag",
		})
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithHandler(t, cbnoop.NewCircuitBreaker(), posthogErrorHandler())
		obs := withRecordingObserver(ffm)

		def := map[string]any{"k": "v"}
		result, err := ffm.GetObjectValue(ctx, "object-flag", def, evalCtx("user123"))
		test.Error(t, err)
		test.Eq[any](t, def, result)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "object-flag",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManagerWithHandler(t, cb, posthogFlagsHandler(testFlags))

		def := map[string]any{"k": "v"}
		result, err := ffm.GetObjectValue(ctx, "nonexistent-flag", def, evalCtx("user123"))
		test.ErrorIs(t, err, featureflags.ErrFlagNotFound)
		test.Eq[any](t, def, result)
		test.SliceLen(t, 0, cb.FailedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("with broken circuit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return false },
		}

		ffm := buildTestManager(t, cb)

		def := map[string]any{"k": "v"}
		result, err := ffm.GetObjectValue(ctx, "some-flag", def, evalCtx("user123"))
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.Eq[any](t, def, result)
		test.SliceLen(t, 1, cb.CanProceedCalls())
	})
}

func TestFeatureFlagManager_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ffm := buildTestManager(t, cbnoop.NewCircuitBreaker())

		err := ffm.Close()
		test.NoError(t, err)
	})
}
