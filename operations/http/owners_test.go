package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v14/operations"
	operationsmock "github.com/primandproper/platform-go/v14/operations/mock"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The two owners a signed-in person holds in a deployment where dataprivacy
// starts operations under the person and the application under the tenant.
var (
	tenantOwner = tenancy.Of("tenant_1")
	personOwner = tenancy.Of("person_1")
)

func ownersOf(owners ...tenancy.Scope) OwnersResolver {
	return func(context.Context) ([]tenancy.Scope, error) { return owners, nil }
}

func notFound(id string) error {
	return platformerrors.Wrapf(operations.ErrOperationNotFound, "operation %q", id)
}

func TestNew_ownersResolver(T *testing.T) {
	T.Parallel()

	T.Run("satisfies the resolver requirement on its own", func(t *testing.T) {
		t.Parallel()

		handlers, err := New(&operationsmock.ServiceMock{}, WithOwnersResolver(ownersOf(tenantOwner)))
		must.NoError(t, err)
		must.NotNil(t, handlers)
	})
}

func TestHandlers_severalOwners(T *testing.T) {
	T.Parallel()

	T.Run("an operation is read under whichever owner holds it", func(t *testing.T) {
		t.Parallel()

		op := &operations.Operation{ID: "op1", Owner: personOwner, State: operations.StateRunning}
		handler := mount(t, serviceReturning(op), tenantOwner, WithOwnersResolver(ownersOf(tenantOwner, personOwner)))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/operations/op1", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.StrContains(t, res.Body.String(), `"id":"op1"`)
	})

	// The positive control's other half: holding two owners widens a read to
	// those two and to nobody else's.
	T.Run("an operation neither owner holds is absent", func(t *testing.T) {
		t.Parallel()

		op := &operations.Operation{ID: "op1", Owner: tenancy.Of("somebody_else"), State: operations.StateRunning}
		handler := mount(t, serviceReturning(op), tenantOwner, WithOwnersResolver(ownersOf(tenantOwner, personOwner)))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/operations/op1", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
	})

	T.Run("a failure that is not an absence is not masked by a later owner", func(t *testing.T) {
		t.Parallel()

		svc := &operationsmock.ServiceMock{}

		var asked []tenancy.Scope

		svc.GetFunc = func(_ context.Context, scope tenancy.Scope, _ string) (*operations.Operation, error) {
			asked = append(asked, scope)

			return nil, platformerrors.New("the database is down")
		}

		handler := mount(t, svc, tenantOwner, WithOwnersResolver(ownersOf(tenantOwner, personOwner)))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/operations/op1", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.Eq(t, []tenancy.Scope{tenantOwner}, asked)
	})

	// Cancel still reads nothing first: the owner that holds the operation is
	// the one whose Cancel finds it, and Get is never asked.
	T.Run("a cancellation is made under the owner that holds the operation", func(t *testing.T) {
		t.Parallel()

		svc := &operationsmock.ServiceMock{}

		svc.CancelFunc = func(_ context.Context, scope tenancy.Scope, id string) (*operations.Operation, error) {
			if scope != personOwner {
				return nil, notFound(id)
			}

			return &operations.Operation{ID: id, Owner: scope, State: operations.StateCancelled}, nil
		}

		handler := mount(t, svc, tenantOwner, WithOwnersResolver(ownersOf(tenantOwner, personOwner)))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodPost, "/operations/op1/cancel", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.SliceLen(t, 2, svc.CancelCalls())
		test.SliceEmpty(t, svc.GetCalls())
	})

	T.Run("a listing is every owner's operations, in identifier order, one page long", func(t *testing.T) {
		t.Parallel()

		byOwner := map[tenancy.Scope][]*operations.Operation{
			tenantOwner: {{ID: "a1", Owner: tenantOwner}, {ID: "c3", Owner: tenantOwner}},
			personOwner: {{ID: "b2", Owner: personOwner}},
		}

		svc := &operationsmock.ServiceMock{}
		svc.ListFunc = func(
			_ context.Context,
			scope tenancy.Scope,
			_ *operations.ListScope,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[operations.Operation], error) {
			rows := byOwner[scope]

			return filtering.NewQueryFilteredResult(rows, uint64(len(rows)), uint64(len(rows)),
				func(o *operations.Operation) string { return o.ID }, filter), nil
		}

		handler := mount(t, svc, tenantOwner, WithOwnersResolver(ownersOf(tenantOwner, personOwner)))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/operations?limit=2", nethttp.NoBody))
		must.EqOp(t, nethttp.StatusOK, res.Code)

		var body struct {
			Data struct {
				Cursor string `json:"cursor"`
				Data   []struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"data"`
		}
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))

		ids := make([]string, 0, len(body.Data.Data))
		for _, op := range body.Data.Data {
			ids = append(ids, op.ID)
		}

		// Both owners contribute, the union is in identifier order, and the
		// page is cut at its size with the cursor on the last row emitted —
		// not c3, which was fetched and dropped and must be the next page's.
		test.Eq(t, []string{"a1", "b2"}, ids)
		test.EqOp(t, "b2", body.Data.Cursor)
	})

	T.Run("an owners resolver that names nobody is refused before anything is read", func(t *testing.T) {
		t.Parallel()

		svc := &operationsmock.ServiceMock{}
		handler := mount(t, svc, tenantOwner, WithOwnersResolver(ownersOf()))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/operations/op1", nethttp.NoBody))

		test.NotEqOp(t, nethttp.StatusOK, res.Code)
		test.SliceEmpty(t, svc.GetCalls())
	})
}
