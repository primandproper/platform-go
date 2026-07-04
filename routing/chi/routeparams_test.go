package chi

import (
	"context"
	"strconv"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/testutils"

	"github.com/go-chi/chi/v5"
	"github.com/shoenig/test"
)

func TestNewRouteParamManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewRouteParamManager())
	})
}

func Test_BuildRouteParamIDFetcher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := &chiRouteParamManager{}

		ctx := t.Context()
		exampleKey := "blah"
		fn := r.BuildRouteParamIDFetcher(loggingnoop.NewLogger(), exampleKey, "thing")
		expected := uint64(123)
		req := testutils.BuildTestRequest(t).WithContext(
			context.WithValue(
				ctx,
				chi.RouteCtxKey,
				&chi.Context{
					URLParams: chi.RouteParams{
						Keys:   []string{exampleKey},
						Values: []string{strconv.FormatUint(expected, 10)},
					},
				},
			),
		)

		actual := fn(req)
		test.EqOp(t, expected, actual)
	})

	T.Run("with invalid value returns 0", func(t *testing.T) {
		// A non-numeric param genuinely happens (bad client input); it returns 0 and
		// the fetcher logs the parse failure rather than swallowing it.
		t.Parallel()

		r := &chiRouteParamManager{}

		ctx := t.Context()
		exampleKey := "blah"
		fn := r.BuildRouteParamIDFetcher(loggingnoop.NewLogger(), exampleKey, "thing")
		expected := uint64(0)

		req := testutils.BuildTestRequest(t)
		req = req.WithContext(
			context.WithValue(
				ctx,
				chi.RouteCtxKey,
				&chi.Context{
					URLParams: chi.RouteParams{
						Keys:   []string{exampleKey},
						Values: []string{"expected"},
					},
				},
			),
		)

		actual := fn(req)
		test.EqOp(t, expected, actual)
	})
}

func Test_BuildRouteParamStringIDFetcher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := &chiRouteParamManager{}

		ctx := t.Context()
		exampleKey := "blah"
		fn := r.BuildRouteParamStringIDFetcher(exampleKey)
		expectedInt := uint64(123)
		expected := strconv.FormatUint(expectedInt, 10)
		req := testutils.BuildTestRequest(t).WithContext(
			context.WithValue(
				ctx,
				chi.RouteCtxKey,
				&chi.Context{
					URLParams: chi.RouteParams{
						Keys:   []string{exampleKey},
						Values: []string{strconv.FormatUint(expectedInt, 10)},
					},
				},
			),
		)

		actual := fn(req)
		test.EqOp(t, expected, actual)
	})
}
