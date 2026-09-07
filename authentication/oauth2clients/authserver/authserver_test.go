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
			resolverFunc(func(_ context.Context, _ *http.Request) (*oauth2server.Subject, error) {
				return &oauth2server.Subject{ID: userA}, nil
			}),
			&fakeRegistry{client: registration(tenantA, userA)},
			newDBClient(t),
			authserver.WithResolverScopeResolver(func(_ context.Context, _ *http.Request) (tenancy.Scope, error) {
				return tenantA, nil
			}),
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
			resolverFunc(func(_ context.Context, _ *http.Request) (*oauth2server.Subject, error) {
				return &oauth2server.Subject{ID: userA}, nil
			}),
			&fakeRegistry{client: registration(tenantA, "")},
			newDBClient(t),
			authserver.WithResolverScopeResolver(func(_ context.Context, _ *http.Request) (tenancy.Scope, error) {
				return tenantB, nil
			}),
		)
		must.NoError(t, err)

		subject, err := guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))

		// (nil, nil), and not an error. An error here becomes a server_error
		// redirect with no form rendered; declining sends the person to the
		// form, where the authenticator makes the same check and can say why.
		must.NoError(t, err)
		test.Nil(t, subject)
	})

	T.Run("declines when the registration belongs to somebody else", func(t *testing.T) {
		t.Parallel()

		guard, err := authserver.NewGuardedResolver(
			resolverFunc(func(_ context.Context, _ *http.Request) (*oauth2server.Subject, error) {
				return &oauth2server.Subject{ID: userB}, nil
			}),
			&fakeRegistry{client: registration(tenantA, userA)},
			newDBClient(t),
			authserver.WithResolverScopeResolver(func(_ context.Context, _ *http.Request) (tenancy.Scope, error) {
				return tenantA, nil
			}),
		)
		must.NoError(t, err)

		subject, err := guard.ResolveSubject(t.Context(), authorizeRequest(t, "cid-1"))
		must.NoError(t, err)
		test.Nil(t, subject)
	})

	T.Run("hands back the inner resolver's decline unchanged", func(t *testing.T) {
		t.Parallel()

		guard, err := authserver.NewGuardedResolver(
			resolverFunc(func(_ context.Context, _ *http.Request) (*oauth2server.Subject, error) {
				// The inner resolver declining, which is what this case asserts is
				// handed back unchanged.
				return nil, nil
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
			resolverFunc(func(_ context.Context, _ *http.Request) (*oauth2server.Subject, error) {
				return nil, broken
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
			resolverFunc(func(_ context.Context, _ *http.Request) (*oauth2server.Subject, error) {
				return &oauth2server.Subject{ID: userA}, nil
			}),
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

		_, err = authserver.NewGuardedResolver(
			resolverFunc(func(_ context.Context, _ *http.Request) (*oauth2server.Subject, error) { return nil, nil }),
			nil, newDBClient(t))
		test.ErrorIs(t, err, oauth2clients.ErrNilStore)
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
