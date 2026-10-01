package anonymous

import (
	"context"
	nethttp "net/http"
	"testing"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	dataprivacymock "github.com/primandproper/platform-go/v14/dataprivacy/mock"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"
	"github.com/primandproper/platform-go/v14/operations"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"
	operationsmock "github.com/primandproper/platform-go/v14/operations/mock"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	oauth2memory "github.com/primandproper/primitives-go/v2/authentication/oauth2server/memory"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/encoding"
	"github.com/primandproper/primitives-go/v2/routing"
	"github.com/primandproper/primitives-go/v2/routing/backends/chi"
	"github.com/primandproper/primitives-go/v2/tenancy"
	uploadsmock "github.com/primandproper/primitives-go/v2/uploads/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestHTTPRosterMatchesWhatEachSurfaceMounts is the HTTP half's counterpart to
// TestRosterCoversEveryService, and it checks both directions.
//
// There is no protobuf registry to read an HTTP surface's routes out of, so the
// roster lists them — and a list is exactly what can stop describing the code.
// This builds each surface's real handlers, mounts them on a router of its own,
// and compares what Mount returned with what the roster says. A route added
// later is missing from the roster and fails here rather than going unasserted;
// a route removed leaves an entry that fails here rather than asserting against
// a path that answers 404 to everybody, which would pass for the wrong reason.
func TestHTTPRosterMatchesWhatEachSurfaceMounts(t *testing.T) {
	t.Parallel()

	mounted := map[string][]*routing.Route{
		"dataprivacy":   mountDataPrivacy(t),
		"mediaregistry": {mountMediaRegistry(t)},
		"oauth2server":  mountOAuth2Server(t),
		"operations":    mountOperations(t),
	}

	roster := httpRoster()
	must.SliceLen(t, len(mounted), roster, must.Sprint("the HTTP roster and the surfaces this test mounts disagree about how many there are"))

	for _, surf := range roster {
		routes, ok := mounted[surf.name]
		must.True(t, ok, must.Sprintf("roster entry %q is not a surface this test knows how to mount", surf.name))

		got := map[httpRoute]struct{}{}
		for _, r := range routes {
			must.NotNil(t, r, must.Sprintf("%s's Mount returned a nil route", surf.name))
			got[httpRoute{method: r.Method, path: r.Path}] = struct{}{}
		}

		want := map[httpRoute]struct{}{}
		for _, r := range surf.routes {
			want[r] = struct{}{}
		}

		for r := range got {
			test.MapContainsKey(t, want, r, test.Sprintf("%s mounts %s %s and the roster does not list it", surf.name, r.method, r.path))
		}

		for r := range want {
			test.MapContainsKey(t, got, r, test.Sprintf("the roster lists %s %s for %s, which does not mount it", r.method, r.path, surf.name))
		}
	}
}

// recordingBackend is a routing.Backend that keeps what was registered on it,
// for the one surface whose Mount returns nothing to read the routes back from.
type recordingBackend struct {
	routes []*routing.Route
}

func (b *recordingBackend) Handle(method, pattern string, _ nethttp.Handler) {
	b.routes = append(b.routes, &routing.Route{Method: method, Path: pattern})
}

func (*recordingBackend) Use(...routing.Middleware) {}

func (*recordingBackend) PathValue(*nethttp.Request, string) string { return "" }

func (*recordingBackend) Handler() nethttp.Handler { return nethttp.NotFoundHandler() }

// mountOAuth2Server mounts the authorization server the way a deployment whose
// clients come from the registry builds it: without /register, which
// oauth2clients/authserver's documentation says such a deployment does not
// serve. A server built with open registration mounts one route more, and it is
// anonymous by RFC 7591's design; that arrangement is not one this module's
// registry is used in, so the roster does not describe it.
func mountOAuth2Server(t *testing.T) []*routing.Route {
	t.Helper()

	srv, err := oauth2server.NewServer("https://example.com", oauth2memory.NewStore(),
		oauth2server.SubjectAuthenticatorFunc(func(context.Context, *nethttp.Request) (*oauth2server.Subject, error) {
			return nil, oauth2server.ErrLoginFailed
		}),
		oauth2server.WithDynamicRegistration(false))
	must.NoError(t, err)

	backend := &recordingBackend{}
	srv.Mount(routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)))

	return backend.routes
}

func newRouter() *routing.Router {
	return routing.New(chi.NewBackend(&chi.Config{ServiceName: "roster"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))
}

func newDataPrivacyHandlers(t *testing.T) *dataprivacyhttp.Handlers {
	t.Helper()

	h, err := dataprivacyhttp.New(&dataprivacymock.ServiceMock{},
		dataprivacyhttp.WithSubjectResolver(func(context.Context) (dataprivacy.Subject, error) {
			return dataprivacy.Subject{}, nil
		}))
	must.NoError(t, err)

	return h
}

func mountDataPrivacy(t *testing.T) []*routing.Route {
	t.Helper()

	return newDataPrivacyHandlers(t).Mount(newRouter())
}

func mountMediaRegistry(t *testing.T) *routing.Route {
	t.Helper()

	h, err := mediaregistryhttp.New(&mediaregistrymock.StoreMock{}, &databasemock.ClientMock{
		DialectFunc: func() dialect.Dialect { return dialect.SQLite },
	}, &uploadsmock.UploadManagerMock{},
		mediaregistryhttp.WithCallerResolver(func(context.Context) (mediaregistryhttp.Caller, error) {
			return mediaregistryhttp.Caller{}, nil
		}))
	must.NoError(t, err)

	return h.Mount(newRouter())
}

func mountOperations(t *testing.T) []*routing.Route {
	t.Helper()

	store := &operationsmock.StoreMock{}

	// With a watcher, because the composition root always registers one and a
	// deployment built from it therefore serves the events route too.
	watcher, err := operations.NewWatcher(t.Context(), &operations.WatcherConfig{},
		&databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return dialect.SQLite }}, store)
	must.NoError(t, err)

	h, err := operationshttp.New(&operationsmock.ServiceMock{},
		operationshttp.WithOwnerResolver(func(context.Context) (tenancy.Scope, error) { return tenancy.Global(), nil }),
		operationshttp.WithWatcher(watcher))
	must.NoError(t, err)

	return h.Mount(newRouter())
}
