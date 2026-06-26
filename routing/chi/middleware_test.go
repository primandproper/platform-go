package chi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/shoenig/test"
)

func TestBuildLoggingMiddleware(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		tracer := tracing.NewTracerForTest("")
		middleware := buildLoggingMiddleware(loggingnoop.NewLogger(), tracer, false)

		test.NotNil(t, middleware)

		hf := http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {})

		req, res := httptest.NewRequestWithContext(ctx, http.MethodPost, "/nil", http.NoBody), httptest.NewRecorder()

		middleware(hf).ServeHTTP(res, req)
	})
}
