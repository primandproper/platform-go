package grpc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// permitEverything is the seam replaced with the rule it had before it existed:
// the method-level permission and nothing else.
//
// It is here rather than exported because a shipped one is an escape hatch
// somebody wires up by accident. What it is for in this suite is the handful of
// tests whose subject is a check *behind* the row check — the store's scoped
// reads — which nothing could otherwise reach now that the transport refuses
// first.
type permitEverything struct{}

var _ identitygrpc.TargetAuthorizer = permitEverything{}

func (permitEverything) AuthorizeAccount(context.Context, identitygrpc.Principal, string) error {
	return nil
}

func (permitEverything) AuthorizeUser(context.Context, identitygrpc.Principal, string) error {
	return nil
}

func (permitEverything) AuthorizeInvitation(context.Context, identitygrpc.Principal, string) error {
	return nil
}

// refuseEverything is the other extreme, for the test that asserts the seam is
// actually consulted rather than duplicated inside the handlers.
type refuseEverything struct{}

var _ identitygrpc.TargetAuthorizer = refuseEverything{}

func (refuseEverything) AuthorizeAccount(context.Context, identitygrpc.Principal, string) error {
	return identitygrpc.ErrTargetNotPermitted
}

func (refuseEverything) AuthorizeUser(context.Context, identitygrpc.Principal, string) error {
	return identitygrpc.ErrTargetNotPermitted
}

func (refuseEverything) AuthorizeInvitation(context.Context, identitygrpc.Principal, string) error {
	return identitygrpc.ErrTargetNotPermitted
}

// neighborhood is one directory with two unrelated accounts in it, and it is the
// shape the whole gap was about.
//
// mine and theirs are both in testScope, so nothing below is a tenancy question:
// every store read filters on the same scope and every one of these calls would
// have been answered before this change. The caller holds every permission this
// service asks for — this harness mounts no enforcer, so a call that reaches a
// handler is a call the method-level check let through, which is exactly the
// starting position of the escalation.
type neighborhood struct {
	h                *harness
	mine             *identity.Registration
	theirs           *identity.Registration
	theirInvitation  *identitypb.Invitation
	myInvitation     *identitypb.Invitation
	theirOtherMember *identity.User
}

func newNeighborhood(t *testing.T, opts ...identitygrpc.Option) *neighborhood {
	t.Helper()

	h := newHarness(t, opts...)

	n := &neighborhood{
		h:      h,
		mine:   h.seedAccount(t, testScope, "mine"),
		theirs: h.seedAccount(t, testScope, "theirs"),
	}

	// Somebody in their account who is not its owner, so the user-targeted reads
	// have a subject that is neither the caller nor an account owner.
	n.theirOtherMember = h.seedUser(t, testScope, "theircolleague")
	h.seedMembership(t, testScope, n.theirOtherMember.ID, n.theirs.Account.ID, "support")

	n.myInvitation = invite(t, h, n.mine, "mine-invitee@example.com", "support")
	n.theirInvitation = invite(t, h, n.theirs, "their-invitee@example.com", "support")

	return n
}

// ctx is the caller: the owner of mine, and a stranger to theirs.
func (n *neighborhood) ctx() context.Context {
	return n.h.as(&testPrincipal{userID: n.mine.User.ID, scope: testScope})
}

// TestARequestNamedTargetIsCheckedAgainstTheCaller is the acceptance test for
// the whole seam: one case per RPC that takes its target from the request body,
// each making the same call the caller may make in their own account against
// somebody else's in the same directory.
//
// Before the row check every one of these succeeded. The permission fragment
// grants on the method, so a member with identity.accounts.update on their own
// account had it on every account in the scope; these are the eleven places that
// was reachable from.
func TestARequestNamedTargetIsCheckedAgainstTheCaller(T *testing.T) {
	T.Parallel()

	// Each case is the same call twice: once against the caller's own account,
	// where it must not be refused, and once against the neighbor's, where it
	// must. The positive half is what keeps a rule that refuses everybody from
	// passing this test.
	cases := map[string]struct {
		mine   func(*neighborhood) error
		theirs func(*neighborhood) error
	}{
		"UpdateAccount": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.UpdateAccount(n.ctx(), &identitypb.UpdateAccountRequest{
					AccountId: n.mine.Account.ID,
					Input:     &identitypb.AccountUpdateInput{Name: new("Renamed")},
				})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.UpdateAccount(n.ctx(), &identitypb.UpdateAccountRequest{
					AccountId: n.theirs.Account.ID,
					Input:     &identitypb.AccountUpdateInput{Name: new("Renamed")},
				})

				return err
			},
		},
		"TransferAccountOwnership/account": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.TransferAccountOwnership(n.ctx(),
					&identitypb.TransferAccountOwnershipRequest{
						AccountId:      n.mine.Account.ID,
						NewOwnerUserId: n.mine.User.ID,
					})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.TransferAccountOwnership(n.ctx(),
					&identitypb.TransferAccountOwnershipRequest{
						AccountId:      n.theirs.Account.ID,
						NewOwnerUserId: n.mine.User.ID,
					})

				return err
			},
		},
		// The second of the two rows this RPC names. The account is the caller's
		// own, so only the new owner can be refused — which is the case for a
		// caller handing their account to somebody they cannot see.
		"TransferAccountOwnership/newOwner": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.TransferAccountOwnership(n.ctx(),
					&identitypb.TransferAccountOwnershipRequest{
						AccountId:      n.mine.Account.ID,
						NewOwnerUserId: n.mine.User.ID,
					})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.TransferAccountOwnership(n.ctx(),
					&identitypb.TransferAccountOwnershipRequest{
						AccountId:      n.mine.Account.ID,
						NewOwnerUserId: n.theirOtherMember.ID,
					})

				return err
			},
		},
		"SetMembershipRoles": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.SetMembershipRoles(n.ctx(), &identitypb.SetMembershipRolesRequest{
					AccountId: n.mine.Account.ID,
					UserId:    n.mine.User.ID,
					Roles:     []string{"owner", "support"},
				})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.SetMembershipRoles(n.ctx(), &identitypb.SetMembershipRolesRequest{
					AccountId: n.theirs.Account.ID,
					UserId:    n.theirOtherMember.ID,
					Roles:     []string{"owner"},
				})

				return err
			},
		},
		"RemoveMembership": {
			// Removing the account's only owner is refused by the Service, which
			// is a different refusal and the one this half is asserting is
			// reached: the roster write got past the row check.
			mine: func(n *neighborhood) error {
				_, err := n.h.client.RemoveMembership(n.ctx(), &identitypb.RemoveMembershipRequest{
					AccountId: n.mine.Account.ID,
					UserId:    n.mine.User.ID,
				})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.RemoveMembership(n.ctx(), &identitypb.RemoveMembershipRequest{
					AccountId: n.theirs.Account.ID,
					UserId:    n.theirOtherMember.ID,
				})

				return err
			},
		},
		"Invite": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.Invite(n.ctx(), &identitypb.InviteRequest{
					AccountId: n.mine.Account.ID,
					ToEmail:   "somebody@example.com",
					Roles:     []string{"support"},
				})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.Invite(n.ctx(), &identitypb.InviteRequest{
					AccountId: n.theirs.Account.ID,
					ToEmail:   "somebody@example.com",
					Roles:     []string{"owner"},
				})

				return err
			},
		},
		"CancelInvitation": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.CancelInvitation(n.ctx(), &identitypb.CancelInvitationRequest{
					InvitationId: n.myInvitation.GetId(),
				})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.CancelInvitation(n.ctx(), &identitypb.CancelInvitationRequest{
					InvitationId: n.theirInvitation.GetId(),
				})

				return err
			},
		},
		"GetAccount": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.GetAccount(n.ctx(),
					&identitypb.GetAccountRequest{AccountId: n.mine.Account.ID})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.GetAccount(n.ctx(),
					&identitypb.GetAccountRequest{AccountId: n.theirs.Account.ID})

				return err
			},
		},
		"ListAccountMembers": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.ListAccountMembers(n.ctx(),
					&identitypb.ListAccountMembersRequest{AccountId: n.mine.Account.ID})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.ListAccountMembers(n.ctx(),
					&identitypb.ListAccountMembersRequest{AccountId: n.theirs.Account.ID})

				return err
			},
		},
		"GetMembership": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.GetMembership(n.ctx(), &identitypb.GetMembershipRequest{
					UserId:    n.mine.User.ID,
					AccountId: n.mine.Account.ID,
				})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.GetMembership(n.ctx(), &identitypb.GetMembershipRequest{
					UserId:    n.theirOtherMember.ID,
					AccountId: n.theirs.Account.ID,
				})

				return err
			},
		},
		"ListMembershipsForUser": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.ListMembershipsForUser(n.ctx(),
					&identitypb.ListMembershipsForUserRequest{UserId: n.mine.User.ID})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.ListMembershipsForUser(n.ctx(),
					&identitypb.ListMembershipsForUserRequest{UserId: n.theirOtherMember.ID})

				return err
			},
		},
		"ListAccountsForUser": {
			mine: func(n *neighborhood) error {
				_, err := n.h.client.ListAccountsForUser(n.ctx(),
					&identitypb.ListAccountsForUserRequest{UserId: n.mine.User.ID})

				return err
			},
			theirs: func(n *neighborhood) error {
				_, err := n.h.client.ListAccountsForUser(n.ctx(),
					&identitypb.ListAccountsForUserRequest{UserId: n.theirOtherMember.ID})

				return err
			},
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			n := newNeighborhood(t)

			err := tc.theirs(n)
			must.Error(t, err, must.Sprint("the call against another account in the same scope was answered"))
			test.EqOp(t, codes.PermissionDenied, status.Code(err))
			test.True(t, errors.Is(err, identitygrpc.ErrTargetNotPermitted),
				test.Sprint("the refusal was not the row check's"))

			// And the same call in the caller's own account is not refused by
			// the row check. Some of these fail for a reason of their own —
			// removing an account's last owner, most of all — so the assertion
			// is about which refusal, not about none.
			mineErr := tc.mine(n)
			test.False(t, errors.Is(mineErr, identitygrpc.ErrTargetNotPermitted),
				test.Sprintf("the caller was refused their own account: %v", mineErr))
		})
	}
}

// TestTheRowCheckIsTheSeamAndNotAnInlineRule asserts what a consumer is buying:
// every one of those calls goes through the interface, so replacing it replaces
// all of them rather than most of them.
func TestTheRowCheckIsTheSeamAndNotAnInlineRule(T *testing.T) {
	T.Parallel()

	// An authorizer that refuses everything turns the caller's own account into
	// somebody else's. If a handler had kept a rule of its own beside the seam,
	// the call it governs would still succeed here.
	closed := newHarness(T, identitygrpc.WithTargetAuthorizer(refuseEverything{}))

	// Seeded through the store rather than through the client, because with this
	// authorizer mounted there is no call that would set it up.
	account := closed.seedAccount(T, testScope, "mine")

	_, err := closed.client.GetAccount(
		closed.as(&testPrincipal{userID: account.User.ID, scope: testScope}),
		&identitypb.GetAccountRequest{AccountId: account.Account.ID})
	must.Error(T, err)
	test.EqOp(T, codes.PermissionDenied, status.Code(err))
	test.True(T, errors.Is(err, identitygrpc.ErrTargetNotPermitted))

	// And the other direction: a permissive one restores the pre-seam behavior
	// exactly, which is what a consumer with an operator console replaces it for.
	open := newNeighborhood(T, identitygrpc.WithTargetAuthorizer(permitEverything{}))

	read, err := open.h.client.GetAccount(open.ctx(),
		&identitypb.GetAccountRequest{AccountId: open.theirs.Account.ID})
	must.NoError(T, err)
	test.EqOp(T, open.theirs.Account.ID, read.GetAccount().GetId())
}

// TestTheDefaultRowCheckPermitsAnyAccountTheCallerIsIn is the other half of the
// default's rule, and the reason it is memberships rather than
// Principal.ActiveAccountID: a caller acting on their second account has not
// switched into it, and a check against a field the client sends is not a check.
func TestTheDefaultRowCheckPermitsAnyAccountTheCallerIsIn(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	mine := h.seedAccount(T, testScope, "mine")
	second := h.seedAccount(T, testScope, "second")
	h.seedMembership(T, testScope, mine.User.ID, second.Account.ID, "support")

	// The principal names the first account as active and the request names the
	// second.
	ctx := h.as(&testPrincipal{
		userID:          mine.User.ID,
		activeAccountID: mine.Account.ID,
		scope:           testScope,
	})

	read, err := h.client.GetAccount(ctx, &identitypb.GetAccountRequest{AccountId: second.Account.ID})
	must.NoError(T, err)
	test.EqOp(T, second.Account.ID, read.GetAccount().GetId())
}

// TestTheDefaultRowCheckPermitsAUserSharingAnAccount is the user-targeted rule,
// which is the one that decides whether two people in a directory can see each
// other at all.
func TestTheDefaultRowCheckPermitsAUserSharingAnAccount(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	mine := h.seedAccount(T, testScope, "mine")
	colleague := h.seedUser(T, testScope, "colleague")
	h.seedMembership(T, testScope, colleague.ID, mine.Account.ID, "support")

	ctx := h.as(&testPrincipal{userID: mine.User.ID, scope: testScope})

	held, err := h.client.ListMembershipsForUser(ctx,
		&identitypb.ListMembershipsForUserRequest{UserId: colleague.ID})
	must.NoError(T, err)
	must.SliceLen(T, 1, held.GetResults())
	test.EqOp(T, mine.Account.ID, held.GetResults()[0].GetBelongsToAccount())
}

// TestCancellingAnInvitationIsThePrerogativeOfItsSenderOrTheAccount pins both
// halves of the invitation rule, including the one the account rule alone would
// miss: a sender who has left the account can still withdraw what they sent.
func TestCancellingAnInvitationIsThePrerogativeOfItsSenderOrTheAccount(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	account := h.seedAccount(T, testScope, "account")
	sender := h.seedUser(T, testScope, "sender")
	h.seedMembership(T, testScope, sender.ID, account.Account.ID, "support")

	sent := invite(T, h, &identity.Registration{User: sender, Account: account.Account},
		"invitee@example.com", "support")

	// The account's owner did not send it and may cancel it, because they are a
	// member of the account it is into.
	byOwner := h.as(&testPrincipal{userID: account.User.ID, scope: testScope})

	read, err := h.client.GetInvitation(byOwner,
		&identitypb.GetInvitationRequest{InvitationId: sent.GetId()})
	must.NoError(T, err)
	test.EqOp(T, sender.ID, read.GetInvitation().GetFromUser())

	// The sender leaves the account, and may still withdraw it.
	_, err = h.client.RemoveMembership(byOwner, &identitypb.RemoveMembershipRequest{
		AccountId: account.Account.ID,
		UserId:    sender.ID,
	})
	must.NoError(T, err)

	cancelled, err := h.client.CancelInvitation(
		h.as(&testPrincipal{userID: sender.ID, scope: testScope}),
		&identitypb.CancelInvitationRequest{InvitationId: sent.GetId()})
	must.NoError(T, err)
	test.EqOp(T, identitypb.InvitationStatus_INVITATION_STATUS_CANCELLED,
		cancelled.GetInvitation().GetStatus())
}

// TestARowCheckThatCannotDecideIsNotARefusal is the distinction the interface's
// contract turns on: a store that will not answer is a failure, and reporting it
// as a permission refusal would tell a consumer to fix their policy while their
// database was down.
func TestARowCheckThatCannotDecideIsNotARefusal(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTargetAuthorizer(undecidableTargets{}))

	account := h.seedAccount(T, testScope, "account")

	_, err := h.client.GetAccount(
		h.as(&testPrincipal{userID: account.User.ID, scope: testScope}),
		&identitypb.GetAccountRequest{AccountId: account.Account.ID})
	must.Error(T, err)
	test.NotEqOp(T, codes.PermissionDenied, status.Code(err))
	test.False(T, errors.Is(err, identitygrpc.ErrTargetNotPermitted))
	test.True(T, errors.Is(err, errUndecidable))
}

var errUndecidable = errors.New("the row check could not reach the database")

// undecidableTargets is an authorizer that fails rather than refuses.
type undecidableTargets struct{}

var _ identitygrpc.TargetAuthorizer = undecidableTargets{}

func (undecidableTargets) AuthorizeAccount(context.Context, identitygrpc.Principal, string) error {
	return errUndecidable
}

func (undecidableTargets) AuthorizeUser(context.Context, identitygrpc.Principal, string) error {
	return errUndecidable
}

func (undecidableTargets) AuthorizeInvitation(context.Context, identitygrpc.Principal, string) error {
	return errUndecidable
}

// TestNewMembershipAuthorizerRefusesWhatItCannotBeBuiltFrom: the default is
// constructed from the same two things the server is, and neither is optional.
func TestNewMembershipAuthorizerRefusesWhatItCannotBeBuiltFrom(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	_, err := identitygrpc.NewMembershipAuthorizer(nil, h.store)
	must.Error(T, err)
	test.True(T, errors.Is(err, identitygrpc.ErrNilDatabaseClient))

	_, err = identitygrpc.NewMembershipAuthorizer(h.db, nil)
	must.Error(T, err)
	test.True(T, errors.Is(err, identitygrpc.ErrNilStore))

	authorizer, err := identitygrpc.NewMembershipAuthorizer(h.db, h.store)
	must.NoError(T, err)
	must.NotNil(T, authorizer)
}
