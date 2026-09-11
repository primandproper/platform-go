package authserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/primandproper/primitives-go/v2/observability"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// pillars is one fully populated set, so a test asserting that all three
// arrived is not also asserting which three somebody wrote down.
func pillars() *observability.Pillars {
	return &observability.Pillars{
		Logger:          loggingnoop.NewLogger(),
		TracerProvider:  tracingnoop.NewTracerProvider(),
		MetricsProvider: metricsnoop.NewMetricsProvider(),
	}
}

// The three constructors in this package keep their options in one
// observabilityOptions rather than in three sets of fields, so these run the
// same assertions against each of the three option types. Written as a table
// over the three because the failure worth catching is one of them being wired
// differently from the other two — which is exactly what three separate suites
// would hide.
func TestObservabilityOptions(T *testing.T) {
	T.Parallel()

	// Each entry applies its own package's options to its own type and reports
	// what landed in that type's observabilityOptions.
	// Each entry says what one type's observabilityOptions holds after the four
	// wirings below, so the assertions are written once and the answers come from
	// three different option types.
	for name, apply := range map[string]struct {
		separately     func(*observability.Pillars) observabilityOptions
		atOnce         func(*observability.Pillars) observabilityOptions
		nilPillars     func() observabilityOptions
		pillarsThenNil func(*observability.Pillars) observabilityOptions
	}{
		"authenticator": {
			separately: func(p *observability.Pillars) observabilityOptions {
				a := &Authenticator{}
				WithLogger(p.Logger)(a)
				WithTracerProvider(p.TracerProvider)(a)
				WithMetricsProvider(p.MetricsProvider)(a)

				return a.opts
			},
			atOnce: func(p *observability.Pillars) observabilityOptions {
				a := &Authenticator{}
				WithPillars(p)(a)

				return a.opts
			},
			nilPillars: func() observabilityOptions {
				a := &Authenticator{}
				WithPillars(nil)(a)

				return a.opts
			},
			pillarsThenNil: func(p *observability.Pillars) observabilityOptions {
				a := &Authenticator{}
				WithPillars(p)(a)
				WithMetricsProvider(nil)(a)

				return a.opts
			},
		},
		"guarded resolver": {
			separately: func(p *observability.Pillars) observabilityOptions {
				r := &GuardedResolver{}
				WithResolverLogger(p.Logger)(r)
				WithResolverTracerProvider(p.TracerProvider)(r)
				WithResolverMetricsProvider(p.MetricsProvider)(r)

				return r.opts
			},
			atOnce: func(p *observability.Pillars) observabilityOptions {
				r := &GuardedResolver{}
				WithResolverPillars(p)(r)

				return r.opts
			},
			nilPillars: func() observabilityOptions {
				r := &GuardedResolver{}
				WithResolverPillars(nil)(r)

				return r.opts
			},
			pillarsThenNil: func(p *observability.Pillars) observabilityOptions {
				r := &GuardedResolver{}
				WithResolverPillars(p)(r)
				WithResolverMetricsProvider(nil)(r)

				return r.opts
			},
		},
		"store decorator": {
			separately: func(p *observability.Pillars) observabilityOptions {
				s := &Store{}
				WithStoreLogger(p.Logger)(s)
				WithStoreTracerProvider(p.TracerProvider)(s)
				WithStoreMetricsProvider(p.MetricsProvider)(s)

				return s.opts
			},
			atOnce: func(p *observability.Pillars) observabilityOptions {
				s := &Store{}
				WithStorePillars(p)(s)

				return s.opts
			},
			nilPillars: func() observabilityOptions {
				s := &Store{}
				WithStorePillars(nil)(s)

				return s.opts
			},
			pillarsThenNil: func(p *observability.Pillars) observabilityOptions {
				s := &Store{}
				WithStorePillars(p)(s)
				WithStoreMetricsProvider(nil)(s)

				return s.opts
			},
		},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			p := pillars()

			t.Run("each option sets the pillar it names", func(t *testing.T) {
				t.Parallel()

				opts := apply.separately(p)

				test.Eq(t, p.Logger, opts.logger)
				test.Eq(t, p.TracerProvider, opts.tracerProvider)
				test.Eq(t, p.MetricsProvider, opts.metricsProvider)
			})

			t.Run("the pillars option supplies all three at once", func(t *testing.T) {
				t.Parallel()

				opts := apply.atOnce(p)

				test.Eq(t, p.Logger, opts.logger)
				test.Eq(t, p.TracerProvider, opts.tracerProvider)
				test.Eq(t, p.MetricsProvider, opts.metricsProvider)
			})

			t.Run("a nil Pillars attaches nothing", func(t *testing.T) {
				t.Parallel()

				// A consumer that built none hands over nil rather than
				// branching at the wiring site, and absent means noop: each
				// constructor resolves what it was not given through the Ensure*
				// helpers.
				opts := apply.nilPillars()

				test.Nil(t, opts.logger)
				test.Nil(t, opts.tracerProvider)
				test.Nil(t, opts.metricsProvider)
			})

			t.Run("a later option overrides what the pillars supplied", func(t *testing.T) {
				t.Parallel()

				// Options apply in order, which is what lets a caller hand over
				// their pillars and leave this one seam unmetered.
				opts := apply.pillarsThenNil(p)

				test.NotNil(t, opts.logger)
				test.NotNil(t, opts.tracerProvider)
				test.Nil(t, opts.metricsProvider)
			})
		})
	}
}

// TestGlobalScopeIsTheDefaultResolver pins the default and the reason it is
// defensible rather than a placeholder.
//
// A single-tenant deployment is entirely described by it, and so is a
// multi-tenant one whose registrations are all administered — Global() is the
// registry a client belonging to nobody in particular lives in. It is safe as a
// default only because the scope it returns is handed to
// signin.Service.LoginForToken before it reaches Client.Admits.
func TestGlobalScopeIsTheDefaultResolver(T *testing.T) {
	T.Parallel()

	scope, err := GlobalScope(T.Context(), nil)
	must.NoError(T, err)
	test.EqOp(T, tenancy.Global(), scope)

	// And it is what an authenticator built without WithScopeResolver holds.
	a := &Authenticator{scopes: GlobalScope}

	resolved, err := a.scopes(T.Context(), nil)
	must.NoError(T, err)
	test.EqOp(T, tenancy.Global(), resolved)
}

// TestAuthenticatorOptionsRefuseToUnsetThemselves is the shape three of this
// package's options share, and it is not decoration.
//
// A nil resolver would leave every request with no registry to sign in against,
// and an empty message would render a blank alert into a page. Both defaults are
// the safe answer, so the option leaves them in place rather than installing
// something worse than what it replaced.
func TestAuthenticatorOptionsRefuseToUnsetThemselves(T *testing.T) {
	T.Parallel()

	T.Run("a nil scope resolver leaves the default", func(t *testing.T) {
		t.Parallel()

		named := func(context.Context, *http.Request) (tenancy.Scope, error) {
			return tenancy.Of("tenant-a"), nil
		}

		a := &Authenticator{scopes: GlobalScope}
		WithScopeResolver(named)(a)

		resolved, err := a.scopes(t.Context(), nil)
		must.NoError(t, err)
		test.EqOp(t, tenancy.Of("tenant-a"), resolved)

		WithScopeResolver(nil)(a)

		stillNamed, err := a.scopes(t.Context(), nil)
		must.NoError(t, err)
		test.EqOp(t, tenancy.Of("tenant-a"), stillNamed)
	})

	T.Run("an empty mismatch message leaves the default", func(t *testing.T) {
		t.Parallel()

		a := &Authenticator{message: DefaultMismatchMessage}

		WithMismatchMessage("Ask the platform team.")(a)
		test.EqOp(t, "Ask the platform team.", a.message)

		WithMismatchMessage("")(a)
		test.EqOp(t, "Ask the platform team.", a.message)
	})
}

// TestWithAdministrativeLoginSendsTheSignInThroughTheOtherDoor pins the one
// option here that changes what the seam does rather than what it records.
//
// It is a deliberate setting rather than something inferred because there is no
// client-side check that fixes "anybody with an account may sign in here", and
// because a service that named no administrative roles has no administrative
// door — every sign-in through such an authenticator is then refused, which is a
// failure to have at construction rather than in production.
func TestWithAdministrativeLoginSendsTheSignInThroughTheOtherDoor(T *testing.T) {
	T.Parallel()

	a := &Authenticator{}
	test.False(T, a.administrative, test.Sprint("the ordinary door is the default"))

	WithAdministrativeLogin()(a)
	test.True(T, a.administrative)
}
