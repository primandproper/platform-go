package grpc

import (
	"errors"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

var errBoom = errors.New("boom")

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithLogger attaches a logger", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(testRequirements(t), grantsOf(permRead),
			WithLogger(loggingnoop.NewLogger()))

		must.NoError(t, err)
		test.NotNil(t, e.logger)
	})

	T.Run("WithMetricsProvider attaches counters", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(testRequirements(t), grantsOf(permRead),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()))

		must.NoError(t, err)
		test.NotNil(t, e.checksCounter)
		test.NotNil(t, e.denialsCounter)
		test.NotNil(t, e.undeclaredCounter)
		test.NotNil(t, e.noGrantsCounter)
	})

	// Audit-only announces itself at construction, which is the log line an
	// operator greps for to confirm enforcement really is off.
	T.Run("WithAuditOnly records the mode", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(testRequirements(t), grantsOf(permRead),
			WithAuditOnly(), WithLogger(loggingnoop.NewLogger()))

		must.NoError(t, err)
		test.True(t, e.auditOnly)
	})

	T.Run("a nil logger is replaced rather than dereferenced", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(testRequirements(t), grantsOf(permRead), WithLogger(nil))

		must.NoError(t, err)
		test.NotNil(t, e.logger)
	})
}

// Failing each counter in turn also proves none of them is silently skipped.
func TestNewEnforcer_MetricsFailure(T *testing.T) {
	T.Parallel()

	for _, failOn := range []string{
		serviceName + "_grpc_checks",
		serviceName + "_grpc_denials",
		serviceName + "_grpc_undeclared_methods",
		serviceName + "_grpc_missing_grants",
	} {
		T.Run("surfaces a failure creating "+failOn, func(t *testing.T) {
			t.Parallel()

			mp := &metricsmock.ProviderMock{
				NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
					if name == failOn {
						return nil, errBoom
					}

					return &metricsmock.Int64CounterMock{}, nil
				},
			}

			_, err := NewEnforcer(testRequirements(t), grantsOf(permRead), WithMetricsProvider(mp))

			test.ErrorIs(t, err, errBoom)
		})
	}
}

func TestDeniedError(T *testing.T) {
	T.Parallel()

	T.Run("carries a constant message", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "permission denied", errDenied.Error())
	})

	T.Run("unwraps to the platform sentinel", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, errors.Unwrap(errDenied))
	})
}

func TestRequirementsBuilder_PublicEdgeCases(T *testing.T) {
	T.Parallel()

	T.Run("rejects an empty public method", func(t *testing.T) {
		t.Parallel()

		_, err := NewRequirements().Public("").Build()

		test.ErrorIs(t, err, ErrEmptyMethod)
	})

	T.Run("rejects a method declared public twice", func(t *testing.T) {
		t.Parallel()

		_, err := NewRequirements().Public(methodHealth).Public(methodHealth).Build()

		test.ErrorIs(t, err, ErrDuplicateMethod)
	})

	// Methods is what a consumer uses to assert its table covers every
	// registered RPC, so it must report both kinds of declaration.
	T.Run("Methods reports required and public alike", func(t *testing.T) {
		t.Parallel()

		reqs, err := NewRequirements().
			Require(methodWrite, permWrite).
			Public(methodHealth).
			Build()
		must.NoError(t, err)

		test.Eq(t, []string{methodHealth, methodWrite}, reqs.Methods())
	})

	T.Run("RequireAll with an empty map is a no-op", func(t *testing.T) {
		t.Parallel()

		reqs, err := NewRequirements().RequireAll(nil).Build()

		must.NoError(t, err)
		test.SliceEmpty(t, reqs.Methods())
	})
}
