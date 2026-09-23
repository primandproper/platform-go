package service

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetmock "github.com/primandproper/platform-go/v14/authentication/passwordreset/mock"
	"github.com/primandproper/platform-go/v14/billing"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/comments"
	commentsmock "github.com/primandproper/platform-go/v14/comments/mock"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacymock "github.com/primandproper/platform-go/v14/dataprivacy/mock"
	"github.com/primandproper/platform-go/v14/identity"
	identitymock "github.com/primandproper/platform-go/v14/identity/mock"
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	issuereportsmock "github.com/primandproper/platform-go/v14/issuereports/mock"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationsmock "github.com/primandproper/platform-go/v14/notifications/mock"
	"github.com/primandproper/platform-go/v14/operations"
	operationsmock "github.com/primandproper/platform-go/v14/operations/mock"
	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	settingsmock "github.com/primandproper/platform-go/v14/settings/mock"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	waitlistsmock "github.com/primandproper/platform-go/v14/waitlists/mock"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v14/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
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
	"google.golang.org/grpc/credentials/insecure"
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
		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

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

		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		// The gRPC lane then the HTTP one, alphabetical within each. identity,
		// oauth2 clients, password reset and sign-in are absent because their
		// services are: see the subtest below.
		test.Eq(t, []string{
			"audit gRPC",
			"billing gRPC",
			"comments gRPC",
			"issue reports gRPC",
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
		test.SliceLen(t, 8, mounted.registrations)
	})

	// The surfaces above are mounted off a store, which a mock satisfies. The four
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

		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		// No extractor was needed for it, which is the half worth pinning: it is
		// the only surface here that mounts without one.
		test.Eq(t, []string{"password reset gRPC"}, mounted.names)
		test.SliceLen(t, 1, mounted.registrations)
	})

	T.Run("a surface whose store is unconfigured does not mount, and that is not an error", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.Eq(t, []string{"billing gRPC"}, mounted.names)
	})

	T.Run("a surface whose service is unregistered does not mount, though its store is", func(t *testing.T) {
		t.Parallel()

		// identity is the case worth pinning: Register registers the store and
		// not the service, so a Config naming Identity is not on its own enough
		// to put the directory on the wire.
		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[identity.Store](i, &identitymock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

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

				RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: tc.seams})

				_, err := do.Invoke[*mountedTransports](i)
				must.ErrorIs(t, err, tc.want)
			})
		}
	})

	T.Run("an optional authorizer left nil leaves the surface's own default in place", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[comments.Store](i, &commentsmock.StoreMock{})

		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

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
		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

		_, err := do.Invoke[*mountedTransports](i)
		must.ErrorIs(t, err, ErrGRPCRegistrationsAlreadyProvided)
	})

	T.Run("a route the router refuses is reported rather than quietly not mounted", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue(i, newRouter())
		do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})

		seams := &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()}

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

		seams := &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()}

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

		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

		_, err := do.Invoke[*mountedTransports](i)
		must.ErrorIs(t, err, errBrokenStore)
	})
}

func TestDerivedSeams(T *testing.T) {
	T.Parallel()

	caller := testPrincipal{userID: "user_1", scope: tenancy.Of("tenant_1"), account: "account_1"}
	withCaller := context.WithValue(context.Background(), principalKey{}, callers.Principal(caller))

	T.Run("the scope a caller is acting in", func(t *testing.T) {
		t.Parallel()

		scope, err := deriveScope(withPrincipal)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, caller.scope, scope)
	})

	T.Run("the subject a privacy request is about", func(t *testing.T) {
		t.Parallel()

		subject, err := deriveSubject(withPrincipal)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, dataprivacy.Subject{ID: "user_1", Type: dataprivacy.SubjectUser}, subject)
	})

	T.Run("the caller a media request is from", func(t *testing.T) {
		t.Parallel()

		mediaCaller, err := deriveMediaCaller(withPrincipal, deriveScope(withPrincipal))(withCaller)
		must.NoError(t, err)
		test.EqOp(t, mediaregistryhttp.Caller{PrincipalID: "user_1", Scope: caller.scope}, mediaCaller)
	})

	T.Run("a media caller carries the tenant it was handed rather than the directory", func(t *testing.T) {
		t.Parallel()

		tenant := func(context.Context) (tenancy.Scope, error) { return tenancy.Of("account_1"), nil }

		mediaCaller, err := deriveMediaCaller(withPrincipal, tenant)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, mediaregistryhttp.Caller{PrincipalID: "user_1", Scope: tenancy.Of("account_1")}, mediaCaller)
	})

	T.Run("a tenant resolver that refuses takes the media caller down with it", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("no tenant on this request")
		tenant := func(context.Context) (tenancy.Scope, error) { return tenancy.Scope{}, sentinel }

		_, err := deriveMediaCaller(withPrincipal, tenant)(withCaller)
		test.ErrorIs(t, err, sentinel)
	})

	T.Run("a request with nobody on it is refused rather than read as the global scope", func(t *testing.T) {
		t.Parallel()

		nobody := context.Background()

		_, err := deriveScope(withPrincipal)(nobody)
		test.ErrorIs(t, err, ErrNoPrincipal)

		_, err = deriveSubject(withPrincipal)(nobody)
		test.ErrorIs(t, err, ErrNoPrincipal)

		_, err = deriveMediaCaller(withPrincipal, deriveScope(withPrincipal))(nobody)
		test.ErrorIs(t, err, ErrNoPrincipal)
	})
}

func TestMount_tenantScope(T *testing.T) {
	T.Parallel()

	caller := testPrincipal{userID: "user_1", scope: tenancy.Of("directory_1"), account: "account_1"}
	withCaller := context.WithValue(context.Background(), principalKey{}, callers.Principal(caller))

	T.Run("falls back to the principal's own scope", func(t *testing.T) {
		t.Parallel()

		m := &mount{t: &Transports{Extractor: withPrincipal}}

		scope, err := m.tenantScope(withPrincipal)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, tenancy.Of("directory_1"), scope)
	})

	T.Run("prefers the application's resolver", func(t *testing.T) {
		t.Parallel()

		m := &mount{t: &Transports{
			Extractor: withPrincipal,
			TenantScope: func(ctx context.Context) (tenancy.Scope, error) {
				principal, ok := withPrincipal(ctx)
				if !ok {
					return tenancy.Scope{}, ErrNoPrincipal
				}

				return tenancy.Of(principal.ActiveAccountID()), nil
			},
		}}

		// The directory the caller is in and the tenant the request is against
		// are different answers, which is the whole reason the field exists.
		scope, err := m.tenantScope(withPrincipal)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, tenancy.Of("account_1"), scope)
		test.NotEqOp(t, caller.scope, scope)
	})

	T.Run("carries the application resolver's refusal out", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("this caller belongs to no tenant")

		m := &mount{t: &Transports{
			Extractor:   withPrincipal,
			TenantScope: func(context.Context) (tenancy.Scope, error) { return tenancy.Scope{}, sentinel },
		}}

		_, err := m.tenantScope(withPrincipal)(withCaller)
		test.ErrorIs(t, err, sentinel)
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

		RegisterTransports(i, &Transports{Extractor: withPrincipal, Authorizers: allAuthorizers()})

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

		RegisterTransports(i, &Transports{Extractor: withPrincipal})

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

// TestRegisterTransports_tenantScopeReachesTheMountedSurface is the assertion
// the unit tests above cannot make: that the three surfaces meaning the tenant
// are mounted with Transports.TenantScope and not with the derivation.
//
// It proves it by removing the derivation's input. A context value does not
// cross a connection, so the principal withPrincipal reads is not there on the
// server side of a real gRPC call: deriveScope would refuse the request with
// ErrNoPrincipal. A read that succeeds, against the scope the resolver named,
// is therefore a read the resolver placed.
func TestRegisterTransports_tenantScopeReachesTheMountedSurface(T *testing.T) {
	T.Parallel()

	const tenant = "acct_the_studio"

	T.Run("audit reads the tenant the application named", func(t *testing.T) {
		t.Parallel()

		var asked *tenancy.Scope

		reader := &auditmock.ReaderMock{
			ListFunc: func(_ context.Context, _ database.SQLQueryExecutor, query *audit.Query, _ *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
				asked = query.Scope

				return &filtering.QueryFilteredResult[audit.Entry]{Data: []*audit.Entry{}}, nil
			},
		}

		client := auditServiceOverBufconn(t, reader, &Transports{
			Extractor:   withPrincipal,
			Authorizers: allAuthorizers(),
			TenantScope: func(context.Context) (tenancy.Scope, error) { return tenancy.Of(tenant), nil },
		})

		_, err := client.ListEntries(t.Context(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		must.NotNil(t, asked, must.Sprint("the reader was never asked"))
		test.EqOp(t, tenancy.Of(tenant), *asked)
	})

	T.Run("without one, the derivation still governs and refuses a request with nobody on it", func(t *testing.T) {
		t.Parallel()

		reader := &auditmock.ReaderMock{
			ListFunc: func(context.Context, database.SQLQueryExecutor, *audit.Query, *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
				t.Error("the reader must not be consulted for a request that could not be placed")

				return nil, nil
			},
		}

		client := auditServiceOverBufconn(t, reader, &Transports{
			Extractor:   withPrincipal,
			Authorizers: allAuthorizers(),
		})

		_, err := client.ListEntries(t.Context(), &auditpb.ListEntriesRequest{})
		must.Error(t, err, must.Sprint("a request with no principal and no resolver has no scope to be against"))
	})
}

// auditServiceOverBufconn mounts the transports and serves them on an
// in-process connection, returning a client for the audit surface.
//
// The audit config block is deliberately absent: leaving it out is what stops
// Register providing its own reader, so the test's can be the one the surface
// mounts over.
func auditServiceOverBufconn(t *testing.T, reader audit.Reader, transports *Transports) auditpb.AuditServiceClient {
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

	mounted, err := do.Invoke[*mountedTransports](i)
	must.NoError(t, err)
	must.SliceContains(t, mounted.names, "audit gRPC")

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

	return auditpb.NewAuditServiceClient(conn)
}
