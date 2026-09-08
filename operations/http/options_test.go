package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v14/operations"
	operationsmock "github.com/primandproper/platform-go/v14/operations/mock"

	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("the defaults are a mounted base path and one tag", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		test.EqOp(t, BasePath, o.basePath)
		test.Eq(t, []string{"operations"}, o.tags)
		test.Nil(t, o.resolver)
		test.Nil(t, o.watcher)
	})

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		o := newOptions([]Option{
			WithOwnerResolver(Unscoped),
			WithBasePath("/jobs"),
			WithTags("jobs", "async"),
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
		})

		must.NotNil(t, o.resolver)
		test.EqOp(t, "/jobs", o.basePath)
		test.Eq(t, []string{"jobs", "async"}, o.tags)
		test.Eq(t, logger, o.logger)
		test.Eq(t, tracerProvider, o.tracerProvider)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil})

		test.EqOp(t, BasePath, o.basePath)
	})

	T.Run("an empty base path keeps the default rather than mounting at the root", func(t *testing.T) {
		t.Parallel()

		// A base path of "" would mount the listing on /, which is a surface
		// nobody asked for on top of whatever else the router serves there.
		o := newOptions([]Option{WithBasePath("")})

		test.EqOp(t, BasePath, o.basePath)
	})

	T.Run("no resolver is refused at construction", func(t *testing.T) {
		t.Parallel()

		// It has no default on purpose: "no scoping" and "not configured" must
		// not be the same wiring.
		handlers, err := New(&operationsmock.ServiceMock{})
		test.ErrorIs(t, err, ErrNilOwnerResolver)
		test.Nil(t, handlers)
	})

	T.Run("the tags reach the OpenAPI document", func(t *testing.T) {
		t.Parallel()

		// The option is only observable through the spec, which is the whole
		// reason it exists.
		handlers, err := New(&operationsmock.ServiceMock{},
			WithOwnerResolver(Unscoped),
			WithTags("jobs"))
		must.NoError(t, err)

		test.Eq(t, []string{"jobs"}, handlers.tags)
	})
}

func TestFilterFrom(T *testing.T) {
	T.Parallel()

	T.Run("no parameters is the default page", func(t *testing.T) {
		t.Parallel()

		filter := filterFrom(listInput{})

		must.NotNil(t, filter)
		test.Nil(t, filter.Cursor)
	})

	T.Run("a cursor is carried through", func(t *testing.T) {
		t.Parallel()

		filter := filterFrom(listInput{Cursor: "op-42"})

		must.NotNil(t, filter.Cursor)
		test.EqOp(t, "op-42", *filter.Cursor)
	})

	T.Run("a limit is carried through", func(t *testing.T) {
		t.Parallel()

		filter := filterFrom(listInput{Limit: 5})

		must.NotNil(t, filter.MaxResponseSize)
		test.EqOp(t, uint16(5), *filter.MaxResponseSize)
	})

	T.Run("a zero limit leaves the default page size alone", func(t *testing.T) {
		t.Parallel()

		// Absent and zero are the same thing on a query parameter, so a zero
		// must not become a request for no rows.
		filter := filterFrom(listInput{Limit: 0})

		must.NotNil(t, filter.MaxResponseSize)
		test.NotEqOp(t, uint16(0), *filter.MaxResponseSize)
	})
}

func TestHandlers_listPaging(T *testing.T) {
	T.Parallel()

	T.Run("the cursor and limit reach the service", func(t *testing.T) {
		t.Parallel()

		svc := &operationsmock.ServiceMock{}

		var seen *filtering.QueryFilter

		svc.ListFunc = func(
			_ context.Context,
			_ *operations.ListScope,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[operations.Operation], error) {
			seen = filter

			return filtering.NewQueryFilteredResult(
				[]*operations.Operation{}, 0, 0,
				func(o *operations.Operation) string { return o.ID }, filter,
			), nil
		}

		handler := mount(t, svc, "u1")

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(),
			nethttp.MethodGet, "/operations?cursor=op-42&limit=5", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusOK, res.Code)
		must.NotNil(t, seen)
		must.NotNil(t, seen.Cursor)
		test.EqOp(t, "op-42", *seen.Cursor)
		must.NotNil(t, seen.MaxResponseSize)
		test.EqOp(t, uint16(5), *seen.MaxResponseSize)
	})
}
