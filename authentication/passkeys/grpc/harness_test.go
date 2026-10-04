package grpc_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passkeys"
	passkeysgrpc "github.com/primandproper/platform-go/v14/authentication/passkeys/grpc"
	passkeysclient "github.com/primandproper/platform-go/v14/authentication/passkeys/grpc/client"
	passkeysmigrations "github.com/primandproper/platform-go/v14/authentication/passkeys/migrations"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/errormappers"
	"github.com/primandproper/platform-go/v14/identity"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/totp"
	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/authentication/webauthn/webauthntest"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	pquernatotp "github.com/pquerna/otp/totp"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// The relying party the harness verifies against, and the origin the virtual
// authenticator answers from.
const (
	testRPID   = "example.com"
	testOrigin = "https://example.com"
)

// testScope is a named tenant, deliberately not Global, so a surface that
// dropped its scope fails rather than passing under the empty identifier.
var testScope = tenancy.Of("tenant_a")

// TestMain registers the domain tier's mappers, as a composition root does.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

var prefixCounter atomic.Uint64

type testClientConfig struct{ connectionString string }

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// testPrincipal is who the authenticate interceptor puts on the context.
type testPrincipal struct{ userID string }

var _ callers.Principal = (*testPrincipal)(nil)

func (p *testPrincipal) UserID() string          { return p.userID }
func (p *testPrincipal) Scope() tenancy.Scope    { return testScope }
func (p *testPrincipal) ActiveAccountID() string { return "" }

type principalKey struct{}

// mdUserID is the metadata a test request names its caller in. It stands in
// for a consumer's own authentication interceptor.
const mdUserID = "test-user-id"

func asUser(ctx context.Context, userID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdUserID, userID)
}

func authenticate(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if ids := md.Get(mdUserID); len(ids) > 0 && ids[0] != "" {
			ctx = context.WithValue(ctx, principalKey{}, &testPrincipal{userID: ids[0]})
		}
	}

	return handler(ctx, req)
}

func extractPrincipal(ctx context.Context) (callers.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(callers.Principal)

	return p, ok
}

// memorySessions is ceremony state in a map; the real stores are held to their
// own suites.
type memorySessions struct {
	sessions map[string]webauthn.SessionData
	mu       sync.Mutex
}

var _ webauthn.SessionStore = (*memorySessions)(nil)

func (m *memorySessions) Save(_ context.Context, session *webauthn.SessionData, ttl time.Duration) error {
	if err := webauthn.ValidateSession(session, ttl); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.sessions[session.Challenge] = *session

	return nil
}

func (m *memorySessions) Consume(_ context.Context, challenge string) (*webauthn.SessionData, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[challenge]
	if !ok {
		return nil, webauthn.ErrSessionNotFound
	}

	delete(m.sessions, challenge)

	return &session, nil
}

// fakeTokens stands in for a real token issuer.
type fakeTokens struct{}

func (*fakeTokens) IssueToken(_ context.Context, subject string, _ time.Duration, _ map[string]any) (token, jti string, err error) {
	return "token-for-" + subject, "jti-" + subject, nil
}

// recordingSignInHooks keeps the credential kind every sign-in was stamped
// with.
type recordingSignInHooks struct {
	signin.NoopHooks

	kinds []signin.CredentialKind
	mu    sync.Mutex
}

func (h *recordingSignInHooks) AfterAuthenticate(_ context.Context, _ database.Tx, _ tenancy.Scope, auth *signin.Authentication) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.kinds = append(h.kinds, auth.CredentialKind)

	return nil
}

func (h *recordingSignInHooks) recorded() []signin.CredentialKind {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]signin.CredentialKind(nil), h.kinds...)
}

// harness is one database holding a directory, a sign-in service and a
// passkey service, and a client connected to the passkeys surface over them.
type harness struct {
	db      database.Client
	store   identity.Store
	signIns *recordingSignInHooks
	client  *passkeysclient.Client

	jane, bob *identity.User
}

func newHarness(t *testing.T, svcOpts ...passkeys.ServiceOption) *harness {
	t.Helper()

	return newHarnessWith(t, nil, svcOpts...)
}

// newHarnessWith is newHarness with options for the server as well as for the
// service beneath it.
func newHarnessWith(t *testing.T, srvOpts []passkeysgrpc.Option, svcOpts ...passkeys.ServiceOption) *harness {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "passkeys.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("pk_%d", prefixCounter.Add(1))

	for _, render := range []func(dialect.Dialect, string) ([]string, error){
		identitymigrations.Statements,
		passkeysmigrations.Statements,
	} {
		stmts, renderErr := render(dialect.SQLite, prefix)
		must.NoError(t, renderErr)

		for _, stmt := range stmts {
			_, execErr := db.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr)
		}
	}

	store, err := identity.NewSQLStore(db, identity.WithTablePrefix(prefix))
	must.NoError(t, err)

	signIns := &recordingSignInHooks{}

	signInSvc, err := signin.NewService(db, store, argon2.NewArgon2Authenticator(), &fakeTokens{}, []string{"owner"}, signIns,
		signin.WithTOTPIssuer("Example"))
	must.NoError(t, err)

	credentials, err := passkeys.NewSQLStore(db, passkeys.WithTablePrefix(prefix))
	must.NoError(t, err)

	rp, err := webauthn.NewRelyingParty(t.Context(), &webauthn.Config{
		RPID:          testRPID,
		RPDisplayName: "Example",
		RPOrigins:     []string{testOrigin},
	}, &memorySessions{sessions: map[string]webauthn.SessionData{}})
	must.NoError(t, err)

	// A handle is the user's ID, which is UserIDHandle's arrangement.
	users, err := passkeys.NewUserSource(credentials, func(ctx context.Context, handle []byte) (passkeys.UserIdentity, error) {
		user, getErr := store.GetUser(ctx, db.Reader(), testScope, string(handle))
		if getErr != nil {
			return passkeys.UserIdentity{}, getErr
		}

		return passkeys.UserIdentity{UserID: user.ID, Name: user.Username, DisplayName: user.Username}, nil
	})
	must.NoError(t, err)

	svc, err := passkeys.NewService(db, credentials, rp, users, passkeys.NoopHooks{}, append([]passkeys.ServiceOption{
		passkeys.WithEnrollmentGate(passkeys.AdmitEveryEnrollment),
		passkeys.WithUsernameResolver(func(ctx context.Context, scope tenancy.Scope, username string) ([]byte, error) {
			user, getErr := store.GetUserByUsername(ctx, db.Reader(), scope, username)
			if platformerrors.Is(getErr, identity.ErrUserNotFound) {
				return nil, passkeys.ErrUnknownUsername
			}

			if getErr != nil {
				return nil, getErr
			}

			return []byte(user.ID), nil
		}),
	}, svcOpts...)...)
	must.NoError(t, err)

	srv, err := passkeysgrpc.NewServer(svc, db, signInSvc, extractPrincipal, append([]passkeysgrpc.Option{
		passkeysgrpc.WithScopeResolver(func(context.Context) (tenancy.Scope, error) { return testScope, nil }),
	}, srvOpts...)...)
	must.NoError(t, err)

	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcerrors.UnaryErrorEncodingInterceptor(), authenticate))
	srv.RegisterOn(grpcServer)

	listener := bufconn.Listen(1 << 20)

	go func() { _ = grpcServer.Serve(listener) }()

	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		passkeysclient.DefaultInterceptors(),
	)
	must.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	h := &harness{db: db, store: store, signIns: signIns, client: passkeysclient.Wrap(conn)}
	h.jane = h.registerUser(t, "jane")
	h.bob = h.registerUser(t, "bob")

	return h
}

func (h *harness) registerUser(t *testing.T, username string) *identity.User {
	t.Helper()

	identitySvc, err := identity.NewService(h.db, h.store, identity.NoopHooks{})
	must.NoError(t, err)

	registration, err := identitySvc.Register(t.Context(), testScope,
		&identity.User{
			Username:      username,
			EmailAddress:  username + "@example.com",
			AccountStatus: identity.StatusGood,
			Scope:         testScope,
		},
		&identity.Account{Name: username + "'s", Scope: testScope},
		[]string{"owner"},
	)
	must.NoError(t, err)

	return registration.User
}

// enrollTOTP gives a user a proven second factor and returns its secret.
func (h *harness) enrollTOTP(t *testing.T, user *identity.User) string {
	t.Helper()

	enrollment, err := totp.NewGenerator().Generate(t.Context(), "Example", user.Username)
	must.NoError(t, err)

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		if err = h.store.UpdateUserTwoFactorSecret(t.Context(), tx, testScope, user.ID, enrollment.Secret); err != nil {
			return err
		}

		_, err = h.store.MarkUserTwoFactorSecretVerified(t.Context(), tx, testScope, user.ID)

		return err
	}))

	return enrollment.Secret
}

func totpCode(t *testing.T, secret string) string {
	t.Helper()

	code, err := pquernatotp.GenerateCode(secret, time.Now().UTC())
	must.NoError(t, err)

	return code
}

// ceremonyOptions is the part of a begin's JSON a test reads: the challenge
// every ceremony signs, and the user handle a registration hands out.
type ceremonyOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"publicKey"`
}

func parseOptions(t *testing.T, raw []byte) ceremonyOptions {
	t.Helper()

	var options ceremonyOptions
	must.NoError(t, json.Unmarshal(raw, &options))
	must.NotEq(t, "", options.PublicKey.Challenge)

	return options
}

func (o ceremonyOptions) handle(t *testing.T) []byte {
	t.Helper()

	handle, err := base64.RawURLEncoding.DecodeString(o.PublicKey.User.ID)
	must.NoError(t, err)

	return handle
}

// newDevice mints an authenticator that verifies the person unless told not
// to.
func newDevice(t *testing.T, opts ...webauthntest.AuthenticatorOption) *webauthntest.Authenticator {
	t.Helper()

	return webauthntest.NewAuthenticator(t, testRPID, testOrigin, opts...)
}
