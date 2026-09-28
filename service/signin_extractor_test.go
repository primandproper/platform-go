package service

import (
	"context"
	"net"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signincfg "github.com/primandproper/platform-go/v14/authentication/signin/config"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	signinclient "github.com/primandproper/platform-go/v14/authentication/signin/grpc/client"
	refreshtokenmigrations "github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/identity"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// TestNew_signInTokensAreTheCallerByDefault is a service built with sign-in and
// no extractor of the application's, and a token that service minted presented
// on a real connection: the caller is the person it was minted for, with no
// interceptor installed and nothing hand-built.
func TestNew_signInTokensAreTheCallerByDefault(T *testing.T) {
	T.Parallel()

	cfg := &Config{
		Name:     "example",
		Database: sqliteDatabase(T),
		Identity: &identitycfg.Config{TablePrefix: storePrefix},
		Tokens:   testTokens(),
		SignIn: &signincfg.Config{
			TOTPIssuer:    "Example",
			RefreshTokens: signincfg.RefreshTokensConfig{TablePrefix: storePrefix},
			Registration:  signincfg.RegistrationConfig{Disabled: true},
		},
	}
	must.NoError(T, cfg.ValidateWithContext(T.Context()))

	i := newInjector(T, cfg)
	do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())

	RegisterTransports(i, &Transports{})

	svc, err := New(i)
	must.NoError(T, err)
	must.SliceContains(T, svc.surfaces, "sign-in gRPC")

	// The default is registered where a composition root can reach it, for
	// the router middleware and the interceptor this package installs neither
	// of.
	extractor, err := do.Invoke[*signingrpc.PrincipalExtractor](i)
	must.NoError(T, err)
	must.NotNil(T, extractor)

	client := do.MustInvoke[database.Client](i)

	for _, render := range []func(dialect.Dialect, string) ([]string, error){
		identitymigrations.Statements, refreshtokenmigrations.Statements,
	} {
		stmts, stmtErr := render(dialect.SQLite, storePrefix)
		must.NoError(T, stmtErr)

		for _, stmt := range stmts {
			_, execErr := client.Writer().ExecContext(T.Context(), stmt)
			must.NoError(T, execErr)
		}
	}

	directory, err := identity.NewService(client, do.MustInvoke[identity.Store](i))
	must.NoError(T, err)

	reg, err := directory.Register(T.Context(), tenancy.Global(),
		&identity.User{Username: "jane", EmailAddress: "jane@example.com", AccountStatus: identity.StatusGood},
		&identity.Account{Name: "Jane's"},
		[]string{"owner"},
	)
	must.NoError(T, err)

	issued, err := do.MustInvoke[*signin.Service](i).IssueForPrincipal(T.Context(), tenancy.Global(), reg.User.ID, reg.Account.ID)
	must.NoError(T, err)

	mounted, err := do.Invoke[*mountedTransports](i)
	must.NoError(T, err)

	signIn := signInOverBufconn(T, mounted)

	T.Run("a token the service minted is its person", func(t *testing.T) {
		t.Parallel()

		ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+issued.Token)

		self, selfErr := signIn.GetSelf(ctx, &signinpb.GetSelfRequest{})
		must.NoError(t, selfErr)
		test.EqOp(t, reg.User.ID, self.GetUser().GetId())
	})

	T.Run("a request with no token has nobody on it", func(t *testing.T) {
		t.Parallel()

		_, selfErr := signIn.GetSelf(t.Context(), &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(selfErr))
	})

	T.Run("a token some other key signed has nobody on it", func(t *testing.T) {
		t.Parallel()

		ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+issued.Token+"x")

		_, selfErr := signIn.GetSelf(ctx, &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(selfErr))
	})
}

// TestRegisterTransports_applicationExtractorWins pins the two ways an
// application keeps the default out: naming an extractor, and registering its
// own *signingrpc.PrincipalExtractor.
func TestRegisterTransports_applicationExtractorWins(T *testing.T) {
	T.Parallel()

	T.Run("a Transports that names an extractor registers no default", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		RegisterTransports(i, &Transports{Extractor: withPrincipal})

		_, err := do.Invoke[*signingrpc.PrincipalExtractor](i)
		test.Error(t, err)
	})

	T.Run("an application's own registration is left in place", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		own := &signingrpc.PrincipalExtractor{}
		do.ProvideValue(i, own)

		RegisterTransports(i, &Transports{})

		got, err := do.Invoke[*signingrpc.PrincipalExtractor](i)
		must.NoError(t, err)
		test.EqOp(t, own, got)
	})
}

// signInOverBufconn serves what mounted on an in-process connection with no
// interceptor at all, which is the point: the default extractor reads the
// bearer token itself.
func signInOverBufconn(t *testing.T, mounted *mountedTransports) *signinclient.Client {
	t.Helper()

	server := grpc.NewServer()
	for _, register := range mounted.registrations {
		register(server)
	}

	listener := bufconn.Listen(1024 * 1024)

	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	must.NoError(t, err)

	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	})

	return signinclient.Wrap(conn)
}
