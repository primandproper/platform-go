package grpc_test

import (
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

func TestGetAccountIsScopedToTheCallersDirectory(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	mine := h.seedAccount(T, testScope, "mine")
	theirs := h.seedAccount(T, otherScope, "theirs")

	ctx := h.as(&testPrincipal{userID: mine.User.ID, scope: testScope})

	found, err := h.client.GetAccount(ctx, &identitypb.GetAccountRequest{AccountId: mine.Account.ID})
	must.NoError(T, err)
	test.EqOp(T, mine.Account.Name, found.GetAccount().GetName())
	test.EqOp(T, mine.User.ID, found.GetAccount().GetOwnerUserId())

	// The neighbor's account is refused, and by the same answer an account in
	// this directory the caller is not a member of gets: the row check runs
	// before the scoped read, so the caller cannot tell "elsewhere" from "not
	// yours" and neither answer says whether the id names anything.
	_, err = h.client.GetAccount(ctx, &identitypb.GetAccountRequest{AccountId: theirs.Account.ID})
	must.Error(T, err)
	test.EqOp(T, codes.PermissionDenied, status.Code(err))
	test.True(T, errors.Is(err, identitygrpc.ErrTargetNotPermitted))
}

func TestListAccountsPagesTheCallersDirectoryOnly(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	h.seedAccount(T, testScope, "one")
	h.seedAccount(T, testScope, "two")
	h.seedAccount(T, otherScope, "elsewhere")

	page, err := h.client.ListAccounts(h.ctx(), &identitypb.ListAccountsRequest{})
	must.NoError(T, err)

	names := accountNames(page.GetResults())
	test.SliceContains(T, names, "one's account")
	test.SliceContains(T, names, "two's account")
	test.SliceNotContains(T, names, "elsewhere's account")
	test.NotNil(T, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))
}

func TestListAccountsForUserAnswersTheAccountsTheyBelongTo(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	mine := h.seedAccount(T, testScope, "mine")
	somebodyElse := h.seedAccount(T, testScope, "somebodyelse")

	ctx := h.as(&testPrincipal{userID: mine.User.ID, scope: testScope})

	page, err := h.client.ListAccountsForUser(ctx,
		&identitypb.ListAccountsForUserRequest{UserId: mine.User.ID})
	must.NoError(T, err)

	names := accountNames(page.GetResults())
	test.SliceContains(T, names, mine.Account.Name)
	test.SliceNotContains(T, names, somebodyElse.Account.Name,
		test.Sprint("an account the named user belongs to nothing of was listed for them"))
}

func TestTransferAccountOwnershipMovesTheAccount(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	// The successor is a colleague rather than a stranger: they own an account
	// of their own that the transferring owner is also a member of, which is
	// what makes them somebody the caller may name. They are not a member of the
	// account being handed over, which is what leaves the roster assertion below
	// with something to prove.
	owner := h.seedAccount(T, testScope, "owner")
	successor := h.seedAccount(T, testScope, "successor")
	h.seedMembership(T, testScope, owner.User.ID, successor.Account.ID, "member")

	ctx := h.as(&testPrincipal{userID: owner.User.ID, scope: testScope})

	response, err := h.client.TransferAccountOwnership(ctx,
		&identitypb.TransferAccountOwnershipRequest{
			AccountId:      owner.Account.ID,
			NewOwnerUserId: successor.User.ID,
		})
	must.NoError(T, err)
	test.EqOp(T, successor.User.ID, response.GetAccount().GetOwnerUserId())

	// And the new owner is on the roster, because an owner who is not a member
	// is an account whose every roster-driven check refuses the person
	// responsible for it.
	members, err := h.client.ListAccountMembers(ctx,
		&identitypb.ListAccountMembersRequest{AccountId: owner.Account.ID})
	must.NoError(T, err)

	holders := make([]string, 0, len(members.GetResults()))
	for _, m := range members.GetResults() {
		holders = append(holders, m.GetMembership().GetBelongsToUser())
	}

	test.SliceContains(T, holders, successor.User.ID)
}

// TestTransferAccountOwnershipRefusesAStrangerToTheDirectory pins both refusals
// a neighbor's user id now meets, in the order they run.
//
// The transport's is first: a user the caller shares no live account with is not
// one they may hand an account to, and a neighbor's is the extreme case of that.
// The store's is behind it and is the one that matters if a consumer replaces
// the seam — owner_user_id carries no scope and no foreign key, so nothing below
// that read would decline to store a neighbor's user id. The second half of this
// test asks with the row check disabled, which is the only way to reach it.
func TestTransferAccountOwnershipRefusesAStrangerToTheDirectory(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	owner := h.seedAccount(T, testScope, "owner")
	stranger := h.seedUser(T, otherScope, "stranger")

	ctx := h.as(&testPrincipal{userID: owner.User.ID, scope: testScope})

	_, err := h.client.TransferAccountOwnership(ctx,
		&identitypb.TransferAccountOwnershipRequest{
			AccountId:      owner.Account.ID,
			NewOwnerUserId: stranger.ID,
		})
	must.Error(T, err)
	test.EqOp(T, codes.PermissionDenied, status.Code(err))
	test.True(T, errors.Is(err, identitygrpc.ErrTargetNotPermitted))

	open := newHarness(T, identitygrpc.WithTargetAuthorizer(permitEverything{}))

	openOwner := open.seedAccount(T, testScope, "owner")
	openStranger := open.seedUser(T, otherScope, "stranger")

	_, err = open.client.TransferAccountOwnership(
		open.as(&testPrincipal{userID: openOwner.User.ID, scope: testScope}),
		&identitypb.TransferAccountOwnershipRequest{
			AccountId:      openOwner.Account.ID,
			NewOwnerUserId: openStranger.ID,
		})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
	test.True(T, errors.Is(err, identity.ErrUserNotFound))
}

// TestUpdateAccountRefusesAnAbsentInput is the branch that separates "the client
// sent an empty form" from "the client sent no form": the first clears nothing,
// because every field is optional, and the second is a request that named an
// account and asked for nothing.
func TestUpdateAccountRefusesAnAbsentInput(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	account := h.seedAccount(T, testScope, "somebody")

	_, err := h.client.UpdateAccount(h.ctx(),
		&identitypb.UpdateAccountRequest{AccountId: account.Account.ID})
	must.Error(T, err)
	test.EqOp(T, codes.InvalidArgument, status.Code(err))
	test.True(T, errors.Is(err, identity.ErrNilAccountUpdate))
}

func accountNames(accounts []*identitypb.Account) []string {
	out := make([]string, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, a.GetName())
	}

	return out
}
