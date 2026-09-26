package conformance

import (
	"testing"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// OperatorMethods are the RPCs the suites make only as an administrator, as
// the full method names a client invokes.
//
// They are the calls that act on the deployment rather than on the caller's
// own rows: a catalog somebody publishes, a directory somebody administers, a
// read across every tenant, a transition that decides whose turn it is. A
// deployment reserves those to a service role, and a suite that made them as
// an ordinary caller would be asserting against its authorization interceptor
// rather than against the handler — which is how an assertion about a
// malformed filter came to be answered PermissionDenied.
//
// What is not here the suites make as an ordinary caller, and that is a
// promise rather than an oversight: each suite's documentation names the
// ordinary calls it relies on, so a deployment that refuses one is told which
// promise it broke. Being on this list asserts nothing about an ordinary
// caller either way — a deployment that lets one make these calls passes, and
// so does one that refuses — so an entry costs a deployment nothing, and the
// cost of a missing one is a deployment failing for being right.
//
// Each suite's documentation names the ones on its own surface; the filters
// and pagination suites read the list itself, since they enumerate every
// paged read rather than naming them.
func OperatorMethods() []string {
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

// Operator mints the caller an operator-grade call is made as, in a tenant of
// its own: an administrator where the subject mints one, and an ordinary
// caller where it declines.
//
// The fallback is what keeps a subject with no notion of a service role
// asserting rather than skipping. Such a subject is one enforcing no method
// grants — a harness over a hand-built server — and its ordinary caller makes
// every call there is; a deployment that enforces grants and mints no
// administrator is refused, and the refusal is its own to explain.
func (s *Session) Operator(t *testing.T) *Subject {
	t.Helper()

	if admin := s.admin(t, AsAdmin()); admin != nil {
		return admin
	}

	return s.Subject(t)
}

// OperatorIn is Operator in an existing tenant, for the operator-grade call
// whose rows an ordinary caller there goes on to read, or has already written.
// Where the subject mints no administrator it answers an ordinary caller in that
// tenant, and where it cannot put one there either the assertion skips.
func (s *Session) OperatorIn(t *testing.T, scope tenancy.Scope) *Subject {
	t.Helper()

	if admin := s.admin(t, AsAdmin(), InTenant(scope)); admin != nil {
		return admin
	}

	return s.Subject(t, InTenant(scope))
}

// admin mints an administrator, or reports nil where the subject declines to.
//
// It calls the factory itself rather than through Subject, because Subject
// turns a decline into a skip and a decline is what the two callers above
// fall back from.
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
