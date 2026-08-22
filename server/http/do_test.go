package http

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/healthcheck"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/routing"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterHTTPServer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, Config{Port: 8080, StartupDeadline: time.Second})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, (*routing.Router)(nil))
		do.ProvideValue(i, tracingnoop.NewTracerProvider())

		RegisterHTTPServer(i, "test_service")

		srv, err := do.Invoke[Server](i)
		must.NoError(t, err)
		test.NotNil(t, srv)
	})

	T.Run("serves the probes from the registry the container carries", func(t *testing.T) {
		t.Parallel()

		registry := newHealthRegistry(t)
		registry.Register(&stubChecker{name: "database"})

		router := testRouter(t)

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, Config{Port: 8080, StartupDeadline: time.Second})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, router)
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[healthcheck.Registry](i, registry)

		RegisterHTTPServer(i, "test_service")

		_, err := do.Invoke[Server](i)
		must.NoError(t, err)

		test.EqOp(t, http.StatusOK, serve(t, router.Handler(), ReadinessPath).Code)
		test.EqOp(t, http.StatusOK, serve(t, router.Handler(), VersionPath).Code)
	})

	T.Run("a container with no registry still wires up", func(t *testing.T) {
		t.Parallel()

		// Absent is not an error: this bridge is usable from a container the
		// composition root never touched.
		router := testRouter(t)

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, Config{Port: 8080, StartupDeadline: time.Second})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, router)
		do.ProvideValue(i, tracingnoop.NewTracerProvider())

		RegisterHTTPServer(i, "test_service")

		_, err := do.Invoke[Server](i)
		must.NoError(t, err)

		test.EqOp(t, http.StatusNotFound, serve(t, router.Handler(), ReadinessPath).Code)
	})

	T.Run("a registry that cannot be built is an error", func(t *testing.T) {
		t.Parallel()

		// The other half of optional: a service meant to report its health must
		// not come up silently unable to.
		errBuild := errors.New("building the registry")

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, Config{Port: 8080, StartupDeadline: time.Second})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, testRouter(t))
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.Provide(i, func(do.Injector) (healthcheck.Registry, error) { return nil, errBuild })

		RegisterHTTPServer(i, "test_service")

		_, err := do.Invoke[Server](i)
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
	})
}
