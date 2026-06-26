package chi

import (
	"context"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/identifiers"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRequestIDFunc(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		expected := identifiers.New()
		ctx := context.WithValue(t.Context(), chimiddleware.RequestIDKey, expected)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", http.NoBody)
		must.NoError(t, err)

		actual := RequestIDFunc(req)
		test.EqOp(t, expected, actual)
	})
}
