package grpc

import (
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/authorization"
	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"
)

// The permissions this service's administrative methods require, in
// authorization's vocabulary.
//
// They are declared here rather than in the authorization package because
// authorization is a primitive: it owns what a Permission *is* — a string, a
// set, a role that inherits — and owns no domain's names.
//
// The strings are dotted and namespaced so that a consumer composing several
// domains' fragments cannot have two of them collide on "read". They are values
// rather than an enum because a consumer's policy is data — a YAML file, a table
// of roles — and it has to be able to name one without importing Go.
//
// There are ten of them over fourteen RPCs, and the two collapses are the
// ordinary one: each read covers a get and its list, because they answer the
// same question at two cardinalities and a grant that separated them would let a
// consumer allow enumeration while forbidding the read it enumerates into. A
// consumer who wants the page behind a stronger grant than the get overrides the
// map, which is what [Permissions] returning a fresh one is for.
const (
	// PermissionCreateLists covers opening a waitlist.
	//
	// Which lists a deployment runs is a decision in the same sense a product
	// launch is, which is why nothing on the public half creates one.
	PermissionCreateLists authorization.Permission = "waitlists.lists.create"

	// PermissionReadLists covers reading or paging the tenant's catalog, open
	// and closed alike.
	//
	// It is not the grant in front of ListOpenLists, which is behind none: the
	// open lists are what a signup page publishes, and the closed and archived
	// ones are the operator's view of a launch that is over.
	PermissionReadLists authorization.Permission = "waitlists.lists.read"

	// PermissionUpdateLists covers revising a list's name, description and
	// closing time.
	//
	// Moving the closing time is how a list is extended or brought in, and it is
	// the sharpest thing in this grant: bringing it forward closes a list to new
	// signups without touching anybody already on it, and pushing it out lets the
	// next person through.
	PermissionUpdateLists authorization.Permission = "waitlists.lists.update"

	// PermissionArchiveLists covers retiring a list. The signups against it are
	// left alone and stay readable, because archiving is not erasure; what it
	// does do is close the list immediately, whatever its closing time says.
	PermissionArchiveLists authorization.Permission = "waitlists.lists.archive"

	// PermissionReadSignups covers every read of who is on a list: one signup by
	// id, one by the address it was made with, a list's page, and one
	// principal's signups across the tenant.
	//
	// It is the grant to think hardest about, because GetSignupByContact is
	// behind it. A holder can ask whether any address they can type is on any
	// list in the tenant, which is what the list holds and what a person joining
	// one has not agreed to publish. That read is the reason the public half of
	// this service stops at the open catalog.
	PermissionReadSignups authorization.Permission = "waitlists.signups.read"

	// PermissionUpdateSignups covers rewriting the operator's note against a
	// signup.
	//
	// It is deliberately lighter than the two transitions below: a note moves
	// nobody, and the store leaves status_changed_at alone precisely so that a
	// typo fixed here does not reschedule somebody's reminder.
	PermissionUpdateSignups authorization.Permission = "waitlists.signups.update"

	// PermissionInviteSignups covers moving a waiting signup to invited.
	//
	// It is the grant that sends an email, in the sense that the invitation a
	// consumer sends is scheduled off the moment this stamps. A holder decides
	// whose turn it is.
	PermissionInviteSignups authorization.Permission = "waitlists.signups.invite"

	// PermissionConvertSignups covers moving an invited signup to converted.
	//
	// It is separate from PermissionInviteSignups rather than collapsed with it,
	// because the two are different acts by different callers: deciding whose
	// turn it is, and recording that somebody took theirs. The second is
	// frequently an integration reporting an outcome — whatever the person
	// became is a row of the consumer's, written in the same transaction — and
	// an integration that could also decide who gets in is a grant nobody meant
	// to hand out.
	PermissionConvertSignups authorization.Permission = "waitlists.signups.convert"

	// PermissionArchiveSignups covers retiring a signup administratively.
	//
	// It is not a withdrawal and the store says so at length: the row is hidden
	// and nothing about what it holds changes, so the contact is still stored and
	// nothing is suppressed. Somebody asking to come off a list reaches Withdraw,
	// which is on the public half and behind no grant at all.
	PermissionArchiveSignups authorization.Permission = "waitlists.signups.archive"

	// PermissionEraseSignups covers withdrawing every signup one principal holds
	// across the tenant, archived signups included.
	//
	// It is its own grant and the sharpest read-write pair in this file. It is
	// the erasure path — waitlists/privacy is the dataprivacy.Eraser built on it
	// — and a holder can take a named person off every list in the deployment in
	// one call. Nothing undoes it: the rows keep a digest and lose the address,
	// which is the point.
	PermissionEraseSignups authorization.Permission = "waitlists.signups.erase"
)

// PublicMethods are the RPCs that require no grant at all.
//
// Three of them, and they are the signup page: the open catalog it renders, the
// form it submits, and the unsubscribe link in the mail that follows. Public
// here means "no authorization check", not "no authentication" — the consumer's
// authentication interceptor still runs, and a caller who does arrive with a
// principal has their signup attributed to them.
//
// It is a list rather than a paragraph for the reason
// authentication/signin/grpc's is: the difference between "declared and requires
// nothing" and "not declared" is the difference between a service that works and
// one whose every method the enforcer denies, and nothing reports the second at
// wiring time.
//
// Withdraw is on this list and is not thereby unguarded. It names a row, and the
// standing to move that row is [SignupAuthorizer]'s — a seam with no default,
// asked inside the handler, because it is the question a grant on the method
// could not have answered.
func PublicMethods() []string {
	return []string{
		waitlistspb.WaitlistsService_ListOpenLists_FullMethodName,
		waitlistspb.WaitlistsService_Join_FullMethodName,
		waitlistspb.WaitlistsService_Withdraw_FullMethodName,
	}
}

// Permissions is the default map from method name to what it requires: the
// fourteen administrative RPCs, and nothing else.
//
// The three in [PublicMethods] are deliberately absent, and permissions_test.go
// reads the service descriptor rather than a list in order to check that every
// method is in exactly one of the two — so an RPC added later and decided about
// in neither fails there rather than being denied in somebody's production.
//
// The keys are the generated full method name constants rather than strings, so
// an RPC renamed in the .proto is a compile error here instead of a method that
// silently requires nothing.
//
// It returns a fresh map each call, so a consumer composing it into their own
// policy and then overriding an entry is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		waitlistspb.WaitlistsService_CreateList_FullMethodName:  {PermissionCreateLists},
		waitlistspb.WaitlistsService_GetList_FullMethodName:     {PermissionReadLists},
		waitlistspb.WaitlistsService_ListLists_FullMethodName:   {PermissionReadLists},
		waitlistspb.WaitlistsService_UpdateList_FullMethodName:  {PermissionUpdateLists},
		waitlistspb.WaitlistsService_ArchiveList_FullMethodName: {PermissionArchiveLists},

		waitlistspb.WaitlistsService_GetSignup_FullMethodName:             {PermissionReadSignups},
		waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName:    {PermissionReadSignups},
		waitlistspb.WaitlistsService_ListSignups_FullMethodName:           {PermissionReadSignups},
		waitlistspb.WaitlistsService_ListSignupsForSubject_FullMethodName: {PermissionReadSignups},

		waitlistspb.WaitlistsService_UpdateSignupNotes_FullMethodName: {PermissionUpdateSignups},
		waitlistspb.WaitlistsService_Invite_FullMethodName:            {PermissionInviteSignups},
		waitlistspb.WaitlistsService_Convert_FullMethodName:           {PermissionConvertSignups},
		waitlistspb.WaitlistsService_ArchiveSignup_FullMethodName:     {PermissionArchiveSignups},

		waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName: {PermissionEraseSignups},
	}
}

// Require declares every method of this service on a requirements builder: the
// fourteen behind their grants and the three as public.
//
// It is the exported name rather than a paragraph asking a consumer to write the
// loop, because authorization/grpc is fail-closed — a method declared nowhere is
// denied — and what a consumer needs is a call that stays correct when this
// service's method set changes.
//
// A method declared twice is ErrDuplicateMethod, so a consumer who wants one of
// the public three gated after all declares the whole set themselves rather than
// calling this and amending it.
//
// A nil builder is tolerated and returns nil, so composing several domains'
// fragments in a chain does not need a nil check per link.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	for _, method := range PublicMethods() {
		b.Public(method)
	}

	return b.RequireAll(Permissions())
}
