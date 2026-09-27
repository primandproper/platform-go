package conformance

import (
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ReservableMethods are the RPCs a deployment may reserve to an operator, as
// the full method names a client invokes, and which the suites then make as
// one. It is a roster of what the suites can route, not a policy: which of
// them a deployment actually reserves is its own to say, in
// Seams.OperatorMethods, and a deployment that reserves none has every one of
// them made by an ordinary caller.
//
// They are the calls a deployment might reasonably keep from its members,
// because they act on the deployment rather than on the caller's own rows: a
// catalog somebody publishes, a directory somebody administers, a read across
// every tenant, a transition that decides whose turn it is. Whether it does is
// its product's decision — billing's own documentation says most deployments
// let every signed-in caller read the catalog — and this module's own assembled
// subject is run both ways.
//
// What is not here the suites make as an ordinary caller, and that is a
// promise rather than an oversight: each suite's documentation names the
// ordinary calls it relies on, so a deployment that refuses one is told which
// promise it broke. Run refuses a Seams.OperatorMethods entry naming one, for
// the same reason. An entry here costs a deployment nothing; the cost of a
// missing one is a deployment failing for a reservation it was entitled to
// make.
//
// Each suite's documentation names the ones on its own surface; the filters
// and pagination suites read Session.Reserves, since they enumerate every paged
// read rather than naming them.
func ReservableMethods() []string {
	return []string{
		auditpb.AuditService_VerifyChain_FullMethodName,

		billingpb.BillingService_CreateProduct_FullMethodName,
		billingpb.BillingService_GetProduct_FullMethodName,
		billingpb.BillingService_ListProducts_FullMethodName,
		billingpb.BillingService_UpdateProduct_FullMethodName,
		billingpb.BillingService_ArchiveProduct_FullMethodName,
		billingpb.BillingService_ListSubscriptions_FullMethodName,
		billingpb.BillingService_ArchiveSubscription_FullMethodName,
		billingpb.BillingService_ListPurchases_FullMethodName,
		billingpb.BillingService_ArchivePurchase_FullMethodName,
		billingpb.BillingService_ListTransactions_FullMethodName,
		billingpb.BillingService_ArchiveTransaction_FullMethodName,

		commentspb.CommentsService_ListCommentsByTargetType_FullMethodName,

		identitypb.IdentityService_Register_FullMethodName,
		identitypb.IdentityService_GetUser_FullMethodName,
		identitypb.IdentityService_ListUsers_FullMethodName,
		identitypb.IdentityService_SearchUsersByUsername_FullMethodName,
		identitypb.IdentityService_ArchiveUser_FullMethodName,
		identitypb.IdentityService_UpdateUserAccountStatus_FullMethodName,
		identitypb.IdentityService_SetUserServiceRoles_FullMethodName,
		identitypb.IdentityService_SetUserRequiresPasswordChange_FullMethodName,
		identitypb.IdentityService_ListAccounts_FullMethodName,

		oauth2clientspb.OAuth2ClientsService_CreateOAuth2Client_FullMethodName,
		oauth2clientspb.OAuth2ClientsService_GetOAuth2Client_FullMethodName,
		oauth2clientspb.OAuth2ClientsService_ListOAuth2Clients_FullMethodName,
		oauth2clientspb.OAuth2ClientsService_ArchiveOAuth2Client_FullMethodName,

		signinpb.SignInService_Register_FullMethodName,

		settingspb.SettingsService_CreateDefinition_FullMethodName,
		settingspb.SettingsService_UpdateDefinition_FullMethodName,
		settingspb.SettingsService_ArchiveDefinition_FullMethodName,
		settingspb.SettingsService_ListValuesForDefinition_FullMethodName,

		waitlistspb.WaitlistsService_CreateList_FullMethodName,
		waitlistspb.WaitlistsService_UpdateList_FullMethodName,
		waitlistspb.WaitlistsService_ArchiveList_FullMethodName,
		waitlistspb.WaitlistsService_GetSignup_FullMethodName,
		waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName,
		waitlistspb.WaitlistsService_ListSignups_FullMethodName,
		waitlistspb.WaitlistsService_UpdateSignupNotes_FullMethodName,
		waitlistspb.WaitlistsService_Invite_FullMethodName,
		waitlistspb.WaitlistsService_Convert_FullMethodName,
		waitlistspb.WaitlistsService_ArchiveSignup_FullMethodName,
		waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName,
	}
}

// Reserves reports whether the subject reserves method to an operator, by
// naming it in Seams.OperatorMethods.
func (s *Session) Reserves(method string) bool {
	return slices.Contains(s.seams.OperatorMethods, method)
}

// Operator mints the caller that makes methods, in a tenant of its own: an
// administrator where the subject reserves any of them to one, and an ordinary
// caller where it reserves none. methods are every one of ReservableMethods
// the caller goes on to make.
//
// The ordinary caller is the point. A deployment that lets its members make
// these calls has promised they can, and a suite that made them as an
// administrator anyway would never check it. A deployment that reserves one and
// mints no administrator skips, with that said: it has told the suite nobody it
// can mint may make the call.
func (s *Session) Operator(t *testing.T, methods ...string) *Subject {
	t.Helper()

	return s.operator(t, nil, methods)
}

// OperatorIn is Operator in an existing tenant, for the operator-grade call
// whose rows an ordinary caller there goes on to read, or has already written:
// the tenant scope names on surface, as InTenant reads the pair. A suite passes
// its own Suite.Name, and reads scope off the caller it wants an operator
// beside with Subject.ScopeFor. Where the subject reserves none of methods it
// answers an ordinary caller in that tenant, and where it cannot put one there
// the assertion skips.
func (s *Session) OperatorIn(t *testing.T, surface string, scope tenancy.Scope, methods ...string) *Subject {
	t.Helper()

	return s.operator(t, []SubjectOption{InTenant(surface, scope)}, methods)
}

func (s *Session) operator(t *testing.T, opts []SubjectOption, methods []string) *Subject {
	t.Helper()

	if len(methods) == 0 {
		t.Fatal("conformance: an operator was asked for without naming the methods it makes")
	}

	reserved := ""

	for _, method := range methods {
		if !slices.Contains(ReservableMethods(), method) {
			t.Fatalf("conformance: %s is made as an operator but is not in ReservableMethods", method)
		}

		if reserved == "" && s.Reserves(method) {
			reserved = method
		}
	}

	if reserved == "" {
		return s.Subject(t, opts...)
	}

	making := func(r *SubjectRequest) { r.Methods = slices.Clone(methods) }

	if admin := s.admin(t, append(opts, AsAdmin(), making)...); admin != nil {
		return admin
	}

	t.Skipf("conformance: this subject reserves %s to an operator and mints no administrator to make it", reserved)

	return nil
}

// admin mints an administrator, or reports nil where the subject declines to.
//
// It calls the factory itself rather than through Subject, because Subject's
// skip for a decline names neither the reservation nor the method, and the
// skip operator prints names both.
func (s *Session) admin(t *testing.T, opts ...SubjectOption) *Subject {
	t.Helper()

	if s.seams.NewSubject == nil {
		t.Fatal("conformance: Seams.NewSubject is required")
	}

	admin, err := s.seams.NewSubject(t.Context(), opts...)

	switch {
	case platformerrors.Is(err, ErrSubjectUnsupported):
		return nil
	case err != nil:
		t.Fatalf("conformance: minting an administrator: %v", err)
	case admin == nil:
		t.Fatal("conformance: the subject factory returned no administrator and no error")
	}

	return admin
}

// checkOperatorMethods fails a run whose Seams.OperatorMethods reserves a call
// the suites make as an ordinary caller.
func checkOperatorMethods(t *testing.T, methods []string) {
	t.Helper()

	if refused := unreservable(methods); len(refused) > 0 {
		t.Fatalf("conformance: Seams.OperatorMethods reserves %s, which the suites make as an ordinary caller "+
			"and rely on every signed-in caller being allowed; each suite's documentation says why", strings.Join(refused, ", "))
	}
}

// unreservable is the methods a deployment may not reserve: every one on a
// surface the suites cover that is not in ReservableMethods.
//
// A method on a service no suite covers is the deployment's own business and
// is let through, so a consumer can hand over the same list its interceptor
// reads.
func unreservable(methods []string) []string {
	covered := []string{
		auditpb.AuditService_ServiceDesc.ServiceName,
		billingpb.BillingService_ServiceDesc.ServiceName,
		commentspb.CommentsService_ServiceDesc.ServiceName,
		identitypb.IdentityService_ServiceDesc.ServiceName,
		issuereportspb.IssueReportsService_ServiceDesc.ServiceName,
		notificationspb.NotificationsService_ServiceDesc.ServiceName,
		oauth2clientspb.OAuth2ClientsService_ServiceDesc.ServiceName,
		passwordresetpb.PasswordResetService_ServiceDesc.ServiceName,
		settingspb.SettingsService_ServiceDesc.ServiceName,
		signinpb.SignInService_ServiceDesc.ServiceName,
		waitlistspb.WaitlistsService_ServiceDesc.ServiceName,
		webhookspb.WebhooksService_ServiceDesc.ServiceName,
	}

	var out []string

	for _, method := range methods {
		service, _, _ := strings.Cut(strings.TrimPrefix(method, "/"), "/")
		if slices.Contains(covered, service) && !slices.Contains(ReservableMethods(), method) {
			out = append(out, method)
		}
	}

	return out
}
