package launchdarkly

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

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

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldmodel"
	ld "github.com/launchdarkly/go-server-sdk/v6"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoretypes"
	ofld "github.com/open-feature/go-sdk-contrib/providers/launchdarkly/pkg"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func evalCtx(targetingKey string) featureflags.EvaluationContext {
	return featureflags.EvaluationContext{TargetingKey: targetingKey}
}

// fakeLaunchDarklyDataSource provides no flag data.
type fakeLaunchDarklyDataSource struct{}

func (f *fakeLaunchDarklyDataSource) Close() error             { return nil }
func (f *fakeLaunchDarklyDataSource) IsInitialized() bool      { return true }
func (f *fakeLaunchDarklyDataSource) Start(ch chan<- struct{}) { close(ch) }

type fakeLaunchDarklyDataSourceBuilder struct{}

func (b *fakeLaunchDarklyDataSourceBuilder) Build(subsystems.ClientContext) (subsystems.DataSource, error) {
	return &fakeLaunchDarklyDataSource{}, nil
}

// testDataSource pushes pre-configured flag data into the SDK on start.
type testDataSource struct {
	sink  subsystems.DataSourceUpdateSink
	flags []ldstoretypes.KeyedItemDescriptor
}

func (ds *testDataSource) Close() error        { return nil }
func (ds *testDataSource) IsInitialized() bool { return true }
func (ds *testDataSource) Start(ch chan<- struct{}) {
	ds.sink.Init([]ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: ds.flags},
		{Kind: ldstoreimpl.Segments(), Items: nil},
	})
	close(ch)
}

type testDataSourceBuilder struct {
	flags []ldstoretypes.KeyedItemDescriptor
}

func (b *testDataSourceBuilder) Build(ctx subsystems.ClientContext) (subsystems.DataSource, error) {
	return &testDataSource{
		sink:  ctx.GetDataSourceUpdateSink(),
		flags: b.flags,
	}, nil
}

func flagItem(key string, offValue, onValue *ldvalue.Value) ldstoretypes.KeyedItemDescriptor {
	flag := &ldmodel.FeatureFlag{
		Key:         key,
		On:          true,
		Variations:  []ldvalue.Value{*offValue, *onValue},
		Fallthrough: ldmodel.VariationOrRollout{Variation: ldvalue.NewOptionalInt(1)},
		Version:     1,
	}
	ldmodel.PreprocessFlag(flag)
	return ldstoretypes.KeyedItemDescriptor{
		Key:  key,
		Item: ldstoretypes.ItemDescriptor{Version: 1, Item: flag},
	}
}

func testFlagItems() []ldstoretypes.KeyedItemDescriptor {
	boolOff, boolOn := ldvalue.Bool(false), ldvalue.Bool(true)
	stringOff, stringOn := ldvalue.String("fallback"), ldvalue.String("hello-world")
	intOff, intOn := ldvalue.Int(0), ldvalue.Int(42)
	floatOff, floatOn := ldvalue.Float64(0.0), ldvalue.Float64(3.14)
	objectOff, objectOn := ldvalue.Null(), ldvalue.ObjectBuild().Set("key", ldvalue.String("value")).Build()

	return []ldstoretypes.KeyedItemDescriptor{
		flagItem("bool-flag", &boolOff, &boolOn),
		flagItem("string-flag", &stringOff, &stringOn),
		flagItem("int-flag", &intOff, &intOn),
		flagItem("float-flag", &floatOff, &floatOn),
		flagItem("object-flag", &objectOff, &objectOn),
	}
}

func buildTestManager(t *testing.T, cb circuitbreaking.CircuitBreaker) *FeatureFlagManager {
	t.Helper()

	cfg := &Config{SDKKey: t.Name()}

	ffm, err := NewFeatureFlagManager(
		cfg,
		http.DefaultClient,
		cb,
		WithConfigModifiers(func(config ld.Config) ld.Config {
			config.DataSource = &fakeLaunchDarklyDataSourceBuilder{}
			return config
		}),
	)
	must.NoError(t, err)
	must.NotNil(t, ffm)

	return ffm
}

func buildTestManagerWithFlags(t *testing.T, cb circuitbreaking.CircuitBreaker, flags []ldstoretypes.KeyedItemDescriptor) *FeatureFlagManager {
	t.Helper()

	ldConfig := ld.Config{
		DataSource: &testDataSourceBuilder{flags: flags},
		Events:     ldcomponents.NoEvents(),
	}

	client, err := ld.MakeCustomClient(t.Name(), ldConfig, 5*time.Second)
	must.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	// Use a unique domain per test to avoid global OpenFeature provider conflicts.
	domain := "test_" + strings.ReplaceAll(t.Name(), "/", "_")
	provider := ofld.NewProvider(client)
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
		ldClient: client,
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

		cfg := &Config{SDKKey: t.Name()}

		actual, err := NewFeatureFlagManager(
			cfg,
			http.DefaultClient,
			cbnoop.NewCircuitBreaker(),
			WithConfigModifiers(func(config ld.Config) ld.Config {
				config.DataSource = &fakeLaunchDarklyDataSourceBuilder{}
				return config
			}),
		)
		must.NoError(t, err)
		must.NotNil(t, actual)
	})

	T.Run("with missing http client", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{SDKKey: t.Name()}

		actual, err := NewFeatureFlagManager(cfg, nil, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, actual)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		actual, err := NewFeatureFlagManager(nil, http.DefaultClient, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, actual)
	})

	T.Run("with missing SDK key", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		actual, err := NewFeatureFlagManager(
			cfg,
			http.DefaultClient,
			cbnoop.NewCircuitBreaker(),
			WithConfigModifiers(func(config ld.Config) ld.Config {
				config.DataSource = &fakeLaunchDarklyDataSourceBuilder{}
				return config
			}),
		)
		must.Error(t, err)
		must.Nil(t, actual)
	})

	T.Run("with zero init timeout gets default", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{SDKKey: t.Name(), InitTimeout: 0}

		actual, err := NewFeatureFlagManager(
			cfg,
			http.DefaultClient,
			cbnoop.NewCircuitBreaker(),
			WithConfigModifiers(func(config ld.Config) ld.Config {
				config.DataSource = &fakeLaunchDarklyDataSourceBuilder{}
				return config
			}),
		)
		must.NoError(t, err)
		must.NotNil(t, actual)
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
			&Config{SDKKey: t.Name()},
			http.DefaultClient,
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
			&Config{SDKKey: t.Name()},
			http.DefaultClient,
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
			&Config{SDKKey: t.Name()},
			http.DefaultClient,
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
		ffm := buildTestManagerWithFlags(t, cbnoop.NewCircuitBreaker(), testFlagItems())
		obs := withRecordingObserver(ffm)

		result, err := ffm.CanUseFeature(ctx, "bool-flag", evalCtx("user123"))
		test.NoError(t, err)
		test.True(t, result)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "bool-flag",
		})
	})

	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManager(t, cb)
		obs := withRecordingObserver(ffm)

		result, err := ffm.CanUseFeature(ctx, "nonexistent-flag", evalCtx("user123"))
		test.ErrorIs(t, err, featureflags.ErrFlagNotFound)
		test.False(t, result)
		test.SliceLen(t, 1, cb.CanProceedCalls())

		// The provider answered, and answering is not what a failing service does.
		// Scoring this a failure is what let a flag name shipped ahead of its flag
		// open the breaker every other flag in the process shares.
		test.SliceLen(t, 0, cb.FailedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())

		// Even though evaluation failed, the values must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "nonexistent-flag",
		})
		must.SliceLen(t, 1, op.Errors)
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
		ffm := buildTestManagerWithFlags(t, cbnoop.NewCircuitBreaker(), testFlagItems())
		obs := withRecordingObserver(ffm)

		result, err := ffm.GetStringValue(ctx, "string-flag", "fallback", evalCtx("user123"))
		test.NoError(t, err)
		test.EqOp(t, "hello-world", result)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "string-flag",
		})
	})

	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManager(t, cb)

		result, err := ffm.GetStringValue(ctx, "nonexistent-flag", "fallback", evalCtx("user123"))
		test.ErrorIs(t, err, featureflags.ErrFlagNotFound)
		test.EqOp(t, "fallback", result)
		test.SliceLen(t, 1, cb.CanProceedCalls())
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

	// The counterweight to the "flag not found" cases: only a missing flag is
	// exempt from the breaker. Asking for a bool flag as a string resolves
	// TYPE_MISMATCH, which is a flag the provider cannot serve as asked, and that
	// still counts against it.
	T.Run("with a resolution failure that is not a missing flag", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManagerWithFlags(t, cb, testFlagItems())

		result, err := ffm.GetStringValue(ctx, "bool-flag", "fallback", evalCtx("user123"))
		test.Error(t, err)
		test.False(t, errors.Is(err, featureflags.ErrFlagNotFound))
		test.EqOp(t, "fallback", result)
		test.SliceLen(t, 1, cb.FailedCalls())
		test.SliceLen(t, 0, cb.SucceededCalls())
	})
}

func TestFeatureFlagManager_GetInt64Value(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		ffm := buildTestManagerWithFlags(t, cbnoop.NewCircuitBreaker(), testFlagItems())
		obs := withRecordingObserver(ffm)

		result, err := ffm.GetInt64Value(ctx, "int-flag", int64(0), evalCtx("user123"))
		test.NoError(t, err)
		test.EqOp(t, int64(42), result)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "int-flag",
		})
	})

	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManager(t, cb)

		result, err := ffm.GetInt64Value(ctx, "nonexistent-flag", int64(42), evalCtx("user123"))
		test.ErrorIs(t, err, featureflags.ErrFlagNotFound)
		test.EqOp(t, int64(42), result)
		test.SliceLen(t, 1, cb.CanProceedCalls())
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
		ffm := buildTestManagerWithFlags(t, cbnoop.NewCircuitBreaker(), testFlagItems())
		obs := withRecordingObserver(ffm)

		result, err := ffm.GetFloat64Value(ctx, "float-flag", 0.0, evalCtx("user123"))
		test.NoError(t, err)
		test.InDelta(t, 3.14, result, 1e-9)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "user123",
			"feature":      "float-flag",
		})
	})

	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManager(t, cb)

		result, err := ffm.GetFloat64Value(ctx, "nonexistent-flag", 3.14, evalCtx("user123"))
		test.ErrorIs(t, err, featureflags.ErrFlagNotFound)
		test.InDelta(t, 3.14, result, 1e-9)
		test.SliceLen(t, 1, cb.CanProceedCalls())
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
		ffm := buildTestManagerWithFlags(t, cbnoop.NewCircuitBreaker(), testFlagItems())
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

	T.Run("with flag not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CanProceedFunc: func() bool { return true },
			SucceededFunc:  func() {},
			FailedFunc:     func() {},
		}

		ffm := buildTestManager(t, cb)

		def := map[string]any{"k": "v"}
		result, err := ffm.GetObjectValue(ctx, "nonexistent-flag", def, evalCtx("user123"))
		test.ErrorIs(t, err, featureflags.ErrFlagNotFound)
		test.Eq[any](t, def, result)
		test.SliceLen(t, 1, cb.CanProceedCalls())
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
