package routing_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"
)

// The input for creating a user. Tags decide where each field is bound:
//   - path:  taken from the URL, cross-checked against the {orgID:uint64} token
//   - query: taken from the query string
//   - json (no location tag): part of the request body
type newUserForm struct {
	Name   string `json:"name"`
	Email  string `json:"email"`
	OrgID  uint64 `path:"orgID"`
	Notify bool   `query:"notify"`
}

// The typed output. It is encoded into the response (enveloped by default).
type person struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	ID    uint64 `json:"id"`
}

// A typed handler: func(ctx, In) (Out, error). The framework decodes and
// validates In, calls this, then encodes Out — or maps a returned error to an
// HTTP status and error envelope.
func createPerson(_ context.Context, in newUserForm) (person, error) {
	return person{ID: in.OrgID*1000 + 1, Name: in.Name, Email: in.Email}, nil
}

func fetchPerson(_ context.Context, in struct {
	OrgID uint64 `path:"orgID"`
	ID    uint64 `path:"userID"`
}) (person, error) {
	return person{ID: in.ID, Name: "Ada"}, nil
}

// Example demonstrates wiring a Router over the chi backend, registering typed
// routes, and mounting the generated OpenAPI spec.
func Example() {
	// The backend is the swappable seam: chi today, gin/etc. tomorrow. It carries
	// the library-specific middleware + OpenTelemetry stack.
	backend := chi.NewBackend(&chi.Config{
		ServiceName: "example-service",
	})

	// The Router is the declarative, OpenAPI-generating layer on top of it.
	enc := encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)
	r := routing.New(backend, enc,
		routing.WithTitle("Users API"),
		routing.WithVersion("1.0.0"),
	)

	// Typed registration is done with the package-level generic functions.
	// Path params use an inline typed syntax: {orgID:uint64}.
	routing.Post(r, "/orgs/{orgID:uint64}/users", createPerson,
		routing.WithSummary("Create a user"),
		routing.WithTags("users"),
	)

	// Group applies a shared path prefix and default tags.
	r.Group("/orgs/{orgID:uint64}", func(sub *routing.Router) {
		routing.Get(sub, "/users/{userID:uint64}", fetchPerson, routing.WithSummary("Fetch a user"))
	}, "users")

	// Serve the generated OpenAPI 3 spec (and a docs UI) on the same router.
	r.MountOpenAPI("/openapi.json", "/docs")

	// Registration errors (if any) surface here; check before serving.
	if err := r.Err(); err != nil {
		panic(err)
	}

	// In a real service you would hand r.Handler() to an http.Server (or the
	// platform's server/http package). Here we drive one request in-process.
	req := httptest.NewRequest(http.MethodPost, "/orgs/7/users?notify=true",
		strings.NewReader(`{"name":"Ada","email":"ada@example.com"}`))
	req.Header.Set(encoding.ContentTypeHeaderKey, "application/json")

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)

	fmt.Println("status:", rec.Code)
	fmt.Println("body:", strings.TrimSpace(rec.Body.String()))

	// Output:
	// status: 201
	// body: {"data":{"name":"Ada","email":"ada@example.com","id":7001},"details":{"currentAccountID":"","traceID":""}}
}

// upsertForm is the body of a PUT that creates or replaces.
type upsertForm struct {
	Name string `json:"name"`
	ID   uint64 `path:"userID"`
}

// ExampleResult demonstrates a handler naming the status of one response: an
// upsert answers 201 when it created the row and 200 when it replaced one, over
// a body that looks the same either way.
func ExampleResult() {
	r := routing.New(chi.NewBackend(&chi.Config{ServiceName: "example-service"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	// Pretend the store already holds user 7 and nothing else.
	existing := map[uint64]bool{7: true}

	routing.Put(r, "/users/{userID:uint64}", func(_ context.Context, in upsertForm) (routing.Result[person], error) {
		created := !existing[in.ID]
		existing[in.ID] = true

		out := person{ID: in.ID, Name: in.Name}
		if !created {
			return routing.Result[person]{Value: out}, nil
		}

		// Location is worth setting only on the response that created
		// something, which is the same response that chose the 201.
		return routing.Result[person]{
			Value:  out,
			Status: http.StatusCreated,
			Header: http.Header{"Location": {fmt.Sprintf("/users/%d", in.ID)}},
		}, nil
	},
		routing.WithEnvelope(false),
		// The registered status is the documented one; the other is declared.
		routing.WithAdditionalResponse(http.StatusCreated, new(person), "created"),
	)

	if err := r.Err(); err != nil {
		panic(err)
	}

	for _, id := range []string{"7", "8"} {
		req := httptest.NewRequest(http.MethodPut, "/users/"+id, strings.NewReader(`{"name":"Ada"}`))
		req.Header.Set(encoding.ContentTypeHeaderKey, "application/json")

		rec := httptest.NewRecorder()
		r.Handler().ServeHTTP(rec, req)

		fmt.Println("status:", rec.Code, "location:", rec.Header().Get("Location"),
			"body:", strings.TrimSpace(rec.Body.String()))
	}

	// Output:
	// status: 200 location:  body: {"name":"Ada","email":"","id":7}
	// status: 201 location: /users/8 body: {"name":"Ada","email":"","id":8}
}

// storeArea takes a bound path parameter next to a body the router does not
// parse.
func storeArea(_ context.Context, in struct {
	Document routing.RawBody
	AreaID   uint64 `path:"areaID"`
}) (routing.Empty, error) {
	fmt.Println("area:", in.AreaID)
	fmt.Println("document:", string(in.Document))

	return routing.Empty{}, nil
}

// ExampleRawBody demonstrates a route whose body is a document rather than an
// object with fields, bounded to a size the route chooses.
func ExampleRawBody() {
	r := routing.New(chi.NewBackend(&chi.Config{ServiceName: "example-service"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	routing.Put(r, "/areas/{areaID:uint64}/geojson", storeArea,
		routing.WithRequestContentType("application/geo+json"),
		routing.WithMaxRequestBody(4<<20),
		routing.WithResponseStatus(http.StatusNoContent),
	)

	if err := r.Err(); err != nil {
		panic(err)
	}

	req := httptest.NewRequest(http.MethodPut, "/areas/12/geojson",
		strings.NewReader(`{"type":"Point","coordinates":[0,0]}`))
	req.Header.Set(encoding.ContentTypeHeaderKey, "application/geo+json")

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)

	fmt.Println("status:", rec.Code)

	// Output:
	// area: 12
	// document: {"type":"Point","coordinates":[0,0]}
	// status: 204
}

// ExampleRouter_Handle demonstrates the Router's default body bound reaching a
// raw route, and the one route that reads as much as arrives saying so.
func ExampleRouter_Handle() {
	r := routing.New(chi.NewBackend(&chi.Config{ServiceName: "example-service"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON),
		routing.WithDefaultMaxRequestBody(16))

	// A payment webhook: registered raw because its verifier needs the request
	// itself, and bounded all the same.
	r.Handle(http.MethodPost, "/webhooks/payments", http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		fmt.Println("webhook read:", len(body), "err:", err)

		res.WriteHeader(http.StatusNoContent)
	}))

	// An upload that streams: it opts out of the Router's bound rather than
	// making every other route share its ceiling.
	r.MaxRequestBody(0).Handle(http.MethodPost, "/uploads", http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		fmt.Println("upload read:", len(body), "err:", err)

		res.WriteHeader(http.StatusNoContent)
	}))

	payload := strings.NewReader(`{"id":"evt_1","amount":100}`)

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhooks/payments", payload))
	fmt.Println("webhook status:", rec.Code)

	if _, err := payload.Seek(0, io.SeekStart); err != nil {
		panic(err)
	}

	rec = httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/uploads", payload))
	fmt.Println("upload status:", rec.Code)

	// Output:
	// webhook status: 413
	// upload read: 27 err: <nil>
	// upload status: 204
}
