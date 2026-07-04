package http

import (
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"
	"github.com/primandproper/platform-go/v3/routing"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterHTTPServer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, Config{Port: 8080, StartupDeadline: time.Second})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue[routing.Router](i, nil)
		do.ProvideValue(i, tracingnoop.NewTracerProvider())

		RegisterHTTPServer(i, "test_service")

		srv, err := do.Invoke[Server](i)
		must.NoError(t, err)
		test.NotNil(t, srv)
	})
}
