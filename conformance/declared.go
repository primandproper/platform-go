package conformance

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/authentication/passkeys/passkeyspb"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	"github.com/primandproper/platform-go/v14/mediaregistry/mediaregistrypb"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errorf is the half of testing.T a declaredConn reports through.
type errorf interface {
	Errorf(format string, args ...any)
}

// declaredConn is a caller's connection, admitting only the calls the caller
// was minted to make.
//
// It is what makes Making a checked statement rather than a comment. Which
// caller a suite mints for a call — an administrator or a member — is decided
// by what it declares, so a call it did not declare is a call made by a caller
// chosen for something else: a member making a call the deployment keeps to
// its staff, which the deployment refuses and the suite would read as a broken
// promise. The check is on the caller rather than in a deployment, so it holds
// for a member as for an operator, against every subject, whatever that
// subject reserves — including one that reserves nothing, where a missing
// declaration would otherwise pass unnoticed until a consumer who reserves the
// call runs the suite.
//
// An undeclared call fails the test that minted the caller and is answered
// with an error of its own, so an assertion expecting a refusal cannot read it
// as one.
type declaredConn struct {
	grpc.ClientConnInterface

	t        errorf
	declared []string
}

func (c *declaredConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	if err := c.admit(method); err != nil {
		return err
	}

	return c.ClientConnInterface.Invoke(ctx, method, args, reply, opts...)
}

func (c *declaredConn) NewStream(
	ctx context.Context,
	desc *grpc.StreamDesc,
	method string,
	opts ...grpc.CallOption,
) (grpc.ClientStream, error) {
	if err := c.admit(method); err != nil {
		return nil, err
	}

	return c.ClientConnInterface.NewStream(ctx, desc, method, opts...)
}

func (c *declaredConn) admit(method string) error {
	if slices.Contains(c.declared, method) {
		return nil
	}

	c.t.Errorf("conformance: %s was called by a caller that did not declare it; "+
		"add it to the conformance.Making the caller was minted with (it declared %s)", method, declaredList(c.declared))

	return status.Errorf(codes.Internal, "conformance: %s is not among the calls this caller declared", method)
}

func declaredList(methods []string) string {
	if len(methods) == 0 {
		return "nothing"
	}

	return strings.Join(methods, ", ")
}

// declare puts sub's calls behind a connection admitting only methods, and
// rebuilds every surface it mounts over that connection; and puts its HTTP
// client behind a transport admitting only the routes among methods.
//
// A subject mounting a gRPC surface must therefore supply the connection its
// surfaces are reached through: the typed clients it hands over are read for
// which surfaces it mounts, and replaced. Each is the generated client over
// the checked connection, which is what this module's client wrappers embed
// and all a suite calls through.
func declare(t *testing.T, sub *Subject, methods []string) *Subject {
	t.Helper()

	grpcMounted := mountsGRPC(&sub.Surfaces)
	if !grpcMounted && (sub.HTTP == nil || sub.HTTP.Client == nil) {
		return sub
	}

	checked := *sub
	checked.HTTP = declareHTTP(t, sub.HTTP, methods)

	if !grpcMounted {
		return &checked
	}

	if sub.Conn == nil {
		t.Fatal("conformance: the subject mounts a gRPC surface and supplies no Subject.Conn; " +
			"the surfaces are rebuilt over it so that each caller makes only the calls it declared")

		return nil
	}

	conn := &declaredConn{ClientConnInterface: sub.Conn, t: t, declared: slices.Clone(methods)}
	checked.Conn = conn
	checked.Surfaces = surfacesOver(&sub.Surfaces, conn)

	return &checked
}

// mountsGRPC reports whether s names any surface. Field by field rather than
// against the zero value, since comparing an interface holding a value of an
// uncomparable type panics.
func mountsGRPC(s *Surfaces) bool {
	return s.Audit != nil || s.Billing != nil || s.Comments != nil || s.Identity != nil ||
		s.IssueReports != nil || s.MediaRegistry != nil || s.Notifications != nil || s.OAuth2Clients != nil ||
		s.Passkeys != nil || s.PasswordReset != nil || s.Settings != nil || s.SignIn != nil ||
		s.SignInAdministration != nil || s.Waitlists != nil || s.Webhooks != nil
}

// surfacesOver is mounted rebuilt over conn, surface for surface.
func surfacesOver(mounted *Surfaces, conn grpc.ClientConnInterface) Surfaces {
	var out Surfaces

	if mounted.Audit != nil {
		out.Audit = auditpb.NewAuditServiceClient(conn)
	}

	if mounted.Billing != nil {
		out.Billing = billingpb.NewBillingServiceClient(conn)
	}

	if mounted.Comments != nil {
		out.Comments = commentspb.NewCommentsServiceClient(conn)
	}

	if mounted.Identity != nil {
		out.Identity = identitypb.NewIdentityServiceClient(conn)
	}

	if mounted.IssueReports != nil {
		out.IssueReports = issuereportspb.NewIssueReportsServiceClient(conn)
	}

	if mounted.MediaRegistry != nil {
		out.MediaRegistry = mediaregistrypb.NewMediaRegistryServiceClient(conn)
	}

	if mounted.Notifications != nil {
		out.Notifications = notificationspb.NewNotificationsServiceClient(conn)
	}

	if mounted.OAuth2Clients != nil {
		out.OAuth2Clients = oauth2clientspb.NewOAuth2ClientsServiceClient(conn)
	}

	if mounted.Passkeys != nil {
		out.Passkeys = passkeyspb.NewPasskeysServiceClient(conn)
	}

	if mounted.PasswordReset != nil {
		out.PasswordReset = passwordresetpb.NewPasswordResetServiceClient(conn)
	}

	if mounted.Settings != nil {
		out.Settings = settingspb.NewSettingsServiceClient(conn)
	}

	if mounted.SignIn != nil {
		out.SignIn = signinpb.NewSignInServiceClient(conn)
	}

	if mounted.SignInAdministration != nil {
		out.SignInAdministration = signinpb.NewSignInAdministrationServiceClient(conn)
	}

	if mounted.Waitlists != nil {
		out.Waitlists = waitlistspb.NewWaitlistsServiceClient(conn)
	}

	if mounted.Webhooks != nil {
		out.Webhooks = webhookspb.NewWebhooksServiceClient(conn)
	}

	return out
}
