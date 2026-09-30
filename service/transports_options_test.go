package service

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	"github.com/primandproper/platform-go/v14/billing"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"
	"github.com/primandproper/platform-go/v14/comments"
	commentsmock "github.com/primandproper/platform-go/v14/comments/mock"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacymock "github.com/primandproper/platform-go/v14/dataprivacy/mock"
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsmock "github.com/primandproper/platform-go/v14/issuereports/mock"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationsmock "github.com/primandproper/platform-go/v14/notifications/mock"
	"github.com/primandproper/platform-go/v14/operations"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"
	operationsmock "github.com/primandproper/platform-go/v14/operations/mock"
	"github.com/primandproper/platform-go/v14/settings"
	settingsmock "github.com/primandproper/platform-go/v14/settings/mock"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsmock "github.com/primandproper/platform-go/v14/waitlists/mock"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v14/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/database"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/filtering"
	grpcserver "github.com/primandproper/primitives-go/v2/server/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/uploads"
	uploadsmock "github.com/primandproper/primitives-go/v2/uploads/mock"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

// scopeRecordingReader is an audit reader that remembers the scope it was
// asked for, which is how a test sees which resolver a mounted surface used.
func scopeRecordingReader(asked **tenancy.Scope) *auditmock.ReaderMock {
	return &auditmock.ReaderMock{
		ListFunc: func(_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, _ *audit.Query, _ *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
			*asked = &scope

			return &filtering.QueryFilteredResult[audit.Entry]{Data: []*audit.Entry{}}, nil
		},
	}
}

func TestRegisterTransports_surfaceOptions(T *testing.T) {
	T.Parallel()

	const own = "acct_from_the_application"

	ownScope := func(context.Context) (tenancy.Scope, error) { return tenancy.Of(own), nil }

	T.Run("audit mounts with the application's own options and no Transports field for them", func(t *testing.T) {
		t.Parallel()

		var asked *tenancy.Scope

		// No Extractor and no TenantOf: the scope resolver arrives in audit's
		// own Option type, and that is the whole of what the surface is told.
		client := auditServiceOverBufconn(t, scopeRecordingReader(&asked), nil, &Transports{
			Options: SurfaceOptions{
				Audit: []auditgrpc.Option{auditgrpc.WithScopeResolver(ownScope)},
			},
		})

		_, err := client.ListEntries(t.Context(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		must.NotNil(t, asked, must.Sprint("the reader was never asked"))
		test.EqOp(t, tenancy.Of(own), *asked)
	})

	T.Run("the application's option overrides the resolver the platform derived", func(t *testing.T) {
		t.Parallel()

		var asked *tenancy.Scope

		client := auditServiceOverBufconn(t, scopeRecordingReader(&asked), testPrincipal{userID: "user_1"}, &Transports{
			Extractor: withPrincipal,
			TenantOf:  DirectoryTenant,
			Options: SurfaceOptions{
				Audit: []auditgrpc.Option{auditgrpc.WithScopeResolver(ownScope)},
			},
		})

		_, err := client.ListEntries(t.Context(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		must.NotNil(t, asked, must.Sprint("the reader was never asked"))
		test.EqOp(t, tenancy.Of(own), *asked)
	})

	// Options excuse a missing Extractor or TenantOf only by skipping the
	// derivation, never by mounting open: options that name no resolver
	// leave the surface to refuse the startup in its own words.
	T.Run("options naming no resolver leave the surface's own refusal standing", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[audit.Reader](i, &auditmock.ReaderMock{})

		RegisterTransports(i, &Transports{
			Options: SurfaceOptions{Audit: []auditgrpc.Option{auditgrpc.WithLogger(nil)}},
		})

		_, err := do.Invoke[*mountedTransports](i)
		must.ErrorIs(t, err, auditgrpc.ErrNilScopeResolver)
		test.StrContains(t, err.Error(), "audit")
	})

	T.Run("an HTTP surface takes its options the same way", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue(i, newRouter())
		do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})

		RegisterTransports(i, &Transports{
			Options: SurfaceOptions{
				Operations: []operationshttp.Option{
					operationshttp.WithOwnerResolver(ownScope),
					operationshttp.WithBasePath("/jobs"),
				},
			},
		})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.Eq(t, []string{"operations HTTP"}, mounted.names)
	})
}

func TestRegisterTransports_skip(T *testing.T) {
	T.Parallel()

	T.Run("a skipped surface is not mounted, and the application's replacement is", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[billing.Store](i, &billingmock.StoreMock{})
		do.ProvideValue[comments.Store](i, &commentsmock.StoreMock{})

		var replaced int

		RegisterTransports(i, &Transports{
			Extractor:     withPrincipal,
			Authorizers:   allAuthorizers(),
			Skip:          []Surface{SurfaceBilling},
			Registrations: []grpcserver.RegistrationFunc{func(*grpc.Server) { replaced++ }},
		})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.Eq(t, []string{"comments gRPC"}, mounted.names)
		must.SliceLen(t, 2, mounted.registrations)

		mounted.registrations[1](nil)
		test.EqOp(t, 1, replaced)
	})

	// A skipped surface is not asked for its seams either, which is what lets
	// an application replace one whose required seam it has no value for.
	T.Run("a skipped surface's seams are not required", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[audit.Reader](i, &auditmock.ReaderMock{})

		RegisterTransports(i, &Transports{Skip: []Surface{SurfaceAudit}})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.SliceEmpty(t, mounted.names)
	})

	T.Run("skipping every surface mounts none of them", func(t *testing.T) {
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

		skip := make([]Surface, 0, len(surfaces))
		for surface := range surfaces {
			skip = append(skip, surface)
		}

		RegisterTransports(i, &Transports{
			Extractor:   withPrincipal,
			TenantOf:    DirectoryTenant,
			Authorizers: allAuthorizers(),
			Skip:        skip,
		})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)

		test.SliceEmpty(t, mounted.names)
		test.SliceEmpty(t, mounted.registrations)
	})

	T.Run("a surface this package does not mount is a startup error", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		RegisterTransports(i, &Transports{Skip: []Surface{"auditt"}})

		_, err := do.Invoke[*mountedTransports](i)
		must.ErrorIs(t, err, ErrUnknownSurface)
		test.StrContains(t, err.Error(), "auditt")
	})
}
