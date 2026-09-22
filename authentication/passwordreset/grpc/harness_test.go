package grpc_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetgrpc "github.com/primandproper/platform-go/v14/authentication/passwordreset/grpc"
	resetmigrations "github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/identity"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// testScope is a named tenant, deliberately not Global: a surface that dropped
// its scope would still pass every assertion made under Global, since the empty
// identifier is what an unscoped column holds anyway.
var testScope = tenancy.Of("tenant_a")

// prefixCounter keeps parallel tests off each other's tables. Every harness gets
// its own table prefix in one shared SQLite file per test.
var prefixCounter atomic.Uint64

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct{ connectionString string }

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 30 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// recordingMailer is the only way a test gets at a reset secret, which is the
// point: it goes to the person the account is about and never into a response,
// so a test here plays the mail client.
type recordingMailer struct {
	sent []*passwordreset.Mail
	mu   sync.Mutex
}

var _ passwordreset.Mailer = (*recordingMailer)(nil)

func (m *recordingMailer) SendPasswordReset(_ context.Context, mail *passwordreset.Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sent = append(m.sent, mail)

	return nil
}

// lastSecret is the secret of the most recent mail, and fails the test if none
// was sent — an absent mail is the failure mode every test here cares about.
func (m *recordingMailer) lastSecret(tb testing.TB) string {
	tb.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	must.SliceNotEmpty(tb, m.sent)

	return m.sent[len(m.sent)-1].Issuance.Secret
}

func (m *recordingMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sent)
}

// harness is one database, one service and one connected client.
type harness struct {
	db     database.Client
	store  identity.Store
	mailer *recordingMailer

	// rootCtx carries no credential, because nothing on this service reads one.
	rootCtx context.Context

	client passwordresetpb.PasswordResetServiceClient

	user     *identity.User
	password string
}

func newHarness(t *testing.T, opts ...passwordresetgrpc.Option) *harness {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "passwordreset.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("pr_%d", prefixCounter.Add(1))

	for _, stmts := range [][]string{
		mustStatements(t, identitymigrations.Statements, prefix),
		mustStatements(t, resetmigrations.Statements, prefix),
	} {
		for _, stmt := range stmts {
			_, execErr := db.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr)
		}
	}

	store, err := identity.NewSQLStore(db, identity.WithTablePrefix(prefix))
	must.NoError(t, err)

	tokens, err := passwordreset.NewSQLStore(&passwordreset.Config{TablePrefix: prefix}, db)
	must.NoError(t, err)

	mailer := &recordingMailer{}
	authenticator := argon2.NewArgon2Authenticator()

	svc, err := passwordreset.NewService(db, tokens, store, authenticator, mailer,
		// The floor holds every request for half a second by default, which this
		// suite cannot afford across a bufconn round trip on every case. What it
		// protects is asserted in the service's own tests.
		passwordreset.WithRequestFloor(time.Nanosecond),
	)
	must.NoError(t, err)

	// The scope comes off the connection, which is the seam that exists because
	// nobody on this service has signed in.
	opts = append([]passwordresetgrpc.Option{
		passwordresetgrpc.WithScopeResolver(func(context.Context) (tenancy.Scope, error) { return testScope, nil }),
	}, opts...)

	srv, err := passwordresetgrpc.NewServer(svc, opts...)
	must.NoError(t, err)

	// The error-encoding interceptor is what puts a sentinel into the status
	// details, and the client's decoding one is what takes it out again. Without
	// both, every errors.Is below would fail against a *status.Error.
	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcerrors.UnaryErrorEncodingInterceptor()))
	srv.RegisterOn(grpcServer)

	listener := bufconn.Listen(1 << 20)

	go func() { _ = grpcServer.Serve(listener) }()

	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(grpcerrors.UnaryErrorDecodingInterceptor()),
	)
	must.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	h := &harness{
		db:       db,
		store:    store,
		mailer:   mailer,
		rootCtx:  t.Context(),
		client:   passwordresetpb.NewPasswordResetServiceClient(conn),
		password: "correct horse battery staple",
	}

	h.register(t, authenticator)

	return h
}

// mustStatements renders one package's schema for SQLite.
func mustStatements(tb testing.TB, render func(dialect.Dialect, string) ([]string, error), prefix string) []string {
	tb.Helper()

	stmts, err := render(dialect.SQLite, prefix)
	must.NoError(tb, err)

	return stmts
}

// register creates the user every test resets.
func (h *harness) register(t *testing.T, authenticator *argon2.Argon2Authenticator) {
	t.Helper()

	hashed, err := authenticator.HashPassword(t.Context(), h.password)
	must.NoError(t, err)

	identitySvc, err := identity.NewService(h.db, h.store)
	must.NoError(t, err)

	registration, err := identitySvc.Register(t.Context(), testScope,
		&identity.User{
			Username:       "jane",
			EmailAddress:   "jane@example.com",
			HashedPassword: hashed,
			AccountStatus:  identity.StatusGood,
			Scope:          testScope,
		},
		&identity.Account{Name: "Jane's", Scope: testScope},
		[]string{"owner"},
	)
	must.NoError(t, err)

	h.user = registration.User
}

// storedPassword reads the directory's hash back, on a connection that is not in
// anybody's transaction — so what it answers is what committed.
func (h *harness) storedPassword(t *testing.T) string {
	t.Helper()

	user, err := h.store.GetUser(t.Context(), h.db.Reader(), testScope, h.user.ID)
	must.NoError(t, err)

	return user.HashedPassword
}
