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

// TestSetDefaultAccountMovesWhereTheCallerLands also pins the invariant the
// store's statement pair exists for: one default per user, not one per call. A
// setter that only wrote the flag would leave the caller with two.
func TestSetDefaultAccountMovesWhereTheCallerLands(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	first := h.seedAccount(T, testScope, "somebody")
	second := h.seedAccount(T, testScope, "elsewhere")
	h.seedMembership(T, testScope, first.User.ID, second.Account.ID, "member")

	ctx := h.as(&testPrincipal{userID: first.User.ID, scope: testScope})

	response, err := h.client.SetDefaultAccount(ctx,
		&identitypb.SetDefaultAccountRequest{AccountId: second.Account.ID})
	must.NoError(T, err)
	test.EqOp(T, second.Account.ID, response.GetMembership().GetBelongsToAccount())
	test.True(T, response.GetMembership().GetDefaultAccount())

	held, err := h.client.ListMembershipsForUser(ctx,
		&identitypb.ListMembershipsForUserRequest{UserId: first.User.ID})
	must.NoError(T, err)
	must.SliceLen(T, 2, held.GetResults())

	defaults := make([]string, 0, 1)
	for _, m := range held.GetResults() {
		if m.GetDefaultAccount() {
			defaults = append(defaults, m.GetBelongsToAccount())
		}
	}

	test.Eq(T, []string{second.Account.ID}, defaults,
		test.Sprint("the user lands in a number of accounts other than one"))
}

// TestSetDefaultAccountRefusesAnAccountTheCallerIsNotIn is why the subject is
// the caller and the target is still checked: naming an account is not joining
// it.
func TestSetDefaultAccountRefusesAnAccountTheCallerIsNotIn(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	member := h.seedAccount(T, testScope, "member")
	stranger := h.seedAccount(T, testScope, "stranger")

	ctx := h.as(&testPrincipal{userID: member.User.ID, scope: testScope})

	_, err := h.client.SetDefaultAccount(ctx,
		&identitypb.SetDefaultAccountRequest{AccountId: stranger.Account.ID})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
	test.True(T, errors.Is(err, identity.ErrMembershipNotFound))
}

// TestSetMembershipRolesReplacesRatherThanMerges is the property the doc claims
// and the one a merging setter would quietly break: a revocation sent as the
// remaining set has to actually revoke.
func TestSetMembershipRolesReplacesRatherThanMerges(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	account := h.seedAccount(T, testScope, "owner")
	member := h.seedUser(T, testScope, "member")
	h.seedMembership(T, testScope, member.ID, account.Account.ID, "billing", "support")

	response, err := h.client.SetMembershipRoles(h.ctx(), &identitypb.SetMembershipRolesRequest{
		AccountId: account.Account.ID,
		UserId:    member.ID,
		Roles:     []string{"support"},
	})
	must.NoError(T, err)

	test.Eq(T, []string{"support"}, response.GetMembership().GetRoles(),
		test.Sprint("the roles were merged rather than replaced, so nothing here can revoke one"))

	read, err := h.client.GetMembership(h.ctx(), &identitypb.GetMembershipRequest{
		UserId:    member.ID,
		AccountId: account.Account.ID,
	})
	must.NoError(T, err)
	test.Eq(T, []string{"support"}, read.GetMembership().GetRoles())
}

func TestRemoveMembershipEndsIt(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	account := h.seedAccount(T, testScope, "owner")
	member := h.seedUser(T, testScope, "member")
	h.seedMembership(T, testScope, member.ID, account.Account.ID, "support")

	response, err := h.client.RemoveMembership(h.ctx(), &identitypb.RemoveMembershipRequest{
		AccountId: account.Account.ID,
		UserId:    member.ID,
	})
	must.NoError(T, err)

	// What comes back is the membership as it stood, which is what the Service
	// documents and what a consumer striking a roster row needs: an ended
	// membership is returned by no read here, so this is the last moment
	// anything can name it.
	test.EqOp(T, member.ID, response.GetMembership().GetBelongsToUser())
	test.EqOp(T, account.Account.ID, response.GetMembership().GetBelongsToAccount())

	roster, err := h.client.ListAccountMembers(h.ctx(),
		&identitypb.ListAccountMembersRequest{AccountId: account.Account.ID})
	must.NoError(T, err)

	for _, m := range roster.GetResults() {
		test.NotEqOp(T, member.ID, m.GetMembership().GetBelongsToUser(),
			test.Sprint("a removed member is still on the roster"))
	}
}

// TestRemoveMembershipRefusesTheLastOwner is the FailedPrecondition the method's
// doc promises: an ownerless account fails every permission check that resolves
// through its owner, so the removal is refused rather than repaired afterwards.
func TestRemoveMembershipRefusesTheLastOwner(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	account := h.seedAccount(T, testScope, "owner")

	_, err := h.client.RemoveMembership(h.ctx(), &identitypb.RemoveMembershipRequest{
		AccountId: account.Account.ID,
		UserId:    account.User.ID,
	})
	must.Error(T, err)
	test.EqOp(T, codes.FailedPrecondition, status.Code(err))
	test.True(T, errors.Is(err, identity.ErrLastAccountOwner))
}

func TestGetMembershipSurfacesAnAbsenceAsNotFound(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	account := h.seedAccount(T, testScope, "owner")

	_, err := h.client.GetMembership(h.ctx(), &identitypb.GetMembershipRequest{
		UserId:    "nobody",
		AccountId: account.Account.ID,
	})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
	test.True(T, errors.Is(err, identity.ErrMembershipNotFound))
}

// TestListMembershipsForUserIsScopedToTheCallersDirectory: the user id on the
// request is not a way out of the caller's directory, because the scope the read
// filters on never came from the request.
func TestListMembershipsForUserIsScopedToTheCallersDirectory(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	theirs := h.seedAccount(T, otherScope, "theirs")

	held, err := h.client.ListMembershipsForUser(h.ctx(),
		&identitypb.ListMembershipsForUserRequest{UserId: theirs.User.ID})
	must.NoError(T, err)

	test.SliceEmpty(T, held.GetResults(),
		test.Sprint("a neighbor's memberships were listed for a caller who named their user id"))
}

// TestListAccountMembersJoinsEachMembershipToItsUser is the whole reason the
// roster read is its own method: a caller who had to resolve the users would
// make one query per row.
func TestListAccountMembersJoinsEachMembershipToItsUser(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	account := h.seedAccount(T, testScope, "owner")
	member := h.seedUser(T, testScope, "member")
	h.seedMembership(T, testScope, member.ID, account.Account.ID, "support")

	roster, err := h.client.ListAccountMembers(h.ctx(),
		&identitypb.ListAccountMembersRequest{AccountId: account.Account.ID})
	must.NoError(T, err)
	must.SliceLen(T, 2, roster.GetResults())
	test.NotNil(T, roster.GetPagination(), test.Sprint("a paged read answered with no pagination"))

	usernames := make([]string, 0, len(roster.GetResults()))

	for _, m := range roster.GetResults() {
		must.NotNil(T, m.GetUser(), must.Sprint("a roster row carried a membership and no user"))
		must.NotNil(T, m.GetMembership(), must.Sprint("a roster row carried a user and no membership"))
		test.EqOp(T, m.GetUser().GetId(), m.GetMembership().GetBelongsToUser())

		usernames = append(usernames, m.GetUser().GetUsername())

		// The joined user is the same redacted rendering every other read hands
		// back — a roster is not a way around it.
		test.StrNotContains(T, m.GetUser().String(), "argon2")
	}

	test.SliceContains(T, usernames, "owner")
	test.SliceContains(T, usernames, "member")
}
