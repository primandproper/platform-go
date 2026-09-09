package http_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/primandproper/platform-go/v14/uploads/registry"
	registryhttp "github.com/primandproper/platform-go/v14/uploads/registry/http"
	registrymock "github.com/primandproper/platform-go/v14/uploads/registry/mock"

	"github.com/primandproper/primitives-go/database"
	databasemock "github.com/primandproper/primitives-go/database/mock"
	"github.com/primandproper/primitives-go/encoding"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/routing"
	"github.com/primandproper/primitives-go/routing/backends/chi"
	"github.com/primandproper/primitives-go/tenancy"
	"github.com/primandproper/primitives-go/uploads"
)

// callerKey is where this example's authentication middleware leaves the
// caller. A real one reads it off a session or a token's subject.
type callerKey struct{}

// callerFromContext is the CallerResolver the handler is wired with. It is the
// one thing this package cannot default: the guard is two facts about whoever
// is asking, and a default would have to invent both.
func callerFromContext(ctx context.Context) (registryhttp.Caller, error) {
	caller, ok := ctx.Value(callerKey{}).(registryhttp.Caller)
	if !ok {
		return registryhttp.Caller{}, platformerrors.New("no caller on the request")
	}

	return caller, nil
}

// The guarded serve: one route, and a caller who is checked against the row
// rather than against knowledge of the object's key.
func Example() {
	scope, owner := tenancy.Of("tenant_1"), "user_1"

	store := &registrymock.StoreMock{}
	store.GetObjectFunc = func(
		_ context.Context,
		_ database.SQLQueryExecutor,
		readScope tenancy.Scope,
		objectID string,
	) (*registry.Object, error) {
		// The real store binds the scope into the statement, so a row in
		// another tenant is absent rather than refused. This says the same.
		if objectID != "object_1" || readScope != scope {
			return nil, registry.ErrObjectNotFound
		}

		return &registry.Object{
			ID:          "object_1",
			Key:         "receipts/acme/invoice.pdf",
			ContentType: "application/pdf",
			OwnerID:     owner,
			Scope:       scope,
			Size:        13,
		}, nil
	}

	client := &databasemock.ClientMock{}
	client.ReaderFunc = func() database.SQLQueryExecutor { return nil }

	handler, err := registryhttp.New(store, client, exampleBucket(),
		registryhttp.WithCallerResolver(callerFromContext))
	if err != nil {
		panic(err)
	}

	router := routing.New(
		chi.NewBackend(&chi.Config{ServiceName: "example"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON),
	)

	handler.Mount(router)

	serve := func(caller registryhttp.Caller) (int, string) {
		ctx := context.WithValue(context.Background(), callerKey{}, caller)
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, handler.ObjectPath("object_1"), http.NoBody)

		res := httptest.NewRecorder()
		router.Handler().ServeHTTP(res, req)

		return res.Code, res.Header().Get("Content-Type")
	}

	// The owner, in their own tenant.
	status, contentType := serve(registryhttp.Caller{Scope: scope, PrincipalID: owner})
	fmt.Println("owner:", status, contentType)

	// Somebody else in the same tenant, holding the same URL.
	status, _ = serve(registryhttp.Caller{Scope: scope, PrincipalID: "user_2"})
	fmt.Println("another principal:", status)

	// The owner, in the wrong tenant. The same answer, on purpose: a 403 for an
	// object that exists beside a 404 for one that does not is an oracle.
	status, _ = serve(registryhttp.Caller{Scope: tenancy.Of("tenant_2"), PrincipalID: owner})
	fmt.Println("another tenant:", status)

	// Output:
	// owner: 200 application/pdf
	// another principal: 404
	// another tenant: 404
}

// exampleBucket stands in for objectstorage.Uploader, which is what a service
// actually hands this — and which implements uploads.RangeReader, so a real
// deployment serves Range requests against a video or a PDF.
func exampleBucket() uploads.UploadManager { return bucket{} }

type bucket struct{}

func (bucket) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("invoice bytes"))), nil
}

func (bucket) Save(context.Context, string, io.Reader, ...uploads.SaveOption) error { return nil }

func (bucket) Delete(context.Context, string) error { return nil }

func (bucket) Exists(context.Context, string) (bool, error) { return true, nil }

func (bucket) Close() error { return nil }
