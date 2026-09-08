package grpc

import (
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/authorization"
	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"
)

// The permissions this service's methods require, in authorization's
// vocabulary.
//
// They are declared here rather than in the authorization package because
// authorization is a primitive: it owns what a Permission *is* — a string, a
// set, a role that inherits — and owns no domain's names. "identity.users.read"
// means something only to a directory, so it is spelled beside the directory.
//
// The strings are dotted and namespaced so that a consumer composing several
// domains' fragments cannot have two of them collide on "read". They are values
// rather than an enum because a consumer's policy is data — a YAML file, a
// table of roles — and it has to be able to name one without importing Go.
const (
	// PermissionReadUsers covers reading a user who is not the caller, and
	// paging or searching the directory.
	PermissionReadUsers authorization.Permission = "identity.users.read"

	// PermissionCreateUsers covers Register. It is a permission rather than a
	// public method for the reason Register's own documentation gives: open
	// sign-up is a flow with policy in it, and a consumer that wants one puts
	// that policy in front of this call.
	PermissionCreateUsers authorization.Permission = "identity.users.create"

	// PermissionArchiveUsers covers soft-deleting somebody.
	PermissionArchiveUsers authorization.Permission = "identity.users.archive"

	// PermissionUpdateUserStatus covers banning, terminating and reinstating.
	PermissionUpdateUserStatus authorization.Permission = "identity.users.update_status"

	// PermissionUpdateUserServiceRoles covers granting and withdrawing operator
	// access.
	//
	// It is separate from PermissionUpdateUserStatus, and deliberately: a
	// support role that can unban somebody is a common thing to grant, and one
	// that can make somebody an operator is not. Collapsing the two would make
	// the first imply the second.
	PermissionUpdateUserServiceRoles authorization.Permission = "identity.users.update_service_roles"

	// PermissionReadAccounts covers reading an account and its roster, by id.
	//
	// The grant is on the method and says the caller may perform this kind of
	// read; which account they may perform it against is [TargetAuthorizer]'s,
	// asked inside the handler where the store is. Both have to say yes. That
	// split is the whole of the arrangement in this file: an interceptor sees
	// the method and the grants and has no handle to read a row with, so a
	// permission that tried to mean "the accounts this caller is a member of"
	// would be a sentence nothing could evaluate.
	PermissionReadAccounts authorization.Permission = "identity.accounts.read"

	// PermissionListAllAccounts covers paging every account in the directory.
	//
	// Separate from PermissionReadAccounts because they answer different
	// questions: reading an account somebody named and enumerating the customer
	// list are not the same grant, and a roster screen needs only the first.
	PermissionListAllAccounts authorization.Permission = "identity.accounts.list_all"

	// PermissionUpdateAccounts covers renaming an account and changing its
	// billing address and time zone.
	PermissionUpdateAccounts authorization.Permission = "identity.accounts.update"

	// PermissionTransferAccountOwnership covers moving an account to a new
	// owner.
	PermissionTransferAccountOwnership authorization.Permission = "identity.accounts.transfer_ownership"

	// PermissionManageMembers covers the roster writes: setting a member's roles
	// and removing them.
	PermissionManageMembers authorization.Permission = "identity.members.manage"

	// PermissionInviteMembers covers sending an invitation and cancelling one.
	//
	// Cancelling shares this permission rather than having one of its own
	// because both are the sender's side of the same act. Like every grant here
	// it is on the method, and like every request-named method the row is
	// checked too: Invite asks [TargetAuthorizer] about the account, and
	// CancelInvitation about the invitation, whose default rule names the sender
	// and the account's members. A holder of this permission is therefore an
	// account's inviter rather than the directory's.
	PermissionInviteMembers authorization.Permission = "identity.invitations.send"

	// PermissionReadInvitations covers reading one invitation by id.
	//
	// It is the one id-addressed grant with no row check behind it, because the
	// reader it exists for includes the recipient — who is neither the sender nor
	// a member of the account yet. GetInvitation says the same at more length.
	PermissionReadInvitations authorization.Permission = "identity.invitations.read"
)

// Permissions is the default map from this service's methods to what each
// requires: the fragment a consumer composes into its own policy.
//
// It is a default and not a rule. A consumer that wants ListAccounts open to
// every member, or Register behind two permissions rather than one, overrides
// the entry — the map is theirs once they have it, and authorization/grpc's
// builder takes whatever they hand it.
//
// It is also only half of what gates this service. Eleven of these methods take
// their target from the request body, and a grant on the method cannot say which
// account, user or invitation the caller may name — that is [TargetAuthorizer],
// asked inside the handler, and it is not overridable through this map. See
// [MembershipAuthorizer] for the default rules.
//
// The keys are the generated full method names, which is the form
// grpc.UnaryServerInfo.FullMethod carries and the form RequirementsBuilder
// matches on. Spelling them as the generated constants rather than as string
// literals is what makes a renamed RPC a compile error here instead of a method
// that silently stops being checked.
//
// A method is absent from this map only if it is in [SelfServiceMethods]. The
// two together are exhaustive over the service, and permissions_test.go is what
// keeps them that way — an RPC added later and decided about in neither fails
// there, rather than being denied in somebody's production by the enforcer's
// fail-closed rule.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		// The directory reads. Reading a user who is not you is not the same
		// act as reading yourself, which is GetPrincipal and needs nothing.
		identitypb.IdentityService_GetUser_FullMethodName:               {PermissionReadUsers},
		identitypb.IdentityService_ListUsers_FullMethodName:             {PermissionReadUsers},
		identitypb.IdentityService_SearchUsersByUsername_FullMethodName: {PermissionReadUsers},

		// Registration.
		identitypb.IdentityService_Register_FullMethodName: {PermissionCreateUsers},

		// The three operator writes, each with its own permission.
		identitypb.IdentityService_ArchiveUser_FullMethodName:             {PermissionArchiveUsers},
		identitypb.IdentityService_UpdateUserAccountStatus_FullMethodName: {PermissionUpdateUserStatus},
		identitypb.IdentityService_SetUserServiceRoles_FullMethodName:     {PermissionUpdateUserServiceRoles},

		// Accounts.
		identitypb.IdentityService_GetAccount_FullMethodName:               {PermissionReadAccounts},
		identitypb.IdentityService_ListAccountsForUser_FullMethodName:      {PermissionReadAccounts},
		identitypb.IdentityService_ListAccountMembers_FullMethodName:       {PermissionReadAccounts},
		identitypb.IdentityService_GetMembership_FullMethodName:            {PermissionReadAccounts},
		identitypb.IdentityService_ListMembershipsForUser_FullMethodName:   {PermissionReadAccounts},
		identitypb.IdentityService_ListAccounts_FullMethodName:             {PermissionListAllAccounts},
		identitypb.IdentityService_UpdateAccount_FullMethodName:            {PermissionUpdateAccounts},
		identitypb.IdentityService_TransferAccountOwnership_FullMethodName: {PermissionTransferAccountOwnership},
		identitypb.IdentityService_SetMembershipRoles_FullMethodName:       {PermissionManageMembers},
		identitypb.IdentityService_RemoveMembership_FullMethodName:         {PermissionManageMembers},

		// Invitations, the sender's side.
		identitypb.IdentityService_Invite_FullMethodName:           {PermissionInviteMembers},
		identitypb.IdentityService_CancelInvitation_FullMethodName: {PermissionInviteMembers},
		identitypb.IdentityService_GetInvitation_FullMethodName:    {PermissionReadInvitations},
	}
}

// Require declares every one of this service's methods onto a requirements
// builder: the permissioned ones with what they need, the self-service ones as
// public.
//
// It exists because the two-step version of this is a hole that fails silently.
// A consumer who calls RequireAll(Permissions()) and forgets the Public loop
// gets a builder that Builds cleanly and an enforcer that denies all eight
// self-service methods as undeclared — GetPrincipal among them, which is the
// whoami every client makes on load, so the application appears broken at sign
// in for a reason nothing connects to a missing loop. Nothing reports it at
// wiring time, because "declared nowhere" and "deliberately absent" look
// identical to a builder.
//
// It takes and returns the builder rather than building it, so a consumer
// composes several domains and their own methods into one table:
//
//	reqs, err := identitygrpc.Require(authzgrpc.NewRequirements()).
//		RequireAll(mealplanning.Permissions()).
//		Public(healthpb.Health_Check_FullMethodName).
//		Build()
//
// Overriding stays available: this is the default fragment, and a consumer who
// wants ListAccounts open to every member declares it differently before
// Build — a method declared twice is ErrDuplicateMethod, so the override is
// building the map yourself rather than calling this and amending it.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	b.RequireAll(Permissions())

	for _, method := range SelfServiceMethods() {
		b.Public(method)
	}

	return b
}

// SelfServiceMethods are the RPCs whose whole authorization is "this is the
// caller's own row", checked inside the method rather than by a permission.
//
// Every one of them takes its subject from the principal and has no target on
// the request: reading your own principal, saving your own profile, accepting an
// invitation you hold the token for, choosing where you land, listing what you
// sent or were sent. There is no permission that would make these safer, and one
// would make them wrong — an operator holding "identity.users.read" would not
// thereby be able to accept somebody else's invitation, because the method has
// no way to name one.
//
// They are a separate list rather than entries mapping to an empty slice because
// authorization/grpc refuses an empty requirement (ErrNoPermissionsRequired) and
// denies an undeclared method. Its word for "declared, and requires nothing" is
// Public, which is what a consumer passes these to:
//
//	builder := authzgrpc.NewRequirements().RequireAll(identitygrpc.Permissions())
//	for _, method := range identitygrpc.SelfServiceMethods() {
//		builder.Public(method)
//	}
//
// Public there means "no authorization check", not "no authentication": the
// consumer's authentication interceptor still runs, and every method here
// refuses a request with no principal on it.
func SelfServiceMethods() []string {
	return []string{
		identitypb.IdentityService_GetPrincipal_FullMethodName,
		identitypb.IdentityService_UpdateProfile_FullMethodName,
		identitypb.IdentityService_RecordAgreement_FullMethodName,
		identitypb.IdentityService_SetDefaultAccount_FullMethodName,
		identitypb.IdentityService_AcceptInvitation_FullMethodName,
		identitypb.IdentityService_RejectInvitation_FullMethodName,
		identitypb.IdentityService_ListInvitationsFromUser_FullMethodName,
		identitypb.IdentityService_ListInvitationsForEmailAddress_FullMethodName,
	}
}
