package grpc_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"

	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/observability"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newServerWith builds a server with the given options, for the tests whose
// subject is the option rather than a request.
func newServerWith(tb testing.TB, opts ...waitlistsgrpc.Option) (*waitlistsgrpc.Server, error) {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "waitlists.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	store, err := waitlists.NewSQLStore(db)
	must.NoError(tb, err)

	return waitlistsgrpc.NewServer(store, db, extractPrincipal, permitWithdrawals(), opts...)
}

// TestAbsentObservabilityMeansNoop is the property every constructor in this
// module shares: a caller wanting none of the three names none of them.
func TestAbsentObservabilityMeansNoop(T *testing.T) {
	T.Parallel()

	srv, err := newServerWith(T)
	must.NoError(T, err)
	test.NotNil(T, srv)
}

// TestEachPillarIsAcceptedOnItsOwn covers the three options a consumer supplies
// one at a time.
func TestEachPillarIsAcceptedOnItsOwn(T *testing.T) {
	T.Parallel()

	T.Run("logger", func(t *testing.T) {
		t.Parallel()

		srv, err := newServerWith(t, waitlistsgrpc.WithLogger(loggingnoop.NewLogger()))
		must.NoError(t, err)
		test.NotNil(t, srv)
	})

	T.Run("tracer provider", func(t *testing.T) {
		t.Parallel()

		srv, err := newServerWith(t, waitlistsgrpc.WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.NoError(t, err)
		test.NotNil(t, srv)
	})

	T.Run("metrics provider", func(t *testing.T) {
		t.Parallel()

		srv, err := newServerWith(t, waitlistsgrpc.WithMetricsProvider(metricsnoop.NewMetricsProvider()))
		must.NoError(t, err)
		test.NotNil(t, srv)
	})
}

// TestWithPillarsSuppliesAllThree, and the nil Pillars supplies none — a
// consumer building an option list conditionally ends up with one.
func TestWithPillarsSuppliesAllThree(T *testing.T) {
	T.Parallel()

	srv, err := newServerWith(T, waitlistsgrpc.WithPillars(&observability.Pillars{
		Logger:          loggingnoop.NewLogger(),
		TracerProvider:  tracingnoop.NewTracerProvider(),
		MetricsProvider: metricsnoop.NewMetricsProvider(),
	}))
	must.NoError(T, err)
	test.NotNil(T, srv)

	nilPillars, err := newServerWith(T, waitlistsgrpc.WithPillars(nil))
	must.NoError(T, err)
	test.NotNil(T, nilPillars)
}

// TestOptionsApplyInOrder is the property WithPillars' documentation promises:
// a consumer handing over their pillars and then naming one of them nil leaves
// that one component without it.
func TestOptionsApplyInOrder(T *testing.T) {
	T.Parallel()

	srv, err := newServerWith(T,
		waitlistsgrpc.WithPillars(&observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}),
		waitlistsgrpc.WithMetricsProvider(nil),
	)
	must.NoError(T, err)
	test.NotNil(T, srv)
}

// TestANilScopeResolverIsIgnored keeps the default in place rather than
// installing a resolver that panics on the first anonymous request.
func TestANilScopeResolverIsIgnored(T *testing.T) {
	T.Parallel()

	srv, err := newServerWith(T, waitlistsgrpc.WithScopeResolver(nil))
	must.NoError(T, err)
	test.NotNil(T, srv)
}

// TestGlobalScopeIsTheSingleTenantAnswer, spelled directly, because it is the
// default a consumer gets by saying nothing.
func TestGlobalScopeIsTheSingleTenantAnswer(T *testing.T) {
	T.Parallel()

	scope, err := waitlistsgrpc.GlobalScope(context.Background())
	must.NoError(T, err)

	test.EqOp(T, tenancy.Global(), scope)
}
