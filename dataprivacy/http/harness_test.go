package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacymock "github.com/primandproper/platform-go/v14/dataprivacy/mock"
	"github.com/primandproper/platform-go/v14/errormappers"

	"github.com/primandproper/primitives-go/encoding"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/routing"
	"github.com/primandproper/primitives-go/routing/backends/chi"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test/must"
)

// subjectContextKey is where these tests put the subject, standing in for
// whatever a consumer's authentication middleware does.
type subjectContextKey struct{}

// subjectFromContext is the SubjectResolver the tests wire in.
func subjectFromContext(ctx context.Context) (dataprivacy.Subject, error) {
	subject, ok := ctx.Value(subjectContextKey{}).(dataprivacy.Subject)
	if !ok {
		return dataprivacy.Subject{}, platformerrors.New("no subject on the request")
	}

	return subject, nil
}

// asSubject wraps a handler so every request under it carries a subject.
func asSubject(subject dataprivacy.Subject, next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(res nethttp.ResponseWriter, req *nethttp.Request) {
		next.ServeHTTP(res, req.WithContext(context.WithValue(req.Context(), subjectContextKey{}, subject)))
	})
}

// mount builds a router with the whole surface on it, under one subject.
//
// It registers the module's mappers, because the statuses these tests assert are
// what the composition root's one call buys — see the package documentation.
// Registration is process-global, additive and idempotent in effect.
func mount(t *testing.T, svc dataprivacy.Service, subject dataprivacy.Subject, opts ...Option) nethttp.Handler {
	t.Helper()

	errormappers.Register()

	handlers, router := build(t, svc, opts...)

	handlers.Mount(router)

	must.NoError(t, router.Err())

	return asSubject(subject, router.Handler())
}

// build assembles the handlers and an empty router without mounting anything, for
// the tests that mount one route at a time.
func build(t *testing.T, svc dataprivacy.Service, opts ...Option) (*Handlers, *routing.Router) {
	t.Helper()

	backend := chi.NewBackend(&chi.Config{ServiceName: "dataprivacy-test"})
	router := routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	handlers, err := New(svc, append([]Option{WithSubjectResolver(subjectFromContext)}, opts...)...)
	must.NoError(t, err)

	return handlers, router
}

// do issues one request against a mounted handler.
func do(t *testing.T, handler nethttp.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, nethttp.NoBody)
	if body != "" {
		req = httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	return res
}

// requestFor builds a request row the mocks below answer with.
func requestFor(id string, subject dataprivacy.Subject, status dataprivacy.Status, operationID string) *dataprivacy.Request {
	now := time.Now().UTC()

	return &dataprivacy.Request{
		ID:          id,
		Type:        dataprivacy.RequestErasure,
		Subject:     subject,
		Status:      status,
		OperationID: operationID,
		CreatedAt:   now,
		DueAt:       now.Add(30 * 24 * time.Hour),
	}
}

// serviceReturning is a Service mock whose Get answers with one request.
func serviceReturning(req *dataprivacy.Request) *dataprivacymock.ServiceMock {
	svc := &dataprivacymock.ServiceMock{}

	svc.GetFunc = func(_ context.Context, _ *tenancy.Scope, id string) (*dataprivacy.Request, error) {
		if req != nil && req.ID == id {
			return req, nil
		}

		return nil, platformerrors.Wrapf(dataprivacy.ErrRequestNotFound, "dataprivacy request %q", id)
	}

	return svc
}
