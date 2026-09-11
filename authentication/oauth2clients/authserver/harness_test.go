package authserver_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/authserver"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// The two registries these tests work in, and the two people in them.
var (
	tenantA = tenancy.Of("tenant-a")
	tenantB = tenancy.Of("tenant-b")
)

const (
	userA = "user-a"
	userB = "user-b"
)

// fakeRegistry is an oauth2clients.Store that answers one lookup.
//
// It is hand-written rather than generated because these tests exercise one
// method: what the seams do with what ResolveClientID hands back. The other six
// are declared to satisfy the interface and panic if anything reaches them,
// which is the assertion that these seams read nothing else.
type fakeRegistry struct {
	client *oauth2clients.Client
	err    error
}

var _ oauth2clients.Store = (*fakeRegistry)(nil)

func (f *fakeRegistry) ResolveClientID(
	context.Context, database.SQLQueryExecutor, string,
) (*oauth2clients.Client, error) {
	return f.client, f.err
}

func (f *fakeRegistry) CreateClient(
	context.Context, database.Tx, tenancy.Scope, *oauth2clients.Client,
) error {
	panic("the authorization server seams do not write registrations")
}

func (f *fakeRegistry) UpdateClient(
	context.Context, database.Tx, tenancy.Scope, string, *oauth2clients.UpdateInput,
) error {
	panic("the authorization server seams do not write registrations")
}

func (f *fakeRegistry) ArchiveClient(context.Context, database.Tx, tenancy.Scope, string) error {
	panic("the authorization server seams do not write registrations")
}

func (f *fakeRegistry) GetClient(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
) (*oauth2clients.Client, error) {
	panic("the authorization server seams resolve by client_id, never by row id")
}

func (f *fakeRegistry) ListClients(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
	panic("the authorization server seams do not page registrations")
}

func (f *fakeRegistry) ListClientsForOwner(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
	panic("the authorization server seams do not page registrations")
}

// unreadRegistry is a registry that must never be consulted.
//
// It is what asserts an ordering rather than an answer: the authenticator signs
// somebody in before it reads a registration, so every case that refuses at the
// password reaches this and panics if the order ever inverts. A fake returning
// an error would let that case pass for the wrong reason.
type unreadRegistry struct {
	fakeRegistry
}

func (*unreadRegistry) ResolveClientID(
	context.Context, database.SQLQueryExecutor, string,
) (*oauth2clients.Client, error) {
	panic("the registration was read before the sign-in that must precede it")
}

// fakeProtocolStore is an oauth2server.Store whose client half must never be
// reached: the decorator overrides all three of those methods, and anything
// arriving here is the embedding having stopped working.
type fakeProtocolStore struct {
	oauth2server.Store
}

// resolverFunc is the adapter the package already exports, under a shorter name
// for the call sites below.
//
// An alias rather than a second declaration of the same shape: a suite that
// redeclared it would be exercising its own adapter and leaving the one
// consumers reach for untested, which is the whole failure mode of writing a
// convenience twice.
type resolverFunc = authserver.ScopedSubjectResolverFunc

// resolving is the common inner resolver: this subject, in this registry.
func resolving(userID string, scope tenancy.Scope) resolverFunc {
	return func(context.Context, *http.Request) (*oauth2server.Subject, tenancy.Scope, error) {
		return &oauth2server.Subject{ID: userID}, scope, nil
	}
}

// signInHarness is a real authentication/signin service over a real identity
// store on real SQLite, with one registered user.
//
// The authenticator takes a *signin.Service, so there is no seam to fake here —
// and faking one would be the wrong trade anyway. What these tests are for is
// the order this seam does two things in: it signs somebody in, and only then
// reads the registration against them. A stubbed sign-in would answer for the
// half that decides whether the second step is reached at all.
type signInHarness struct {
	db       database.Client
	svc      *signin.Service
	user     *identity.User
	issuer   *fakeIssuer
	hooks    *recordingHooks
	password string
}

// newSignInHarness stands up the directory, the service and one user in scope.
func newSignInHarness(t *testing.T, scope tenancy.Scope, opts ...signin.ServiceOption) *signInHarness {
	t.Helper()

	db := newDBClient(t)

	stmts, err := migrations.Statements(dialect.SQLite, "")
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	store, err := identity.NewSQLStore(db)
	must.NoError(t, err)

	authenticator := argon2.NewArgon2Authenticator()

	issuer, hooks := &fakeIssuer{}, &recordingHooks{}

	svc, err := signin.NewService(db, store, authenticator, issuer,
		append([]signin.ServiceOption{
			signin.WithTOTPIssuer("Example"),
			signin.WithHooks(hooks),
		}, opts...)...)
	must.NoError(t, err)

	h := &signInHarness{
		db:       db,
		svc:      svc,
		issuer:   issuer,
		hooks:    hooks,
		password: "correct horse battery staple",
	}

	hashed, err := authenticator.HashPassword(t.Context(), h.password)
	must.NoError(t, err)

	identitySvc, err := identity.NewService(db, store)
	must.NoError(t, err)

	registration, err := identitySvc.Register(t.Context(), scope,
		&identity.User{
			Username:       "jane",
			EmailAddress:   "jane@example.test",
			HashedPassword: hashed,
			AccountStatus:  identity.StatusGood,
			Scope:          scope,
		},
		&identity.Account{Name: "Jane's", Scope: scope},
		[]string{"owner"},
	)
	must.NoError(t, err)

	h.user = registration.User

	return h
}

// fakeIssuer stands in for whatever mints a consumer's own session token.
//
// The authorization server never sees one — it wants a subject, not a token —
// and calls is how the tests say so: this seam takes signin's token-less doors,
// so a non-zero count is the wart that signin.Service.Authenticate exists to
// remove growing back.
type fakeIssuer struct {
	calls int
}

func (f *fakeIssuer) IssueToken(
	_ context.Context,
	subject string,
	_ time.Duration,
	_ map[string]any,
) (token, jti string, err error) {
	f.calls++

	return "token-for-" + subject, "jti-" + subject, nil
}

// recordingHooks records which of signin's two sign-in hooks ran.
type recordingHooks struct {
	signin.NoopHooks

	authentications []*signin.Authentication
	signIns         []*signin.SignIn
}

func (h *recordingHooks) AfterAuthenticate(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	a *signin.Authentication,
) error {
	h.authentications = append(h.authentications, a)

	return nil
}

func (h *recordingHooks) AfterIssueToken(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	s *signin.SignIn,
) error {
	h.signIns = append(h.signIns, s)

	return nil
}

// registration builds a fixture in one registry, optionally owned.
func registration(scope tenancy.Scope, owner string) *oauth2clients.Client {
	return &oauth2clients.Client{
		Scope:         scope,
		BelongsToUser: owner,
		ID:            "row-1",
		ClientID:      "cid-1",
		SecretHash:    oauth2server.Hash("s3cret"),
		Name:          "test client",
		RedirectURIs:  []string{"https://example.test/callback"},
		Scopes:        []string{"recipes:read"},
	}
}
