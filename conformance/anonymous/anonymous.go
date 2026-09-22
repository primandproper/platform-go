package anonymous

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	oauth2clientspb "github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	passwordresetgrpc "github.com/primandproper/platform-go/v14/authentication/passwordreset/grpc"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// surface is one service, how to tell it was mounted, and which of its methods
// are deliberately reachable without a caller.
type surface struct {

	// sample is any message from the service's package, which is how its
	// descriptor is found without naming the service twice.
	sample proto.Message

	// mounted reads the one field of Surfaces this service is reached through.
	mounted func(conformance.Surfaces) bool

	// name is the surface, as it appears in a subtest.
	name string

	// service is the descriptor's own name for the service.
	service protoreflect.Name

	// why says what the exception is for, on the three entries that have one.
	why string

	// anonymous are the full method names that require no caller, taken from
	// the surface's own declaration so that this roster cannot disagree with
	// it.
	//
	// nil means every method requires a caller.
	anonymous []string
}

// roster is every gRPC surface this module mounts.
//
// A closed list with a reason on every exception, which is the shape this
// module's other cross-cutting checks take. The completeness of it is asserted
// rather than trusted: TestRosterCoversEveryService walks the module's own
// account of what it mounts and fails on a service nobody put here.
func roster() []surface {
	return []surface{
		{
			name:    "audit",
			mounted: func(s conformance.Surfaces) bool { return s.Audit != nil },
			sample:  &auditpb.GetEntryRequest{},
			service: "AuditService",
		},
		{
			name:    "billing",
			mounted: func(s conformance.Surfaces) bool { return s.Billing != nil },
			sample:  &billingpb.GetSubscriptionRequest{},
			service: "BillingService",
		},
		{
			name:    "comments",
			mounted: func(s conformance.Surfaces) bool { return s.Comments != nil },
			sample:  &commentspb.GetCommentRequest{},
			service: "CommentsService",
		},
		{
			name:    "identity",
			mounted: func(s conformance.Surfaces) bool { return s.Identity != nil },
			sample:  &identitypb.RegisterRequest{},
			service: "IdentityService",
		},
		{
			name:    "issuereports",
			mounted: func(s conformance.Surfaces) bool { return s.IssueReports != nil },
			sample:  &issuereportspb.GetReportRequest{},
			service: "IssueReportsService",
		},
		{
			name:    "notifications",
			mounted: func(s conformance.Surfaces) bool { return s.Notifications != nil },
			sample:  &notificationspb.GetNotificationRequest{},
			service: "NotificationsService",
		},
		{
			name:    "oauth2clients",
			mounted: func(s conformance.Surfaces) bool { return s.OAuth2Clients != nil },
			sample:  &oauth2clientspb.GetOAuth2ClientRequest{},
			service: "OAuth2ClientsService",
		},
		{
			name:      "passwordreset",
			mounted:   func(s conformance.Surfaces) bool { return s.PasswordReset != nil },
			sample:    &passwordresetpb.RequestPasswordResetRequest{},
			service:   "PasswordResetService",
			anonymous: passwordresetgrpc.AnonymousMethods(),
			why:       "every RPC on it is for somebody who cannot sign in, so it reads no caller at all",
		},
		{
			name:    "settings",
			mounted: func(s conformance.Surfaces) bool { return s.Settings != nil },
			sample:  &settingspb.GetDefinitionRequest{},
			service: "SettingsService",
		},
		{
			name:      "signin",
			mounted:   func(s conformance.Surfaces) bool { return s.SignIn != nil },
			sample:    &signinpb.LoginForTokenRequest{},
			service:   "SignInService",
			anonymous: signingrpc.AnonymousMethods(),
			why:       "sign-in itself, and the doors that finish a registration or end a session",
		},
		{
			name:      "waitlists",
			mounted:   func(s conformance.Surfaces) bool { return s.Waitlists != nil },
			sample:    &waitlistspb.GetListRequest{},
			service:   "WaitlistsService",
			anonymous: waitlistsgrpc.PublicMethods(),
			why:       "the signup page, the form it submits, and the unsubscribe link in the mail that follows",
		},
		{
			name:    "webhooks",
			mounted: func(s conformance.Surfaces) bool { return s.Webhooks != nil },
			sample:  &webhookspb.GetEndpointRequest{},
			service: "WebhooksService",
		},
	}
}

// Suite asserts what every RPC does with a request carrying no caller.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name: "anonymous",

		// Every subject mounts something, and each surface's own presence is
		// read inside. Reporting "not mounted" for the whole suite would need
		// a subject that mounted nothing at all.
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	anonymous := s.Seams().Anonymous
	if anonymous == nil {
		t.Skip("conformance: this subject supplies no callerless connection, and one cannot be synthesized from an authenticated one")
	}

	conn, err := anonymous(t.Context())
	must.NoError(t, err, must.Sprint("opening a connection carrying no caller"))
	must.NotNil(t, conn, must.Sprint("the subject returned no connection and no error"))

	probe := s.Subject(t)

	surfaces := roster()

	for i := range surfaces {
		surf := &surfaces[i]

		if !surf.mounted(probe.Surfaces) {
			continue
		}

		t.Run(surf.name, func(t *testing.T) {
			t.Parallel()

			service := descriptorFor(t, surf)
			methods := service.Methods()

			for i := range methods.Len() {
				method := methods.Get(i)
				full := "/" + string(service.FullName()) + "/" + string(method.Name())
				open := slices.Contains(surf.anonymous, full)

				t.Run(string(method.Name()), func(t *testing.T) {
					t.Parallel()

					callErr := conn.Invoke(t.Context(), full,
						dynamicpb.NewMessage(method.Input()),
						dynamicpb.NewMessage(method.Output()))

					if open {
						// Reachable, not successful. An empty request will
						// usually fail on its input, and that is fine — what is
						// asserted is that it did not fail on who was asking.
						test.NotEqOp(t, codes.Unauthenticated, status.Code(callErr),
							test.Sprintf("%s is declared reachable without a caller (%s) and was refused for want of one", full, surf.why))

						return
					}

					must.Error(t, callErr,
						must.Sprintf("%s answered a request carrying no caller", full))
					test.EqOp(t, codes.Unauthenticated, status.Code(callErr),
						test.Sprintf("%s refused a callerless request as something other than unauthenticated", full))
				})
			}
		})
	}
}

// descriptorFor is the schema's own account of a service, which is what makes
// the loop above enumerate RPCs rather than list them.
func descriptorFor(t *testing.T, surf *surface) protoreflect.ServiceDescriptor {
	t.Helper()

	file := surf.sample.ProtoReflect().Descriptor().ParentFile()

	service := file.Services().ByName(surf.service)
	must.NotNil(t, service, must.Sprintf("the generated file for %s describes no %s", surf.name, surf.service))
	must.Positive(t, service.Methods().Len(), must.Sprintf("%s describes no methods", surf.service))

	return service
}
