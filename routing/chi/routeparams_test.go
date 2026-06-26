package chi

import (
	"context"
	"strconv"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/testutils"

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

	T.Run("with invalid value somehow", func(t *testing.T) {
		// NOTE: This will probably never happen in dev or production
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
