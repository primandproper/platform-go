package service

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
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

	"github.com/primandproper/primitives-go/v2/database"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
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
		// oauth2 clients and sign-in are absent because their services are:
		// see the subtest below.
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

		mediaCaller, err := deriveMediaCaller(withPrincipal)(withCaller)
		must.NoError(t, err)
		test.EqOp(t, mediaregistryhttp.Caller{PrincipalID: "user_1", Scope: caller.scope}, mediaCaller)
	})

	T.Run("a request with nobody on it is refused rather than read as the global scope", func(t *testing.T) {
		t.Parallel()

		nobody := context.Background()

		_, err := deriveScope(withPrincipal)(nobody)
		test.ErrorIs(t, err, ErrNoPrincipal)

		_, err = deriveSubject(withPrincipal)(nobody)
		test.ErrorIs(t, err, ErrNoPrincipal)

		_, err = deriveMediaCaller(withPrincipal)(nobody)
		test.ErrorIs(t, err, ErrNoPrincipal)
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
