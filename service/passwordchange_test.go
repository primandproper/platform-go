package service

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signincfg "github.com/primandproper/platform-go/v14/authentication/signin/config"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/billing"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"
	"github.com/primandproper/platform-go/v14/callers"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ownMethod is an application's method, for the gate to be told about.
const ownMethod = "/consumer.v1.Profile/GetAvatar"

// signInInjector is a service configuring the sign-in block and nothing that
// would mount beside it.
func signInInjector(t *testing.T) do.Injector {
	t.Helper()

	cfg := &Config{
		Name:     "example",
		Database: sqliteDatabase(t),
		Identity: &identitycfg.Config{TablePrefix: storePrefix},
		Tokens:   testTokens(),
		SignIn: &signincfg.Config{
			TOTPIssuer:    "Example",
			RefreshTokens: signincfg.RefreshTokensConfig{TablePrefix: storePrefix},
			RecoveryCodes: signincfg.RecoveryCodesConfig{TablePrefix: storePrefix},
			Registration:  signincfg.RegistrationConfig{Disabled: true},
		},
	}
	must.NoError(t, cfg.ValidateWithContext(t.Context()))

	i := newInjector(t, cfg)
	do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())

	return i
}

// flagged is a caller on the request, as an authentication interceptor would
// have put one.
func flagged(ctx context.Context) context.Context {
	return context.WithValue(ctx, principalKey{}, &stubPrincipal{userID: "flagged"})
}

type stubPrincipal struct{ userID string }

func (p *stubPrincipal) UserID() string          { return p.userID }
func (p *stubPrincipal) Scope() tenancy.Scope    { return tenancy.Global() }
func (p *stubPrincipal) ActiveAccountID() string { return "" }

// owed is a reading that says every caller owes a change.
func owed(context.Context, callers.Principal) (bool, error) { return true, nil }

// gated runs method through the gate's unary interceptor as a flagged caller,
// reporting whether the handler was reached.
func gated(t *testing.T, gate *signingrpc.PasswordChangeGate, method string) (bool, error) {
	t.Helper()

	reached := false
	_, err := gate.UnaryServerInterceptor()(flagged(t.Context()), nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(context.Context, any) (any, error) {
			reached = true

			return nil, nil
		})

	return reached, err
}

func TestRegisterTransports_passwordChangeGate(T *testing.T) {
	T.Parallel()

	T.Run("sign-in mounting builds the gate, with the application's methods on it", func(t *testing.T) {
		t.Parallel()

		i := signInInjector(t)
		RegisterTransports(i, &Transports{
			Extractor:      withPrincipal,
			TenantOf:       DirectoryTenant,
			PasswordChange: PasswordChange{AllowedMethods: []string{ownMethod}},
		})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)
		must.NotNil(t, mounted.gate, must.Sprint("sign-in mounted and no gate was built"))

		test.True(t, mounted.gate.Allows(ownMethod))
		test.True(t, mounted.gate.Allows(signinpb.SignInService_UpdatePassword_FullMethodName))
		test.False(t, mounted.gate.Allows(signinpb.SignInService_RefreshTOTPSecret_FullMethodName))
	})

	T.Run("a named reading is the one the gate asks", func(t *testing.T) {
		t.Parallel()

		i := signInInjector(t)
		RegisterTransports(i, &Transports{
			Extractor:      withPrincipal,
			TenantOf:       DirectoryTenant,
			PasswordChange: PasswordChange{Required: owed},
		})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)
		must.NotNil(t, mounted.gate)

		reached, err := gated(t, mounted.gate, ownMethod)
		test.False(t, reached)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.ErrorIs(t, err, signin.ErrPasswordChangeRequired)
	})

	T.Run("Disabled builds none", func(t *testing.T) {
		t.Parallel()

		i := signInInjector(t)
		RegisterTransports(i, &Transports{
			Extractor:      withPrincipal,
			TenantOf:       DirectoryTenant,
			PasswordChange: PasswordChange{Disabled: true},
		})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.SliceContains(t, mounted.names, "sign-in gRPC")
		test.Nil(t, mounted.gate)
	})

	T.Run("a service with no sign-in surface builds none", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)
		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.Eq(t, []string{"billing gRPC"}, mounted.names)
		test.Nil(t, mounted.gate)
	})
}

func TestInnermostGate(T *testing.T) {
	T.Parallel()

	passUnary := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(ctx, req)
	}
	passStream := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, ss)
	}

	T.Run("no gate leaves the application's interceptors alone", func(t *testing.T) {
		t.Parallel()

		for _, mounted := range []*mountedTransports{nil, {}} {
			unary, stream := innermostGate(
				[]grpc.UnaryServerInterceptor{passUnary}, []grpc.StreamServerInterceptor{passStream}, mounted)

			test.SliceLen(t, 1, unary)
			test.SliceLen(t, 1, stream)
		}
	})

	T.Run("a gate goes last, without writing into the application's array", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(withPrincipal, owed)
		must.NoError(t, err)

		// Room to spare, so an unclipped append would land in this array.
		appUnary := make([]grpc.UnaryServerInterceptor, 1, 4)
		appUnary[0] = passUnary
		spare := appUnary[:2]

		unary, stream := innermostGate(appUnary, []grpc.StreamServerInterceptor{passStream}, &mountedTransports{gate: gate})
		must.SliceLen(t, 2, unary)
		must.SliceLen(t, 2, stream)
		test.Nil(t, spare[1], test.Sprint("the gate was written into the application's backing array"))

		// The last one is the gate: a flagged caller is refused by it.
		_, err = unary[1](flagged(t.Context()), nil, &grpc.UnaryServerInfo{FullMethod: ownMethod},
			func(context.Context, any) (any, error) { return nil, nil })
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})
}
