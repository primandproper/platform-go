package authserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/authserver"
	"github.com/primandproper/platform-go/v14/authentication/oauth2server"
	"github.com/primandproper/platform-go/v14/database"
	"github.com/primandproper/platform-go/v14/database/sqlite"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testClientConfig is the minimal database.ClientConfig these tests dial with.
// The handle is taken for Reader() and the registry is faked, so nothing here
// executes a statement.
type testClientConfig struct{ connectionString string }

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

func newDBClient(tb testing.TB) database.Client {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "authserver.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return client
}

func TestStore(T *testing.T) {
	T.Parallel()

	T.Run("resolves a registration and renders it as the protocol's client", func(t *testing.T) {
		t.Parallel()

		registered := registration(tenantA, userA)

		store, err := authserver.NewStore(&fakeProtocolStore{}, &fakeRegistry{client: registered}, newDBClient(t))
		must.NoError(t, err)

		client, err := store.GetClient(t.Context(), "cid-1")
		must.NoError(t, err)

		// The protocol identifier, not the row id: what the authorization server
		// looks a client up by is what it must hand back.
		test.EqOp(t, registered.ClientID, client.ID)
		test.EqOp(t, registered.SecretHash, client.SecretHash)
		test.Eq(t, registered.RedirectURIs, client.RedirectURIs)

		// The registered scopes cross, so an authorization request for anything
		// outside them is rejected rather than silently narrowed.
		test.Eq(t, registered.Scopes, client.Scopes)

		// The four constants: every registration here holds a secret, the server
		// implements two grants and one response type, and an administered
		// registration is withdrawn rather than lapsed.
		test.EqOp(t, oauth2server.AuthMethodClientSecret, client.TokenEndpointAuthMethod)
		test.Eq(t, []string{
			oauth2server.GrantTypeAuthorizationCode,
			oauth2server.GrantTypeRefreshToken,
		}, client.GrantTypes)
		test.Eq(t, []string{oauth2server.ResponseTypeCode}, client.ResponseTypes)
		test.True(t, client.ExpiresAt.IsZero(), test.Sprint("an administered registration was given an expiry"))
	})

	T.Run("an unknown registration is the protocol's not-found", func(t *testing.T) {
		t.Parallel()

		store, err := authserver.NewStore(&fakeProtocolStore{},
			&fakeRegistry{err: oauth2clients.ErrClientNotFound}, newDBClient(t))
		must.NoError(t, err)

		_, err = store.GetClient(t.Context(), "cid-1")
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
	})

	T.Run("a withdrawn registration is the protocol's not-found", func(t *testing.T) {
		t.Parallel()

		withdrawn := registration(tenantA, userA)
		archived := time.Now().UTC()
		withdrawn.ArchivedAt = &archived

		store, err := authserver.NewStore(&fakeProtocolStore{},
			&fakeRegistry{client: withdrawn}, newDBClient(t))
		must.NoError(t, err)

		// The registry returns the row rather than hiding it, precisely so this
		// decision is made where a reason can be recorded. What reaches an
		// unauthenticated caller is still the answer that discloses nothing.
		_, err = store.GetClient(t.Context(), "cid-1")
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
	})

	T.Run("a broken registry is not reported as an unknown client", func(t *testing.T) {
		t.Parallel()

		broken := platformerrors.New("the registry is unreachable")

		store, err := authserver.NewStore(&fakeProtocolStore{},
			&fakeRegistry{err: broken}, newDBClient(t))
		must.NoError(t, err)

		_, err = store.GetClient(t.Context(), "cid-1")

		// The distinction the decorator exists to keep: "we have never heard of
		// you" and "we cannot look you up" are different answers, and collapsing
		// them tells a client to re-register against a database that is down.
		test.ErrorIs(t, err, broken)
		test.False(t, platformerrors.Is(err, oauth2server.ErrNotFound))
	})

	T.Run("an empty identifier is refused before the registry is asked", func(t *testing.T) {
		t.Parallel()

		store, err := authserver.NewStore(&fakeProtocolStore{}, &fakeRegistry{}, newDBClient(t))
		must.NoError(t, err)

		_, err = store.GetClient(t.Context(), "")
		test.ErrorIs(t, err, oauth2server.ErrEmptyIdentifier)
	})

	T.Run("registration writes are refused", func(t *testing.T) {
		t.Parallel()

		store, err := authserver.NewStore(&fakeProtocolStore{}, &fakeRegistry{}, newDBClient(t))
		must.NoError(t, err)

		// Defense in depth rather than the control — WithDynamicRegistration
		// (false) is the control — but a deployment that forgot it still cannot
		// have an anonymous caller writing to a permissioned table.
		test.ErrorIs(t, store.CreateClient(t.Context(), &oauth2server.Client{}),
			oauth2server.ErrRegistrationNotServed)
		test.ErrorIs(t, store.DeleteClient(t.Context(), "cid-1"),
			oauth2server.ErrRegistrationNotServed)
	})

	T.Run("refuses what it cannot be built from", func(t *testing.T) {
		t.Parallel()

		_, err := authserver.NewStore(nil, &fakeRegistry{}, newDBClient(t))
		test.ErrorIs(t, err, oauth2server.ErrNilStore)

		_, err = authserver.NewStore(&fakeProtocolStore{}, nil, newDBClient(t))
		test.ErrorIs(t, err, oauth2clients.ErrNilStore)

		_, err = authserver.NewStore(&fakeProtocolStore{}, &fakeRegistry{}, nil)
		test.ErrorIs(t, err, oauth2clients.ErrNilDatabaseClient)
	})
}

func TestGuardedResolver(T *testing.T) {
	T.Parallel()

	T.Run("passes a subject through when the registration admits them", func(t *testing.T) {
		t.Parallel()

		guard, err := authserver.NewGuardedResolver(
			resolving(userA, tenantA),
			&fakeRegistry{client: registration(tenantA, userA)},
			newDBClient(t),
		)
		must.NoError(t, err)

		subject, err := guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))
		must.NoError(t, err)
		must.NotNil(t, subject)
		test.EqOp(t, userA, subject.ID)
	})

	T.Run("declines rather than erroring when the registry does not match", func(t *testing.T) {
		t.Parallel()

		// The bypass this type exists to close, from the resolver side: a
		// first-party application holding a session for somebody in tenant B,
		// presenting a client registered to tenant A.
		guard, err := authserver.NewGuardedResolver(
			resolving(userB, tenantB),
			&fakeRegistry{client: registration(tenantA, "")},
			newDBClient(t),
		)
		must.NoError(t, err)

		subject, err := guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))

		// (nil, nil), and not an error. An error here becomes a server_error
		// redirect with no form rendered; declining sends the person to the
		// form, where the authenticator makes the same check and can say why.
		must.NoError(t, err)
		test.Nil(t, subject)
	})

	T.Run("the registry is the resolver's, not the request's", func(t *testing.T) {
		t.Parallel()

		// The reason ScopedSubjectResolver reports a scope rather than this
		// package reading one off the request. The registration is administered
		// in tenant A, so it admits *any* subject in tenant A — and a scope
		// taken from the request would be a scope the caller chooses. Here the
		// session says tenant B, which is the only fact about the subject that
		// was ever proven, and the two do not match.
		guard, err := authserver.NewGuardedResolver(
			resolving(userB, tenantB),
			&fakeRegistry{client: registration(tenantA, "")},
			newDBClient(t),
		)
		must.NoError(t, err)

		// The request is free to name tenant A in any way a deployment's own
		// ScopeResolver might have read — a host, a path, a header. None of it
		// reaches the check, because there is nowhere for it to enter.
		req := authorizeRequest(t, "cid-1")
		req.Host = "tenant-a.example.test"
		req.Header.Set("X-Tenant", tenantA.String())

		subject, err := guard.ResolveSubject(t.Context(), req)
		must.NoError(t, err)
		test.Nil(t, subject)
	})

	T.Run("declines when the registration belongs to somebody else", func(t *testing.T) {
		t.Parallel()

		guard, err := authserver.NewGuardedResolver(
			resolving(userB, tenantA),
			&fakeRegistry{client: registration(tenantA, userA)},
			newDBClient(t),
		)
		must.NoError(t, err)

		subject, err := guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))
		must.NoError(t, err)
		test.Nil(t, subject)
	})

	T.Run("refuses a subject whose registry was never decided", func(t *testing.T) {
		t.Parallel()

		// The zero tenancy.Scope is the absence of a decision, not the global
		// registry — and the global registry admits anybody, so reading one as
		// the other would turn a resolver's omission into no check at all.
		guard, err := authserver.NewGuardedResolver(
			resolverFunc(func(context.Context, *http.Request) (*oauth2server.Subject, tenancy.Scope, error) {
				return &oauth2server.Subject{ID: userA}, tenancy.Scope{}, nil
			}),
			&fakeRegistry{client: registration(tenantA, "")},
			newDBClient(t),
		)
		must.NoError(t, err)

		_, err = guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))
		test.ErrorIs(t, err, authserver.ErrScopelessSubject)
	})

	T.Run("admits a subject the global registry resolved", func(t *testing.T) {
		t.Parallel()

		// The other half of the case above: a deployment that means the global
		// registry says so, and is admitted.
		guard, err := authserver.NewGuardedResolver(
			resolving(userA, tenancy.Global()),
			&fakeRegistry{client: registration(tenancy.Global(), "")},
			newDBClient(t),
		)
		must.NoError(t, err)

		subject, err := guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))
		must.NoError(t, err)
		must.NotNil(t, subject)
		test.EqOp(t, userA, subject.ID)
	})

	T.Run("a client this registry never issued is a refusal", func(t *testing.T) {
		t.Parallel()

		// Unreachable behind a wired authserver.Store, which is the point: it is
		// reachable only in the deployment that wired the seams and left the
		// authorization server on another store, where every check here would
		// otherwise be skipped silently.
		guard, err := authserver.NewGuardedResolver(
			resolving(userA, tenantA),
			&fakeRegistry{err: oauth2clients.ErrClientNotFound},
			newDBClient(t),
		)
		must.NoError(t, err)

		_, err = guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))
		test.ErrorIs(t, err, authserver.ErrClientNotRegistered)
	})

	T.Run("hands back the inner resolver's decline unchanged", func(t *testing.T) {
		t.Parallel()

		guard, err := authserver.NewGuardedResolver(
			resolverFunc(func(context.Context, *http.Request) (*oauth2server.Subject, tenancy.Scope, error) {
				// The inner resolver declining, which is what this case asserts
				// is handed back unchanged.
				return nil, tenancy.Scope{}, nil
			}),
			&fakeRegistry{},
			newDBClient(t),
		)
		must.NoError(t, err)

		subject, err := guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))
		must.NoError(t, err)
		test.Nil(t, subject)
	})

	T.Run("hands back the inner resolver's error unchanged", func(t *testing.T) {
		t.Parallel()

		broken := platformerrors.New("the session store is unreachable")

		guard, err := authserver.NewGuardedResolver(
			resolverFunc(func(context.Context, *http.Request) (*oauth2server.Subject, tenancy.Scope, error) {
				return nil, tenancy.Scope{}, broken
			}),
			&fakeRegistry{},
			newDBClient(t),
		)
		must.NoError(t, err)

		_, err = guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))
		test.ErrorIs(t, err, broken)
	})

	T.Run("a broken registry is an error rather than a decline", func(t *testing.T) {
		t.Parallel()

		broken := platformerrors.New("the registry is unreachable")

		guard, err := authserver.NewGuardedResolver(
			resolving(userA, tenantA),
			&fakeRegistry{err: broken},
			newDBClient(t),
		)
		must.NoError(t, err)

		// Not a refused credential, and there is no form that fixes it.
		_, err = guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))
		test.ErrorIs(t, err, broken)
	})

	T.Run("refuses what it cannot be built from", func(t *testing.T) {
		t.Parallel()

		_, err := authserver.NewGuardedResolver(nil, &fakeRegistry{}, newDBClient(t))
		test.Error(t, err)

		_, err = authserver.NewGuardedResolver(resolving(userA, tenantA), nil, newDBClient(t))
		test.ErrorIs(t, err, oauth2clients.ErrNilStore)

		_, err = authserver.NewGuardedResolver(resolving(userA, tenantA), &fakeRegistry{}, nil)
		test.ErrorIs(t, err, oauth2clients.ErrNilDatabaseClient)
	})
}

// authorizeRequest builds the /authorize request the seams are handed, with its
// form already parsed — which is what oauth2server does before either seam is
// asked anything.
func authorizeRequest(tb testing.TB, clientID string) *http.Request {
	tb.Helper()

	req := httptest.NewRequestWithContext(tb.Context(), http.MethodGet,
		"/authorize?client_id="+clientID+"&response_type=code", http.NoBody)
	must.NoError(tb, req.ParseForm())

	return req
}
