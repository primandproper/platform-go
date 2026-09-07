package grpc

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v14/database"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/observability"

	"google.golang.org/grpc/codes"
)

// ErrTargetNotPermitted indicates a caller who may make this call and may not
// make it against the row this request named.
//
// It is the answer to the half of authorization a per-method permission cannot
// reach. authorization/grpc decides from the full method name and the caller's
// grants, both of which it has before the request body is parsed; whether the
// account_id on that body is one this caller has any standing in is a different
// question, and it is the one [TargetAuthorizer] answers.
//
// It is a sentinel of its own rather than a wrap of a platform one, and it is
// deliberately not a wrap of identity.ErrMembershipNotFound even where a missing
// membership is what produced it. Wrapping that one would put it in the chain,
// and identity.GRPCMapper maps it to codes.NotFound — so a refusal would reach
// a client as an absence, and a caller probing account ids would be told apart
// the ones that exist from the ones that do not by watching which refusals said
// which. Every RPC answers this with codes.PermissionDenied at the call site,
// so it needs no mapper.
var ErrTargetNotPermitted = platformerrors.New("the caller may not act on the named target")

// TargetAuthorizer decides whether the caller may act on the row a request
// named.
//
// # Why it exists
//
// Eleven of this service's RPCs take their target from the request body, and the
// permission fragment in front of them is a grant on the method: a holder of
// identity.accounts.update may call UpdateAccount, and nothing in a per-method
// check says which account. Within a tenant that made a grant directory-wide —
// a member with a management grant on their own account held it on every
// account in the scope. This is where that is decided instead.
//
// It is not tenancy. The scope comes off the [Principal] and every store read
// filters on it, so nothing here crosses a directory; this is the check inside
// one.
//
// # Why it is a seam
//
// The same shape as [Principal]: this package names the question and a consumer
// may answer it. The consumer's authorization interceptor cannot — it holds the
// request and the grants and no handle to read a row with — so an interface
// that is called from the handler, where the store and the client already are,
// is the only place the answer is reachable.
//
// The default is [MembershipAuthorizer], and a consumer who says nothing gets
// it. That is the deliberate direction of the asymmetry: a wrong-open default
// is a within-tenant privilege escalation that nothing reports, and a
// wrong-closed one is a PermissionDenied in a consumer's test. A consumer with
// a different rule — an operator console, a support role that reads every
// account, a policy engine of their own — supplies it with
// [WithTargetAuthorizer].
//
// # What implementations owe
//
// A nil error means permitted. [ErrTargetNotPermitted] means refused, and
// reaches the client as codes.PermissionDenied. Any other error is a failure to
// decide — a database that would not answer — and reaches the client as
// codes.Internal, which is what keeps an unavailable store from reading as a
// refusal. The three are distinguished by errors.Is, so an implementation may
// wrap the sentinel with context of its own and still be refusing.
//
// Each method is called after the request has been found well formed and before
// anything reads or writes a row. A malformed request is answered as malformed
// whoever sent it, since saying so discloses nothing about any row; everything
// past that point is gated.
type TargetAuthorizer interface {
	// AuthorizeAccount is asked before an RPC acts on the account a request
	// named.
	AuthorizeAccount(ctx context.Context, caller Principal, accountID string) error

	// AuthorizeUser is asked before an RPC acts on the user a request named.
	AuthorizeUser(ctx context.Context, caller Principal, userID string) error

	// AuthorizeInvitation is asked before an RPC acts on the invitation a
	// request named. It is given the id rather than the row, because the
	// handler has not read one — the row is this method's to resolve if its
	// rule needs it, which is what the default does.
	AuthorizeInvitation(ctx context.Context, caller Principal, invitationID string) error
}

// MembershipAuthorizer is the default [TargetAuthorizer]: the caller's own
// memberships are what they may act through.
//
// # The three rules
//
// An account is permitted to a caller who holds a live membership in it. That
// is the narrowest rule that leaves the service usable, and it is the one the
// store already enforces on the read every authenticated request makes —
// GetPrincipal refuses an active account the user is not a member of — so the
// question is one this package was already asking somewhere else.
//
// A user is permitted to themselves, and otherwise to a caller who shares a live
// account with them. Sharing an account is what makes two people visible to each
// other in a directory; a caller who shares none with the named user is a
// stranger reading a stranger.
//
// An invitation is permitted to the user who sent it, and otherwise by the
// account rule applied to the account it is into. The sender is named
// explicitly because they may have left the account since — an invitation
// nobody can withdraw is worse than one its sender can.
//
// # What it deliberately does not do
//
// It does not consult [Principal.ActiveAccountID]. That field is whatever the
// consumer's authentication interceptor put there, frequently from a header the
// client sent, and comparing a request field against another request field is
// not a check. A live membership is a row.
//
// It does not know about grants, so it has no operator carve-out: a support role
// holding identity.users.read is refused a user they share no account with,
// because the permission is on the method and this type cannot see it. A
// consumer whose operators need the directory implements this interface with
// their grants in hand — that is what the seam is for.
//
// It checks the target and does not narrow the answer. ListAccountsForUser
// against a permitted user returns every account that user belongs to, the ones
// the caller is not in included: the rule decides whether the read happens, not
// what it returns. A consumer for whom that disclosure matters replaces the
// seam.
//
// Every rule costs at most two indexed reads on the client's reader, outside any
// transaction, and a call whose target is the caller costs none.
type MembershipAuthorizer struct {
	client database.Client
	store  identity.Store
}

var _ TargetAuthorizer = (*MembershipAuthorizer)(nil)

// NewMembershipAuthorizer builds the default authorizer over the same client and
// store the server was built with.
//
// NewServer builds one when no [WithTargetAuthorizer] was given, so a consumer
// calls this only to wrap it — a policy that permits what this permits and more
// composes rather than reimplements.
func NewMembershipAuthorizer(client database.Client, store identity.Store) (*MembershipAuthorizer, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if store == nil {
		return nil, ErrNilStore
	}

	return &MembershipAuthorizer{client: client, store: store}, nil
}

// AuthorizeAccount permits an account the caller holds a live membership in.
//
// An absent membership is [ErrTargetNotPermitted] and not the store's
// ErrMembershipNotFound, for the reason that sentinel gives: the store's answer
// maps to codes.NotFound, and a refusal that reached a client as an absence
// would tell a caller enumerating account ids which of them exist. A request
// naming no account is refused by the same read, since no membership joins
// anybody to nothing.
func (a *MembershipAuthorizer) AuthorizeAccount(ctx context.Context, caller Principal, accountID string) error {
	if caller == nil {
		return ErrTargetNotPermitted
	}

	_, err := a.store.GetMembership(ctx, a.client.Reader(), caller.Scope(), caller.UserID(), accountID)
	if err != nil {
		if errors.Is(err, identity.ErrMembershipNotFound) {
			return ErrTargetNotPermitted
		}

		return err
	}

	return nil
}

// AuthorizeUser permits the caller themselves, and any user they share a live
// account with.
//
// A user who belongs to no account is permitted to nobody but themselves, which
// is the honest reading: there is nowhere the caller and they are both members.
func (a *MembershipAuthorizer) AuthorizeUser(ctx context.Context, caller Principal, userID string) error {
	if caller == nil {
		return ErrTargetNotPermitted
	}

	if userID == "" {
		return ErrTargetNotPermitted
	}

	if userID == caller.UserID() {
		return nil
	}

	var (
		scope  = caller.Scope()
		reader = a.client.Reader()
	)

	callerMemberships, err := a.store.ListMembershipsForUser(ctx, reader, scope, caller.UserID())
	if err != nil {
		return err
	}

	// The caller's own memberships are read first, so a caller who belongs to
	// nothing costs one read rather than two.
	if len(callerMemberships) == 0 {
		return ErrTargetNotPermitted
	}

	targetMemberships, err := a.store.ListMembershipsForUser(ctx, reader, scope, userID)
	if err != nil {
		return err
	}

	callersAccounts := make(map[string]struct{}, len(callerMemberships))
	for _, membership := range callerMemberships {
		callersAccounts[membership.BelongsToAccount] = struct{}{}
	}

	for _, membership := range targetMemberships {
		if _, shared := callersAccounts[membership.BelongsToAccount]; shared {
			return nil
		}
	}

	return ErrTargetNotPermitted
}

// AuthorizeInvitation permits the invitation's sender, and otherwise applies
// [MembershipAuthorizer.AuthorizeAccount] to the account it is into.
//
// An invitation this scope does not have is [ErrTargetNotPermitted] rather than
// the store's absence, for the same reason the account rule refuses that way: a
// caller guessing invitation ids learns nothing from a refusal that reads the
// same whether the row is there or not. The RPC's own read still answers an
// absence as one for a caller who was permitted — a sender cancelling something
// already answered is told it is gone, not that it is somebody else's.
func (a *MembershipAuthorizer) AuthorizeInvitation(
	ctx context.Context,
	caller Principal,
	invitationID string,
) error {
	if caller == nil {
		return ErrTargetNotPermitted
	}

	invitation, err := a.store.GetInvitation(ctx, a.client.Reader(), caller.Scope(), invitationID)
	if err != nil {
		if errors.Is(err, identity.ErrInvitationNotFound) {
			return ErrTargetNotPermitted
		}

		return err
	}

	if invitation.FromUser == caller.UserID() {
		return nil
	}

	return a.AuthorizeAccount(ctx, caller, invitation.BelongsToAccount)
}

// The three helpers every request-named RPC calls, each turning what the
// authorizer said into the status a client sees.
//
// They are helpers rather than three lines per method because the code is the
// part that can be got wrong quietly, and there are two of them. A refusal is
// codes.PermissionDenied. Anything else is an authorizer that could not decide —
// a database that would not answer — and it is codes.Internal, because a
// refusal is a sentence about the caller and an outage is not: reporting the
// second as the first tells a consumer to widen their policy while their
// database is down, and leaves a dashboard counting server faults reading zero
// through it.

// authorizeOutcome maps an authorizer's answer onto a status. A nil error is
// permitted and returns nil.
func authorizeOutcome(
	op observability.Operation,
	err error,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	if err == nil {
		return nil
	}

	code := codes.Internal
	if errors.Is(err, ErrTargetNotPermitted) {
		code = codes.PermissionDenied
	}

	return grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), code, descriptionFmt, descriptionArgs...)
}

func (s *Server) authorizeAccount(
	ctx context.Context,
	op observability.Operation,
	caller Principal,
	accountID string,
) error {
	return authorizeOutcome(op, s.targets.AuthorizeAccount(ctx, caller, accountID),
		"authorizing the caller against account %q", accountID)
}

func (s *Server) authorizeUser(
	ctx context.Context,
	op observability.Operation,
	caller Principal,
	userID string,
) error {
	return authorizeOutcome(op, s.targets.AuthorizeUser(ctx, caller, userID),
		"authorizing the caller against user %q", userID)
}

func (s *Server) authorizeInvitation(
	ctx context.Context,
	op observability.Operation,
	caller Principal,
	invitationID string,
) error {
	return authorizeOutcome(op, s.targets.AuthorizeInvitation(ctx, caller, invitationID),
		"authorizing the caller against invitation %q", invitationID)
}
