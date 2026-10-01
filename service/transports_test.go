package service

import (
	"context"
	"errors"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	oauth2clientscfg "github.com/primandproper/platform-go/v14/authentication/oauth2clients/config"
	"github.com/primandproper/platform-go/v14/authentication/passkeys"
	passkeyscfg "github.com/primandproper/platform-go/v14/authentication/passkeys/config"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetcfg "github.com/primandproper/platform-go/v14/authentication/passwordreset/config"
	passwordresetmock "github.com/primandproper/platform-go/v14/authentication/passwordreset/mock"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	signincfg "github.com/primandproper/platform-go/v14/authentication/signin/config"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	webauthnsessionscfg "github.com/primandproper/platform-go/v14/authentication/webauthnsessions/config"
	"github.com/primandproper/platform-go/v14/billing"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/comments"
	commentsmock "github.com/primandproper/platform-go/v14/comments/mock"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacycfg "github.com/primandproper/platform-go/v14/dataprivacy/config"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	dataprivacymock "github.com/primandproper/platform-go/v14/dataprivacy/mock"
	"github.com/primandproper/platform-go/v14/identity"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	identitymock "github.com/primandproper/platform-go/v14/identity/mock"
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	issuereportsmock "github.com/primandproper/platform-go/v14/issuereports/mock"
	"github.com/primandproper/platform-go/v14/links"
	linksmock "github.com/primandproper/platform-go/v14/links/mock"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationsmock "github.com/primandproper/platform-go/v14/notifications/mock"
	"github.com/primandproper/platform-go/v14/operations"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"
	operationsmock "github.com/primandproper/platform-go/v14/operations/mock"
	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	settingsmock "github.com/primandproper/platform-go/v14/settings/mock"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	waitlistsmock "github.com/primandproper/platform-go/v14/waitlists/mock"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v14/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	tokenscfg "github.com/primandproper/primitives-go/v2/authentication/tokens/config"
	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/authorization"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
	cachecfg "github.com/primandproper/primitives-go/v2/cache/config"
	"github.com/primandproper/primitives-go/v2/database"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/routing"
	"github.com/primandproper/primitives-go/v2/routing/backends/chi"
	grpcserver "github.com/primandproper/primitives-go/v2/server/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/uploads"
	uploadsmock "github.com/primandproper/primitives-go/v2/uploads/mock"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// testPrincipal is the three facts a surface reads off a caller.
type testPrincipal struct {
	userID  string
	account string
	scope   tenancy.Scope
}

func (p testPrincipal) UserID() string          { return p.userID }
func (p testPrincipal) Scope() tenancy.Scope    { return p.scope }
func (p testPrincipal) ActiveAccountID() string { return p.account }

// principalKey carries a principal on a context, which is where a consumer's
// authentication interceptor would have put one.
type principalKey struct{}

// withPrincipal is the extractor these tests mount with.
func withPrincipal(ctx context.Context) (callers.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(callers.Principal)

	return principal, ok
}

// discardConfirmations is an application's waitlist confirmation mailer, as
// little of one as mounting needs.
type discardConfirmations struct{}

func (discardConfirmations) SendConfirmation(context.Context, tenancy.Scope, *waitlistsgrpc.ConfirmationMail) error {
	return nil
}

// permissive satisfies every required authorizer, since what is under test here
// is which surfaces mount rather than what they refuse.
type permissive struct{}

func (permissive) AuthorizeAccount(context.Context, callers.Principal, string) error { return nil }

func (permissive) AuthorizeReport(context.Context, callers.Principal, *issuereports.Report) error {
	return nil
}

func (permissive) AuthorizeReporter(context.Context, callers.Principal, string) error { return nil }

func (permissive) AuthorizeSubject(context.Context, callers.Principal, settings.Subject) error {
	return nil
}

func (permissive) AuthorizeSubjectRead(
	context.Context,
	callers.Principal,
	tenancy.Scope,
	waitlists.Subject,
) error {
	return nil
}

func (permissive) AuthorizeWithdrawal(
	context.Context,
	callers.Principal,
	tenancy.Scope,
	string,
	string,
) error {
	return nil
}

// allAuthorizers is the four required seams, all satisfied.
func allAuthorizers() Authorizers {
	return Authorizers{
		BillingAccounts:  permissive{},
		IssueReports:     permissive{},
		SettingsSubjects: permissive{},
		WaitlistSignups:  permissive{},
	}
}

// newRouter builds the router the HTTP surfaces mount on, the way
// operations/http's own tests build one.
func newRouter() *routing.Router {
	backend := chi.NewBackend(&chi.Config{ServiceName: "transports-test"})

	return routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))
}

// newTransportInjector registers the observability every surface reports
// through and nothing else, so each test below says what it configures.
func newTransportInjector(t *testing.T) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	Register(i, &Config{Name: "example"})

	return i
}

// errBrokenStore stands in for a store that was registered and cannot be built,
// which is the failure the mount rule tells apart from an absence.
var errBrokenStore = platformerrors.New("the store under test refuses to build")

func TestRegisterTransports(T *testing.T) {
	T.Parallel()

	T.Run("a service configuring nothing mounts nothing, and that is not an error", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)
		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.SliceEmpty(t, mounted.names)
		test.SliceEmpty(t, mounted.registrations)
	})

	T.Run("mounts every surface whose dependencies resolve, in mount order", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue(i, newRouter())
		do.ProvideValue[uploads.UploadManager](i, &uploadsmock.UploadManagerMock{})

		do.ProvideValue[audit.Reader](i, &auditmock.ReaderMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})
		do.ProvideValue[comments.Store](i, &commentsmock.StoreMock{})
		do.ProvideValue[issuereports.Store](i, &issuereportsmock.StoreMock{})
		do.ProvideValue[notifications.Inbox](i, &notificationsmock.InboxMock{})
		do.ProvideValue[notifications.Registry](i, &notificationsmock.RegistryMock{})
		do.ProvideValue[settings.Store](i, &settingsmock.StoreMock{})
		do.ProvideValue[waitlists.Store](i, &waitlistsmock.StoreMock{})
		do.ProvideValue[webhooks.Dispatcher](i, &webhooksmock.DispatcherMock{})
		do.ProvideValue[webhooks.Store](i, &webhooksmock.StoreMock{})

		do.ProvideValue[dataprivacy.Service](i, &dataprivacymock.ServiceMock{})
		do.ProvideValue[mediaregistry.Store](i, &mediaregistrymock.StoreMock{})
		do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		// The gRPC lane then the HTTP one, alphabetical within each. identity,
		// oauth2 clients, passkeys, password reset and sign-in are absent
		// because their services are: see the subtest below.
		test.Eq(t, []string{
			"audit gRPC",
			"billing gRPC",
			"comments gRPC",
			"issue reports gRPC",
			"media uploads gRPC",
			"notifications gRPC",
			"settings gRPC",
			"waitlists gRPC",
			"webhooks gRPC",
			"data privacy HTTP",
			"media registry HTTP",
			"operations HTTP",
		}, mounted.names)

		// One registration per mounted gRPC surface, and none for the HTTP
		// ones, which are already on the router.
		test.SliceLen(t, 9, mounted.registrations)
	})

	// The surfaces above are mounted off a store, which a mock satisfies. The five
	// mounted off a *Service are not, because a service is a concrete type — so
	// none of them appears in that list. Password reset is the one whose service
	// assembles out of doubles, so it is the one that can pin the property they
	// all share: a provided service is a mounted surface.
	T.Run("a surface mounted off a provided service", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})

		svc, err := passwordreset.NewService(
			&databasemock.ClientMock{},
			&passwordresetmock.StoreMock{},
			stubResetDirectory{},
			argon2.NewArgon2Authenticator(),
			stubResetMailer{},
		)
		must.NoError(t, err)

		do.ProvideValue(i, svc)

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		// No extractor was needed for it, which is the half worth pinning: it is
		// the only surface here that mounts without one.
		test.Eq(t, []string{"password reset gRPC"}, mounted.names)
		test.SliceLen(t, 1, mounted.registrations)
	})

	T.Run("the reset surface mounts from its config block and the application's two", func(t *testing.T) {
		t.Parallel()

		// Nothing about the service is hand-built: the block registers the store
		// and the service, Identity supplies the directory, and the application
		// registers only what no environment variable can name.
		cfg := &Config{
			Name:          "example",
			Database:      sqliteDatabase(t),
			Identity:      &identitycfg.Config{TablePrefix: storePrefix},
			PasswordReset: &passwordresetcfg.Config{TablePrefix: storePrefix},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)
		do.ProvideValue[passwordreset.Mailer](i, stubResetMailer{})
		do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())

		RegisterTransports(i, &Transports{})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.Eq(t, []string{"password reset gRPC"}, mounted.names)
		test.SliceLen(t, 1, mounted.registrations)
	})

	T.Run("the sign-in surface mounts from its config block beside the reset flow's", func(t *testing.T) {
		t.Parallel()

		// Nothing about either service is hand-built. The application registers
		// one authenticator, and both blocks resolve it, so a reset writes a
		// password this sign-in can check. It also registers the two mailers,
		// and nothing else, so registration is disabled: open registration
		// would need identity's service, which only the application registers.
		cfg := &Config{
			Name:          "example",
			Database:      sqliteDatabase(t),
			Identity:      &identitycfg.Config{TablePrefix: storePrefix},
			Tokens:        testTokens(),
			PasswordReset: &passwordresetcfg.Config{TablePrefix: storePrefix},
			SignIn: &signincfg.Config{
				DefaultOwnerRoles: []string{"owner"},
				TOTPIssuer:        "Example",
				RefreshTokens:     signincfg.RefreshTokensConfig{TablePrefix: storePrefix},
				MagicLinks:        &signincfg.MagicLinksConfig{TablePrefix: storePrefix},
				RecoveryCodes:     signincfg.RecoveryCodesConfig{TablePrefix: storePrefix},
				Registration:      signincfg.RegistrationConfig{Disabled: true},
			},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)
		do.ProvideValue[passwordreset.Mailer](i, stubResetMailer{})
		do.ProvideValue[signin.MagicLinkMailer](i, stubMagicLinkMailer{})
		do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.SliceContainsAll(t, []string{"password reset gRPC", "sign-in gRPC"}, mounted.names)
		test.SliceLen(t, 2, mounted.registrations)

		// The operator half of sign-in mounts with it: one surface, two
		// services, the second gated by permissions no role holds by default.
		srv := grpc.NewServer()
		t.Cleanup(srv.Stop)

		for _, register := range mounted.registrations {
			register(srv)
		}

		services := srv.GetServiceInfo()
		test.MapContainsKey(t, services, signinpb.SignInService_ServiceDesc.ServiceName)
		test.MapContainsKey(t, services, signinpb.SignInAdministrationService_ServiceDesc.ServiceName)

		// Registration is disabled, so the mounted door is closed by name
		// rather than reaching a service with no registrar and answering a 500
		// a client could not tell from a broken server.
		client := signinpb.NewSignInServiceClient(serveMounted(t, i, "sign-in gRPC", nil))

		_, err = client.Register(t.Context(), &signinpb.RegisterRequest{})
		test.EqOp(t, codes.Unimplemented, status.Code(err))
		test.EqOp(t, signin.ErrRegistrationClosed.Error(), status.Convert(err).Message())
	})

	T.Run("the passkeys surface mounts from its config block beside sign-in's", func(t *testing.T) {
		t.Parallel()

		// The WebAuthn block supplies the relying party, SignIn the issuer a
		// finished login mints through, and the application registers only the
		// resolver and the gate no environment variable can name.
		cfg := &Config{
			Name:     "example",
			Database: sqliteDatabase(t),
			Identity: &identitycfg.Config{TablePrefix: storePrefix},
			Tokens:   testTokens(),
			SignIn: &signincfg.Config{
				DefaultOwnerRoles: []string{"owner"},
				TOTPIssuer:        "Example",
				RefreshTokens:     signincfg.RefreshTokensConfig{TablePrefix: storePrefix},
				RecoveryCodes:     signincfg.RecoveryCodesConfig{TablePrefix: storePrefix},
				Registration:      signincfg.RegistrationConfig{Disabled: true},
			},
			WebAuthn: &webauthnsessionscfg.Config{
				Provider: webauthnsessionscfg.ProviderCache,
				RelyingParty: webauthn.Config{
					RPID:          "localhost",
					RPDisplayName: "Example",
					RPOrigins:     []string{"http://localhost:8080"},
				},
				Cache: cachecfg.Config{Provider: cachecfg.ProviderMemory},
			},
			Passkeys: &passkeyscfg.Config{TablePrefix: storePrefix},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)
		do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())
		do.ProvideValue[passkeys.UserResolver](i, func(context.Context, []byte) (passkeys.UserIdentity, error) {
			return passkeys.UserIdentity{}, errors.New("nobody")
		})
		do.ProvideValue[passkeys.EnrollmentGate](i, passkeys.AdmitEveryEnrollment)

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.SliceContainsAll(t, []string{"passkeys gRPC", "sign-in gRPC"}, mounted.names)
		test.SliceLen(t, 2, mounted.registrations)
	})

	T.Run("a passkey service with no sign-in service beside it fails rather than staying absent", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue(i, &passkeys.Service{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		_, err := do.Invoke[*mountedTransports](i)
		test.ErrorIs(t, err, ErrPasskeysNeedSignIn)
	})

	T.Run("a sign-in block with no authenticator fails naming it rather than defaulting", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Name:     "example",
			Database: sqliteDatabase(t),
			Identity: &identitycfg.Config{TablePrefix: storePrefix},
			Tokens:   testTokens(),
			SignIn:   &signincfg.Config{DefaultOwnerRoles: []string{"owner"}, TOTPIssuer: "Example"},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant})

		_, err := do.Invoke[*mountedTransports](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[authentication.Authenticator]())
	})

	T.Run("a surface whose store is unconfigured does not mount, and that is not an error", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.Eq(t, []string{"billing gRPC"}, mounted.names)
	})

	T.Run("the client registry mounts from its config block alone", func(t *testing.T) {
		t.Parallel()

		// The pair it mounts over is a service and a store, and a table prefix
		// is all either needs, so a Config naming OAuth2Clients is enough to put
		// the registry on the wire with nothing registered by hand.
		cfg := &Config{
			Name:          "example",
			Database:      sqliteDatabase(t),
			OAuth2Clients: &oauth2clientscfg.Config{TablePrefix: storePrefix},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.Eq(t, []string{"oauth2 clients gRPC"}, mounted.names)
		test.SliceLen(t, 1, mounted.registrations)
	})

	T.Run("a surface whose service is unregistered does not mount, though its store is", func(t *testing.T) {
		t.Parallel()

		// identity is the case worth pinning: Register registers the store and
		// not the service, so a Config naming Identity is not on its own enough
		// to put the directory on the wire.
		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[identity.Store](i, &identitymock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.SliceEmpty(t, mounted.names)
	})

	T.Run("a configured surface with no authorizer is a startup error", func(t *testing.T) {
		t.Parallel()

		// Each of the four required authorizers, left out one at a time, under
		// the surface's own sentinel rather than one invented here.
		for _, tc := range []struct {
			provide func(do.Injector)
			seams   Authorizers
			want    error
			name    string
		}{
			{
				name:    "billing",
				provide: func(i do.Injector) { do.ProvideValue[billing.Store](i, &billingmock.StoreMock{}) },
				seams:   Authorizers{IssueReports: permissive{}, SettingsSubjects: permissive{}, WaitlistSignups: permissive{}},
				want:    billinggrpc.ErrNilAccountAuthorizer,
			},
			{
				name:    "issue reports",
				provide: func(i do.Injector) { do.ProvideValue[issuereports.Store](i, &issuereportsmock.StoreMock{}) },
				seams:   Authorizers{BillingAccounts: permissive{}, SettingsSubjects: permissive{}, WaitlistSignups: permissive{}},
				want:    issuereportsgrpc.ErrNilReportAuthorizer,
			},
			{
				name:    "settings",
				provide: func(i do.Injector) { do.ProvideValue[settings.Store](i, &settingsmock.StoreMock{}) },
				seams:   Authorizers{BillingAccounts: permissive{}, IssueReports: permissive{}, WaitlistSignups: permissive{}},
				want:    settingsgrpc.ErrNilSubjectAuthorizer,
			},
			{
				name:    "waitlists",
				provide: func(i do.Injector) { do.ProvideValue[waitlists.Store](i, &waitlistsmock.StoreMock{}) },
				seams:   Authorizers{BillingAccounts: permissive{}, IssueReports: permissive{}, SettingsSubjects: permissive{}},
				want:    waitlistsgrpc.ErrNilSignupAuthorizer,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				i := newTransportInjector(t)
				do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
				tc.provide(i)

				RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: tc.seams})

				_, err := do.Invoke[*mountedTransports](i)
				must.ErrorIs(t, err, tc.want)
			})
		}
	})

	T.Run("a registered waitlist confirmation mailer mounts the loop over the configured minter", func(t *testing.T) {
		t.Parallel()

		minter := func(t *testing.T, actions ...links.Action) *links.Minter {
			t.Helper()

			policies := map[links.Action]links.ActionPolicy{}
			for _, action := range actions {
				policies[action] = links.ActionPolicy{
					URL: "https://example.com/" + string(action) + "/{token}",
					TTL: links.Duration(time.Hour),
				}
			}

			m, err := links.NewMinter(&linksmock.StoreMock{}, links.WithActions(policies))
			must.NoError(t, err)

			return m
		}

		mount := func(t *testing.T, provide func(do.Injector)) (*mountedTransports, error) {
			t.Helper()

			i := newTransportInjector(t)
			do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
			do.ProvideValue[waitlists.Store](i, &waitlistsmock.StoreMock{})
			do.ProvideValue[waitlistsgrpc.ConfirmationMailer](i, discardConfirmations{})
			provide(i)

			RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

			return do.Invoke[*mountedTransports](i)
		}

		t.Run("with no minter it is a startup error rather than a loop that never mails", func(t *testing.T) {
			t.Parallel()

			_, err := mount(t, func(do.Injector) {})
			test.ErrorIs(t, err, ErrWaitlistConfirmationNeedsLinks)
		})

		t.Run("with a minter declaring the two actions it mounts", func(t *testing.T) {
			t.Parallel()

			mounted, err := mount(t, func(i do.Injector) {
				do.ProvideValue(i, minter(t, waitlistsgrpc.ConfirmAction, waitlistsgrpc.UnsubscribeAction))
			})
			must.NoError(t, err)
			test.Eq(t, []string{"waitlists gRPC"}, mounted.names)
		})

		// The surface's own refusal, which is how this proves the minter
		// actually reached WithConfirmation rather than being resolved and
		// dropped.
		t.Run("with a minter missing an action it is the surface's refusal", func(t *testing.T) {
			t.Parallel()

			_, err := mount(t, func(i do.Injector) {
				do.ProvideValue(i, minter(t, waitlistsgrpc.ConfirmAction))
			})
			test.ErrorIs(t, err, waitlistsgrpc.ErrConfirmationActionMissing)
		})
	})

	T.Run("an optional authorizer left nil leaves the surface's own default in place", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[comments.Store](i, &commentsmock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.Eq(t, []string{"comments gRPC"}, mounted.names)
	})

	T.Run("a configured surface with no extractor is a startup error", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})

		RegisterTransports(i, &Transports{Authorizers: allAuthorizers()})

		_, err := do.Invoke[*mountedTransports](i)
		must.ErrorIs(t, err, ErrNilPrincipalExtractor)
	})

	// A nil TenantOf is refused by each of the three surfaces that read the
	// tenant, rather than read as the directory: a deployment whose tenant is
	// the directory says so by naming DirectoryTenant.
	T.Run("a surface that reads the tenant with no TenantOf is a startup error", func(t *testing.T) {
		t.Parallel()

		provideMedia := func(i do.Injector) {
			do.ProvideValue[mediaregistry.Store](i, &mediaregistrymock.StoreMock{})
		}

		// The media registry's two surfaces are built from the same store, so
		// each is asserted with the other skipped: otherwise the gRPC lane's
		// refusal is the only one either case could ever observe.
		for surface, c := range map[string]struct {
			provide func(do.Injector)
			skip    []Surface
		}{
			"audit": {provide: func(i do.Injector) {
				do.ProvideValue[audit.Reader](i, &auditmock.ReaderMock{})
			}},
			"media registry": {provide: provideMedia, skip: []Surface{SurfaceMediaUploads}},
			"media uploads":  {provide: provideMedia, skip: []Surface{SurfaceMediaRegistry}},
			"operations": {provide: func(i do.Injector) {
				do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})
			}},
		} {
			t.Run(surface, func(t *testing.T) {
				t.Parallel()

				i := newTransportInjector(t)

				do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
				do.ProvideValue(i, newRouter())
				do.ProvideValue[uploads.UploadManager](i, &uploadsmock.UploadManagerMock{})
				c.provide(i)

				RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers(), Skip: c.skip})

				_, err := do.Invoke[*mountedTransports](i)
				must.ErrorIs(t, err, ErrNilTenantOf)
				test.StrContains(t, err.Error(), surface)
			})
		}
	})

	T.Run("no TenantOf is not an error for a service mounting nothing that reads the tenant", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.Eq(t, []string{"billing gRPC"}, mounted.names)
	})

	T.Run("no extractor is not an error for a service with nothing to mount", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)
		RegisterTransports(i, &Transports{})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.SliceEmpty(t, mounted.names)
	})

	T.Run("the application's own gRPC services join the platform's", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})

		var own int

		RegisterTransports(i, &Transports{
			Extractor:     withPrincipal,
			TenantOf:      DirectoryTenant,
			Authorizers:   allAuthorizers(),
			Registrations: []grpcserver.RegistrationFunc{func(*grpc.Server) { own++ }},
		})

		registrations, err := do.Invoke[[]grpcserver.RegistrationFunc](i)
		must.NoError(t, err)
		must.SliceLen(t, 2, registrations)

		// The application's is last, which is the half its author can move when
		// a service name is declared on both ends.
		registrations[1](nil)
		test.EqOp(t, 1, own)
	})

	T.Run("an injector that already holds the gRPC registrations is a startup error", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})
		do.ProvideValue(i, []grpcserver.RegistrationFunc{func(*grpc.Server) {}})

		// It does not panic on the way in, which is the whole point: the
		// duplicate is reported where every other startup failure is.
		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		_, err := do.Invoke[*mountedTransports](i)
		must.ErrorIs(t, err, ErrGRPCRegistrationsAlreadyProvided)
	})

	T.Run("a route the router refuses is reported rather than quietly not mounted", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue(i, newRouter())
		do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})

		seams := &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()}

		// The first pass puts operations' routes on the router. The second
		// mounts the same ones over them, which routing.Router records and
		// goes on from — so without the check this would be a service serving
		// one copy of a route it registered twice and reporting nothing.
		_, err := mountTransports(i, seams)
		must.NoError(t, err)

		_, err = mountTransports(i, seams)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "operations")
	})

	// The other half of the same guarantee. Once a router is carrying a failure,
	// Err joins everything after it, so no later surface can be told apart from
	// whatever broke it first — and the honest answer is to refuse the lane
	// rather than to name the next surface to mount or, worse, to stop looking.
	T.Run("a router that arrives already broken refuses the lane rather than a surface", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue(i, newRouter())
		do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})

		seams := &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()}

		// One clean pass, then the pass that breaks the router and is told so.
		_, err := mountTransports(i, seams)
		must.NoError(t, err)

		_, err = mountTransports(i, seams)
		must.Error(t, err)

		// The third arrives at a router that was already broken before it
		// touched anything, which is the state that used to be skipped over.
		_, err = mountTransports(i, seams)
		must.Error(t, err)
		test.ErrorIs(t, err, ErrRouterAlreadyFailed)

		// The failure underneath is kept, because the reader needs the route
		// rather than only the news that there was one.
		test.StrContains(t, err.Error(), "operations")

		// And no surface is blamed for it: nothing here had mounted yet.
		test.StrNotContains(t, err.Error(), "building the")
	})

	T.Run("a registered store that cannot be built is an error naming it", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.Provide(i, func(do.Injector) (billing.Store, error) {
			return nil, errBrokenStore
		})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		_, err := do.Invoke[*mountedTransports](i)
		must.ErrorIs(t, err, errBrokenStore)
	})
}

func TestDerivedSeams(T *testing.T) {
	T.Parallel()

	caller := testPrincipal{userID: "user_1", scope: tenancy.Of("tenant_1"), account: "account_1"}
	withCaller := context.WithValue(context.Background(), principalKey{}, callers.Principal(caller))

	accountOf := func(principal callers.Principal) (tenancy.Scope, error) {
		return tenancy.Of(principal.ActiveAccountID()), nil
	}

	T.Run("the directory, for a deployment that names it its tenant", func(t *testing.T) {
		t.Parallel()

		scope, err := deriveScope(withPrincipal, DirectoryTenant)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, caller.scope, scope)
	})

	T.Run("the tenant the application reads off the caller rather than the directory", func(t *testing.T) {
		t.Parallel()

		// The directory the caller is in and the tenant the request is against
		// are different answers, which is the whole reason the field exists.
		scope, err := deriveScope(withPrincipal, accountOf)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, tenancy.Of("account_1"), scope)
		test.NotEqOp(t, caller.scope, scope)
	})

	T.Run("the subject a privacy request is about", func(t *testing.T) {
		t.Parallel()

		subject, err := deriveSubject(withPrincipal)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, dataprivacy.Subject{ID: "user_1", Type: dataprivacy.SubjectUser}, subject)
	})

	T.Run("the caller a media request is from, on the directory", func(t *testing.T) {
		t.Parallel()

		mediaCaller, err := deriveMediaCaller(withPrincipal, DirectoryTenant)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, mediaregistryhttp.Caller{PrincipalID: "user_1", Scope: caller.scope}, mediaCaller)
	})

	T.Run("a media caller carries the application's tenant rather than the directory", func(t *testing.T) {
		t.Parallel()

		mediaCaller, err := deriveMediaCaller(withPrincipal, accountOf)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, mediaregistryhttp.Caller{PrincipalID: "user_1", Scope: tenancy.Of("account_1")}, mediaCaller)
	})

	T.Run("an application's refusal is carried out of every surface's seam", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("this caller belongs to no tenant")
		refuse := func(callers.Principal) (tenancy.Scope, error) { return tenancy.Scope{}, sentinel }

		_, err := deriveScope(withPrincipal, refuse)(withCaller)
		test.ErrorIs(t, err, sentinel)

		_, err = deriveMediaCaller(withPrincipal, refuse)(withCaller)
		test.ErrorIs(t, err, sentinel)
	})

	T.Run("an application tenant that names nothing is refused rather than carried to the store", func(t *testing.T) {
		t.Parallel()

		// A caller with no active account, read through tenancy.Of, is the zero
		// scope: undecided, not global.
		noAccount := testPrincipal{userID: "user_1", scope: tenancy.Global()}
		withNoAccount := context.WithValue(context.Background(), principalKey{}, callers.Principal(noAccount))

		_, err := deriveScope(withPrincipal, accountOf)(withNoAccount)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = deriveMediaCaller(withPrincipal, accountOf)(withNoAccount)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("a request with nobody on it is refused before the application is asked", func(t *testing.T) {
		t.Parallel()

		nobody := context.Background()
		asked := func(callers.Principal) (tenancy.Scope, error) {
			t.Error("the application's tenant must not be asked for a request with nobody on it")

			return tenancy.Global(), nil
		}

		for _, tenantOf := range []func(callers.Principal) (tenancy.Scope, error){DirectoryTenant, asked} {
			_, err := deriveScope(withPrincipal, tenantOf)(nobody)
			test.ErrorIs(t, err, ErrNoPrincipal)

			_, err = deriveMediaCaller(withPrincipal, tenantOf)(nobody)
			test.ErrorIs(t, err, ErrNoPrincipal)
		}

		_, err := deriveSubject(withPrincipal)(nobody)
		test.ErrorIs(t, err, ErrNoPrincipal)
	})

	// A privacy request's operation is owned by the person and an
	// application's by the tenant, so a caller follows both — and only those.
	T.Run("the owners a caller follows are their tenant and themselves", func(t *testing.T) {
		t.Parallel()

		owners, err := deriveOwners(withPrincipal, DirectoryTenant)(withCaller)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{caller.scope, tenancy.Of(caller.userID)}, owners)

		_, err = deriveOwners(withPrincipal, DirectoryTenant)(context.Background())
		test.ErrorIs(t, err, callers.ErrNoPrincipal)
	})

	// The refusal is for want of a caller, and says so on both transports. Each
	// of the four surfaces behind these seams falls back to a code written for a
	// resolver that failed — InvalidArgument from audit, a 500 from
	// mediaregistry — and it is callers' mapper that outranks it.
	T.Run("a request with nobody on it reaches a client as unauthenticated", func(t *testing.T) {
		t.Parallel()

		_, err := deriveScope(withPrincipal, DirectoryTenant)(context.Background())
		must.ErrorIs(t, err, callers.ErrNoPrincipal)

		grpcCode, ok := callers.GRPCMapper.Map(err)
		test.True(t, ok)
		test.EqOp(t, codes.Unauthenticated, grpcCode)

		httpCode, _, ok := callers.HTTPMapper.Map(err)
		test.True(t, ok)
		test.EqOp(t, httperrors.ErrFetchingSessionContextData, httpCode)
	})
}

func TestNew_transports(T *testing.T) {
	T.Parallel()

	T.Run("builds the surfaces and records what it mounted", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())

		cfg := &Config{Name: "example"}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
		Register(i, cfg)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})

		svc, err := New(i)
		must.NoError(t, err)

		// RegisterTransports runs after Register recorded its delta, so the
		// surfaces are not among the names resolveRegistered replays. This is
		// the assertion that New builds them anyway.
		test.Eq(t, []string{"billing gRPC"}, svc.surfaces)
	})

	T.Run("a surface that cannot be built fails the boot", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())

		cfg := &Config{Name: "example"}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
		Register(i, cfg)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[settings.Store](i, &settingsmock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant})

		_, err := New(i)
		must.ErrorIs(t, err, settingsgrpc.ErrNilSubjectAuthorizer)
	})
}

// stubResetDirectory and stubResetMailer are the two seams
// passwordreset.NewService refuses to be built without. Neither is called: the
// test that uses them mounts a surface and makes no request through it.
type stubResetDirectory struct{}

func (stubResetDirectory) GetUserByEmailAddress(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
) (*identity.User, error) {
	return nil, nil
}

func (stubResetDirectory) UpdateUserPassword(
	context.Context, database.Tx, tenancy.Scope, string, string,
) error {
	return nil
}

type stubResetMailer struct{}

func (stubResetMailer) SendPasswordReset(context.Context, *passwordreset.Mail) error { return nil }

type stubMagicLinkMailer struct{}

func (stubMagicLinkMailer) SendMagicLink(context.Context, *signin.MagicLinkMail) error { return nil }

// testTokens is a JWT issuer with a fixed key, which is enough for a sign-in
// service to be built over. Nothing here mints a token.
func testTokens() *tokenscfg.Config {
	return &tokenscfg.Config{
		Provider:                tokenscfg.ProviderJWT,
		Issuer:                  "example",
		Audience:                "example",
		Base64EncodedSigningKey: "c2lnbmluZy1rZXktZm9yLXNlcnZpY2UtdGVzdHMtMzI=",
	}
}

// TestRegisterTransports_tenantOfReachesTheMountedSurface is the assertion the
// unit tests above cannot make: that the three surfaces meaning the tenant are
// mounted with Transports.TenantOf, and that supplying it does not open them to
// a request with nobody on it.
//
// A context value does not cross a connection, so the principal arrives on the
// server side the way it does in a deployment: an interceptor puts it there.
// Without that interceptor there is no principal, and the surface must refuse
// whether or not the application supplied a tenant.
func TestRegisterTransports_tenantOfReachesTheMountedSurface(T *testing.T) {
	T.Parallel()

	const tenant = "acct_the_studio"

	caller := testPrincipal{userID: "user_1", scope: tenancy.Global(), account: tenant}
	accountOf := func(principal callers.Principal) (tenancy.Scope, error) {
		return tenancy.Of(principal.ActiveAccountID()), nil
	}

	T.Run("audit reads the tenant the application read off the caller", func(t *testing.T) {
		t.Parallel()

		var asked *tenancy.Scope

		reader := &auditmock.ReaderMock{
			ListFunc: func(_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, _ *audit.Query, _ *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
				asked = &scope

				return &filtering.QueryFilteredResult[audit.Entry]{Data: []*audit.Entry{}}, nil
			},
		}

		client := auditServiceOverBufconn(t, reader, caller, &Transports{
			Extractor:   withPrincipal,
			Authorizers: allAuthorizers(),
			TenantOf:    accountOf,
		})

		_, err := client.ListEntries(t.Context(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		must.NotNil(t, asked, must.Sprint("the reader was never asked"))
		test.EqOp(t, tenancy.Of(tenant), *asked)
	})

	T.Run("a request with nobody on it is refused even when the application reads tenants", func(t *testing.T) {
		t.Parallel()

		reader := &auditmock.ReaderMock{
			ListFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, *audit.Query, *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
				t.Error("the reader must not be consulted for a request with nobody on it")

				return nil, nil
			},
		}

		client := auditServiceOverBufconn(t, reader, nil, &Transports{
			Extractor:   withPrincipal,
			Authorizers: allAuthorizers(),
			TenantOf: func(callers.Principal) (tenancy.Scope, error) {
				t.Error("the application's tenant must not be asked for a request with nobody on it")

				return tenancy.Of(tenant), nil
			},
		})

		_, err := client.ListEntries(t.Context(), &auditpb.ListEntriesRequest{})
		must.Error(t, err, must.Sprint("a request with no principal has no tenant to be against"))
	})

	T.Run("a deployment that names the directory its tenant reads the directory", func(t *testing.T) {
		t.Parallel()

		var asked *tenancy.Scope

		reader := &auditmock.ReaderMock{
			ListFunc: func(_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, _ *audit.Query, _ *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
				asked = &scope

				return &filtering.QueryFilteredResult[audit.Entry]{Data: []*audit.Entry{}}, nil
			},
		}

		client := auditServiceOverBufconn(t, reader, caller, &Transports{
			Extractor:   withPrincipal,
			Authorizers: allAuthorizers(),
			TenantOf:    DirectoryTenant,
		})

		_, err := client.ListEntries(t.Context(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		must.NotNil(t, asked, must.Sprint("the reader was never asked"))
		test.EqOp(t, tenancy.Global(), *asked)
	})
}

// auditServiceOverBufconn mounts the transports and serves them on an
// in-process connection, returning a client for the audit surface.
//
// A non-nil caller is put on every request's server-side context by an
// interceptor, standing in for the authentication interceptor a deployment
// installs; nil leaves the requests with nobody on them.
//
// The audit config block is deliberately absent: leaving it out is what stops
// Register providing its own reader, so the test's can be the one the surface
// mounts over.
func auditServiceOverBufconn(
	t *testing.T,
	reader audit.Reader,
	caller callers.Principal,
	transports *Transports,
) auditpb.AuditServiceClient {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())

	cfg := &Config{Name: "example"}
	must.NoError(t, cfg.ValidateWithContext(t.Context()))
	Register(i, cfg)

	do.ProvideValue[database.Client](i, &databasemock.ClientMock{
		ReaderFunc: func() database.SQLQueryExecutor { return &databasemock.SQLQueryExecutorMock{} },
	})
	do.ProvideValue(i, reader)

	RegisterTransports(i, transports)

	return auditpb.NewAuditServiceClient(serveMounted(t, i, "audit gRPC", caller))
}

// serveMounted serves what RegisterTransports mounted on an in-process
// connection, having checked that surface is among it, and returns the
// connection.
//
// A non-nil caller is put on every request's server-side context by an
// interceptor, standing in for the authentication interceptor a deployment
// installs; nil leaves the requests with nobody on them.
func serveMounted(t *testing.T, i do.Injector, surface string, caller callers.Principal) *grpc.ClientConn {
	t.Helper()

	mounted, err := do.Invoke[*mountedTransports](i)
	must.NoError(t, err)
	must.SliceContains(t, mounted.names, surface)

	var opts []grpc.ServerOption
	if caller != nil {
		opts = append(opts, grpc.UnaryInterceptor(
			func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				return handler(context.WithValue(ctx, principalKey{}, caller), req)
			},
		))
	}

	server := grpc.NewServer(opts...)
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

	return conn
}

// TestRegisterTransports_dataPrivacyArtifactRoute pins that the privacy surface
// serves its artifact route whatever artifact storage was or was not
// registered: the route is part of dataprivacy/http's Mount, because a subject
// who cannot collect their export has not been given it.
func TestRegisterTransports_dataPrivacyArtifactRoute(T *testing.T) {
	T.Parallel()

	caller := testPrincipal{userID: "user_1", scope: tenancy.Global(), account: "acct_1"}

	serve := func(t *testing.T, storage *dataprivacycfg.ArtifactStorage) nethttp.Handler {
		t.Helper()

		i := newTransportInjector(t)

		router := newRouter()
		do.ProvideValue(i, router)

		if storage != nil {
			do.ProvideValue(i, storage)
		}

		export := &dataprivacy.Request{
			ID:          "r1",
			Type:        dataprivacy.RequestExport,
			Subject:     dataprivacy.Subject{ID: caller.userID, Type: dataprivacy.SubjectUser},
			Status:      dataprivacy.StatusCompleted,
			ArtifactRef: "privacy-exports/r1.json",
		}

		do.ProvideValue[dataprivacy.Service](i, &dataprivacymock.ServiceMock{
			GetFunc: func(context.Context, *tenancy.Scope, string) (*dataprivacy.Request, error) {
				return export, nil
			},
			DownloadFunc: func(context.Context, *tenancy.Scope, string) (string, error) {
				return "https://storage.example/signed", nil
			},
		})

		_, err := mountTransports(i, &Transports{Extractor: withPrincipal, TenantOf: DirectoryTenant, Authorizers: allAuthorizers()})
		must.NoError(t, err)

		return nethttp.HandlerFunc(func(res nethttp.ResponseWriter, req *nethttp.Request) {
			router.Handler().ServeHTTP(res, req.WithContext(context.WithValue(req.Context(), principalKey{}, callers.Principal(caller))))
		})
	}

	download := func(t *testing.T, handler nethttp.Handler) int {
		t.Helper()

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet,
			dataprivacyhttp.BasePath+"/r1"+dataprivacyhttp.ArtifactSuffix, nethttp.NoBody))

		return res.Code
	}

	T.Run("mounted with artifact storage of its own", func(t *testing.T) {
		t.Parallel()

		handler := serve(t, &dataprivacycfg.ArtifactStorage{Manager: &uploadsmock.UploadManagerMock{}})

		test.EqOp(t, nethttp.StatusSeeOther, download(t, handler))
	})

	T.Run("mounted with none registered", func(t *testing.T) {
		t.Parallel()

		handler := serve(t, nil)

		test.EqOp(t, nethttp.StatusSeeOther, download(t, handler))
	})
}

// TestRegisterTransports_httpEnforcerReachesEveryHTTPSurface is the assertion
// that Transports.HTTPEnforcer is the enforcer each HTTP surface checks its
// routes with, and that leaving it out refuses them rather than serving them.
//
// The enforcer's deny handler answers with a status nothing else here writes,
// so a refusal that arrives with it is one this enforcer made, on that surface.
func TestRegisterTransports_httpEnforcerReachesEveryHTTPSurface(T *testing.T) {
	T.Parallel()

	caller := testPrincipal{userID: "user_1", scope: tenancy.Global(), account: "acct_1"}

	// One guarded route per surface, at its default base path.
	guarded := map[string]string{
		"dataprivacy":   dataprivacyhttp.RouteList,
		"mediaregistry": mediaregistryhttp.RouteServe,
		"operations":    operationshttp.RouteList,
	}

	serve := func(t *testing.T, enforcer *authzhttp.Enforcer) nethttp.Handler {
		t.Helper()

		i := newTransportInjector(t)

		router := newRouter()
		do.ProvideValue(i, router)
		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[uploads.UploadManager](i, &uploadsmock.UploadManagerMock{})
		do.ProvideValue[dataprivacy.Service](i, &dataprivacymock.ServiceMock{})
		do.ProvideValue[mediaregistry.Store](i, &mediaregistrymock.StoreMock{})
		do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})

		_, err := mountTransports(i, &Transports{
			Extractor:    withPrincipal,
			TenantOf:     DirectoryTenant,
			Authorizers:  allAuthorizers(),
			HTTPEnforcer: enforcer,
		})
		must.NoError(t, err)

		return nethttp.HandlerFunc(func(res nethttp.ResponseWriter, req *nethttp.Request) {
			router.Handler().ServeHTTP(res, req.WithContext(context.WithValue(req.Context(), principalKey{}, callers.Principal(caller))))
		})
	}

	request := func(t *testing.T, handler nethttp.Handler, route string) int {
		t.Helper()

		method, pattern, _ := strings.Cut(route, " ")
		target := strings.NewReplacer("{objectID}", "made-up", "{operationID}", "made-up").Replace(pattern)

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), method, target, nethttp.NoBody))

		return res.Code
	}

	T.Run("each surface checks its routes with the consumer's enforcer", func(t *testing.T) {
		t.Parallel()

		enforcer, err := authzhttp.NewEnforcer(
			func(context.Context) (authorization.Grants, bool) {
				return authorization.NewGrants(authorization.NewPermissionSet()), true
			},
			authzhttp.WithDenyHandler(func(res nethttp.ResponseWriter, _ *nethttp.Request, _ error) {
				res.WriteHeader(nethttp.StatusTeapot)
			}),
		)
		must.NoError(t, err)

		handler := serve(t, enforcer)

		for surface, route := range guarded {
			test.EqOp(t, nethttp.StatusTeapot, request(t, handler, route),
				test.Sprintf("%s's %s was not refused by the enforcer Transports named", surface, route))
		}
	})

	T.Run("without one each surface refuses its guarded routes", func(t *testing.T) {
		t.Parallel()

		handler := serve(t, nil)

		for surface, route := range guarded {
			test.EqOp(t, nethttp.StatusForbidden, request(t, handler, route),
				test.Sprintf("%s served %s with no enforcer to check it", surface, route))
		}
	})
}
