package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/healthcheck"
	messagequeuecfg "github.com/primandproper/platform-go/v13/messagequeue/config"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"
	routingcfg "github.com/primandproper/platform-go/v13/routing/config"
	httpserver "github.com/primandproper/platform-go/v13/server/http"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubChecker reports whatever it was built to report.
type stubChecker struct {
	err  error
	name string
}

func (c *stubChecker) Name() string                  { return c.name }
func (c *stubChecker) Check(_ context.Context) error { return c.err }

// componentNames returns what a registry checks, which is the whole of what the
// auto-wiring is asserting.
func componentNames(t *testing.T, registry healthcheck.Registry) []string {
	t.Helper()

	result := registry.CheckAll(t.Context())

	names := make([]string, 0, len(result.Components))
	for name := range result.Components {
		names = append(names, name)
	}

	return names
}

func Test_registerHealth(T *testing.T) {
	T.Parallel()

	T.Run("a service made of nothing checks nothing", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example"}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		registry, err := do.Invoke[healthcheck.Registry](newInjector(t, cfg))
		must.NoError(t, err)

		// Not an error and not a lie: a process with no dependencies is ready as
		// soon as it is up.
		test.SliceEmpty(t, componentNames(t, registry))
	})

	T.Run("wraps the infrastructure the config named", func(t *testing.T) {
		t.Parallel()

		// The point of the whole feature: nothing here asked for a health
		// check, and the registry has one per component anyway.
		queue := noopQueue()
		cfg := &Config{
			Name:         "example",
			Database:     sqliteConfig(t),
			MessageQueue: &queue,
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		registry, err := do.Invoke[healthcheck.Registry](newInjector(t, cfg))
		must.NoError(t, err)

		test.SliceContainsAll(t, []string{databaseCheckerName, messageQueueCheckerName}, componentNames(t, registry))
	})

	T.Run("checks the components rather than remembering them", func(t *testing.T) {
		t.Parallel()

		// A registry that recorded a status at build time would report a
		// database that has since died as up forever.
		cfg := &Config{Name: "example", Database: sqliteConfig(t)}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		registry, err := do.Invoke[healthcheck.Registry](i)
		must.NoError(t, err)

		must.EqOp(t, healthcheck.StatusUp, registry.CheckAll(t.Context()).Status)

		must.NoError(t, do.MustInvoke[database.Client](i).Close())

		test.EqOp(t, healthcheck.StatusDown, registry.CheckAll(t.Context()).Status)
	})

	T.Run("a subsystem that cannot be built is an error", func(t *testing.T) {
		t.Parallel()

		// Absence is fine — nobody configured it. A component that was
		// configured and cannot be built must not become a service that reports
		// healthy because its check could not be constructed.
		cfg := &Config{
			Name:         "example",
			MessageQueue: &messagequeuecfg.Config{Publisher: messagequeuecfg.MessageQueueConfig{Provider: "nonexistent"}},
		}

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		Register(i, cfg)

		_, err := do.Invoke[healthcheck.Registry](i)
		test.Error(t, err)
	})
}

func Test_adopt(T *testing.T) {
	T.Parallel()

	T.Run("a component nobody registered contributes nothing", func(t *testing.T) {
		t.Parallel()

		registry := newHealthRegistry(t)

		must.NoError(t, adopt(do.New(), registry, func(string) healthcheck.Checker {
			t.Error("wrap was called for a component that was never registered")

			return nil
		}))

		test.SliceEmpty(t, componentNames(t, registry))
	})

	T.Run("a component that cannot be asked contributes nothing", func(t *testing.T) {
		t.Parallel()

		// The database client's IsReady is an optional capability, so a client
		// without one is left unchecked rather than reported down.
		i := do.New()
		do.ProvideValue(i, "present")

		registry := newHealthRegistry(t)

		must.NoError(t, adopt(i, registry, func(string) healthcheck.Checker { return nil }))

		test.SliceEmpty(t, componentNames(t, registry))
	})

	T.Run("a component that fails to build is reported", func(t *testing.T) {
		t.Parallel()

		errBuild := platformerrors.New("building the component")

		i := do.New()
		do.Provide(i, func(do.Injector) (string, error) { return "", errBuild })

		err := adopt(i, newHealthRegistry(t), func(string) healthcheck.Checker { return nil })
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
	})
}

func TestWithHealthChecks(T *testing.T) {
	T.Parallel()

	T.Run("joins the application's checks to the registry", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example", Database: sqliteConfig(t)}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		_, err := New(i, WithHealthChecks(&stubChecker{name: "recipes_api"}, nil))
		must.NoError(t, err)

		registry, err := do.Invoke[healthcheck.Registry](i)
		must.NoError(t, err)

		// Alongside the auto-wired one, not instead of it.
		test.SliceContainsAll(t, []string{databaseCheckerName, "recipes_api"}, componentNames(t, registry))
	})

	T.Run("checks handed to an injector with no registry are an error", func(t *testing.T) {
		t.Parallel()

		// Dropping them silently would leave the service reporting ready
		// without ever running the checks it was given.
		cfg := &Config{Name: "example"}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, cfg)
		do.ProvideValue(i, &cfg.Observability)

		_, err := New(i, WithHealthChecks(&stubChecker{name: "recipes_api"}))
		test.Error(t, err)
	})

	T.Run("what a service is made of is what its probe reports", func(t *testing.T) {
		t.Parallel()

		// The end of the whole path: a config names a database and an HTTP
		// server, nothing in it mentions health, and the server answers /readyz
		// with the database's status and the application's own check beside it.
		queue := noopQueue()
		cfg := &Config{
			Name:         "example",
			Database:     sqliteConfig(t),
			MessageQueue: &queue,
			Encoding:     &encoding.Config{ContentType: string(encoding.ContentTypeJSON)},
			Routing:      &routingcfg.Config{Provider: routingcfg.ProviderChi, Chi: &chi.Config{ServiceName: "example"}},
			HTTPServer:   &httpserver.Config{Port: 8080, StartupDeadline: time.Second},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		svc, err := New(i, WithHealthChecks(&stubChecker{name: "recipes_api"}))
		must.NoError(t, err)
		must.SliceLen(t, 1, svc.servers)

		srv, err := do.Invoke[httpserver.Server](i)
		must.NoError(t, err)

		res := httptest.NewRecorder()
		srv.Router().Handler().ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), http.MethodGet, httpserver.ReadinessPath, http.NoBody))

		must.EqOp(t, http.StatusOK, res.Code)

		var result healthcheck.Result
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &result))

		test.EqOp(t, healthcheck.StatusUp, result.Status)
		test.MapContainsKeys(t, result.Components, []string{databaseCheckerName, messageQueueCheckerName, "recipes_api"})
	})

	T.Run("no checks needs no registry", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example"}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, cfg)
		do.ProvideValue(i, &cfg.Observability)

		_, err := New(i)
		test.NoError(t, err)
	})
}

// newHealthRegistry builds an uninstrumented health registry for a test.
func newHealthRegistry(t *testing.T) *healthcheck.CheckerRegistry {
	t.Helper()

	registry, err := healthcheck.NewRegistry()
	must.NoError(t, err)

	return registry
}
