package grpc_test

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/identity"
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

	found, err := h.client.GetAccount(h.ctx(), &identitypb.GetAccountRequest{AccountId: mine.Account.ID})
	must.NoError(T, err)
	test.EqOp(T, mine.Account.Name, found.GetAccount().GetName())
	test.EqOp(T, mine.User.ID, found.GetAccount().GetOwnerUserId())

	// The neighbor's account reads as absent, for the reason a neighbor's user
	// does: from this directory it is not there, and any other answer tells the
	// caller an account they may not read exists.
	_, err = h.client.GetAccount(h.ctx(), &identitypb.GetAccountRequest{AccountId: theirs.Account.ID})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
	test.True(T, errors.Is(err, identity.ErrAccountNotFound))
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

	page, err := h.client.ListAccountsForUser(h.ctx(),
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

	owner := h.seedAccount(T, testScope, "owner")
	successor := h.seedUser(T, testScope, "successor")

	response, err := h.client.TransferAccountOwnership(h.ctx(),
		&identitypb.TransferAccountOwnershipRequest{
			AccountId:      owner.Account.ID,
			NewOwnerUserId: successor.ID,
		})
	must.NoError(T, err)
	test.EqOp(T, successor.ID, response.GetAccount().GetOwnerUserId())

	// And the new owner is on the roster, because an owner who is not a member
	// is an account whose every roster-driven check refuses the person
	// responsible for it.
	members, err := h.client.ListAccountMembers(h.ctx(),
		&identitypb.ListAccountMembersRequest{AccountId: owner.Account.ID})
	must.NoError(T, err)

	holders := make([]string, 0, len(members.GetResults()))
	for _, m := range members.GetResults() {
		holders = append(holders, m.GetMembership().GetBelongsToUser())
	}

	test.SliceContains(T, holders, successor.ID)
}

// TestTransferAccountOwnershipRefusesAStrangerToTheDirectory pins the refusal
// the store's scoped read of the new owner exists for: owner_user_id carries no
// scope and no foreign key, so nothing below that read would decline to store a
// neighbor's user id.
func TestTransferAccountOwnershipRefusesAStrangerToTheDirectory(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	owner := h.seedAccount(T, testScope, "owner")
	stranger := h.seedUser(T, otherScope, "stranger")

	_, err := h.client.TransferAccountOwnership(h.ctx(),
		&identitypb.TransferAccountOwnershipRequest{
			AccountId:      owner.Account.ID,
			NewOwnerUserId: stranger.ID,
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
