package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/observability"

	"github.com/shoenig/test"
)

func TestLogging(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		obs := observability.NewRecordingObserver()
		middleware := Logging(obs, false)

		test.NotNil(t, middleware)

		hf := http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {})

		req, res := httptest.NewRequestWithContext(ctx, http.MethodPost, "/nil", http.NoBody), httptest.NewRecorder()

		middleware(hf).ServeHTTP(res, req)

		// a span was opened (and ended) for the non-health-check request.
		test.SliceLen(t, 1, obs.Operations)
		test.True(t, obs.Operations[0].Ended)
	})

	T.Run("does not open a span for health checks", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		obs := observability.NewRecordingObserver()
		middleware := Logging(obs, false)

		test.NotNil(t, middleware)

		hf := http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {})

		req, res := httptest.NewRequestWithContext(ctx, http.MethodGet, "/_ops_/live", http.NoBody), httptest.NewRecorder()

		middleware(hf).ServeHTTP(res, req)

		test.SliceLen(t, 0, obs.Operations)
	})
}
