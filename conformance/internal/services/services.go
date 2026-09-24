// Package services is the twelve gRPC services this module mounts, as the
// cross-cutting suites find them: a name, a message from the service's package
// to reach its descriptor through, and the field of conformance.Surfaces it is
// called through.
//
// It is one list because two suites read it, and a second copy is a list that
// can disagree with the first about which services exist. The anonymous
// suite's roster test checks it against the protobuf registry, so a thirteenth
// service fails there until it is added here.
package services

import (
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	oauth2clientspb "github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Service is one gRPC service this module mounts.
type Service struct {
	// Sample is any message from the service's package, which is how its
	// descriptor is found without naming the service twice.
	Sample proto.Message

	// Mounted reads the one field of Surfaces this service is reached through.
	Mounted func(conformance.Surfaces) bool

	// Name is the surface, as it appears in a subtest.
	Name string

	// Service is the descriptor's own name for the service.
	Service protoreflect.Name
}

// Descriptor is the service's descriptor, or nil where Sample's file has no
// service of that name — which the anonymous roster test reports.
func (s *Service) Descriptor() protoreflect.ServiceDescriptor {
	return s.Sample.ProtoReflect().Descriptor().ParentFile().Services().ByName(s.Service)
}

// All is every gRPC service this module mounts.
func All() []Service {
	return []Service{
		{Name: "audit", Service: "AuditService", Sample: &auditpb.GetEntryRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.Audit != nil }},
		{Name: "billing", Service: "BillingService", Sample: &billingpb.GetSubscriptionRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.Billing != nil }},
		{Name: "comments", Service: "CommentsService", Sample: &commentspb.GetCommentRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.Comments != nil }},
		{Name: "identity", Service: "IdentityService", Sample: &identitypb.RegisterRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.Identity != nil }},
		{Name: "issuereports", Service: "IssueReportsService", Sample: &issuereportspb.GetReportRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.IssueReports != nil }},
		{Name: "notifications", Service: "NotificationsService", Sample: &notificationspb.GetNotificationRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.Notifications != nil }},
		{Name: "oauth2clients", Service: "OAuth2ClientsService", Sample: &oauth2clientspb.GetOAuth2ClientRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.OAuth2Clients != nil }},
		{Name: "passwordreset", Service: "PasswordResetService", Sample: &passwordresetpb.RequestPasswordResetRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.PasswordReset != nil }},
		{Name: "settings", Service: "SettingsService", Sample: &settingspb.GetDefinitionRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.Settings != nil }},
		{Name: "signin", Service: "SignInService", Sample: &signinpb.LoginForTokenRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.SignIn != nil }},
		{Name: "waitlists", Service: "WaitlistsService", Sample: &waitlistspb.GetListRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.Waitlists != nil }},
		{Name: "webhooks", Service: "WebhooksService", Sample: &webhookspb.GetEndpointRequest{},
			Mounted: func(s conformance.Surfaces) bool { return s.Webhooks != nil }},
	}
}
