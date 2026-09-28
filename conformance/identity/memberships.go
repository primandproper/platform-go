package identity

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func memberships(t *testing.T, s *conformance.Session) {
	t.Helper()

	role, otherRole := membershipRoles(s)

	t.Run("setting a default account moves where the caller lands, and only there", func(t *testing.T) {
		t.Parallel()

		first := s.Subject(t, conformance.Making(getPrincipal, acceptInvitation, setDefaultAccount, listMembershipsForUser))
		needsAccount(t, first)
		second := colleague(t, s, first, conformance.Making(invite))
		needsAccount(t, second)
		join(t, s, second, first, role)

		ctx := first.Context(t.Context())

		response, err := first.Surfaces.Identity.SetDefaultAccount(ctx,
			&identitypb.SetDefaultAccountRequest{AccountId: second.AccountID})
		must.NoError(t, err)
		test.EqOp(t, second.AccountID, response.GetMembership().GetBelongsToAccount())
		test.True(t, response.GetMembership().GetDefaultAccount())

		held, err := first.Surfaces.Identity.ListMembershipsForUser(ctx,
			&identitypb.ListMembershipsForUserRequest{UserId: first.UserID})
		must.NoError(t, err)

		var defaults []string
		for _, m := range held.GetResults() {
			if m.GetDefaultAccount() {
				defaults = append(defaults, m.GetBelongsToAccount())
			}
		}

		test.Eq(t, []string{second.AccountID}, defaults,
			test.Sprint("the user lands in a number of accounts other than one"))
	})

	t.Run("a default account the caller is not in is refused as absent", func(t *testing.T) {
		t.Parallel()

		member := s.Subject(t, conformance.Making(setDefaultAccount))
		stranger := colleague(t, s, member)
		needsAccount(t, stranger)

		_, err := member.Surfaces.Identity.SetDefaultAccount(member.Context(t.Context()),
			&identitypb.SetDefaultAccountRequest{AccountId: stranger.AccountID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("setting membership roles replaces them rather than merging", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(invite, setMembershipRoles, getMembership))
		needsAccount(t, owner)
		member := colleague(t, s, owner, conformance.Making(getPrincipal, acceptInvitation))
		join(t, s, owner, member, otherRole, role)

		ctx := owner.Context(t.Context())

		response, err := owner.Surfaces.Identity.SetMembershipRoles(ctx, &identitypb.SetMembershipRolesRequest{
			AccountId: owner.AccountID,
			UserId:    member.UserID,
			Roles:     []string{role},
		})
		must.NoError(t, err)
		test.Eq(t, []string{role}, response.GetMembership().GetRoles(),
			test.Sprint("the roles were merged rather than replaced, so nothing here can revoke one"))

		read, err := owner.Surfaces.Identity.GetMembership(ctx, &identitypb.GetMembershipRequest{
			UserId:    member.UserID,
			AccountId: owner.AccountID,
		})
		must.NoError(t, err)
		test.Eq(t, []string{role}, read.GetMembership().GetRoles())
	})

	t.Run("removing a membership ends it", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(invite, listAccountMembers, removeMembership))
		needsAccount(t, owner)
		member := colleague(t, s, owner, conformance.Making(getPrincipal, acceptInvitation))
		join(t, s, owner, member, role)

		// The positive control: the member is on the roster before removal.
		must.SliceContains(t, memberIDs(t, owner, owner.AccountID), member.UserID)

		response, err := owner.Surfaces.Identity.RemoveMembership(owner.Context(t.Context()),
			&identitypb.RemoveMembershipRequest{AccountId: owner.AccountID, UserId: member.UserID})
		must.NoError(t, err)
		test.EqOp(t, member.UserID, response.GetMembership().GetBelongsToUser())
		test.EqOp(t, owner.AccountID, response.GetMembership().GetBelongsToAccount())

		test.SliceNotContains(t, memberIDs(t, owner, owner.AccountID), member.UserID,
			test.Sprint("a removed member is still on the roster"))
	})

	t.Run("the last owner cannot be removed", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(removeMembership))
		needsAccount(t, owner)

		_, err := owner.Surfaces.Identity.RemoveMembership(owner.Context(t.Context()),
			&identitypb.RemoveMembershipRequest{AccountId: owner.AccountID, UserId: owner.UserID})
		must.Error(t, err)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})

	t.Run("an absent membership is reported as absent", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(getMembership))
		needsAccount(t, owner)
		outsider := colleague(t, s, owner)

		_, err := owner.Surfaces.Identity.GetMembership(owner.Context(t.Context()),
			&identitypb.GetMembershipRequest{UserId: outsider.UserID, AccountId: owner.AccountID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("a user's memberships are refused to a caller from another directory", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s, conformance.Making(listMembershipsForUser))

		// The positive control: the caller reads its own.
		held, err := mine.Surfaces.Identity.ListMembershipsForUser(mine.Context(t.Context()),
			&identitypb.ListMembershipsForUserRequest{UserId: mine.UserID})
		must.NoError(t, err)
		must.SliceNotEmpty(t, held.GetResults(), must.Sprint("a registered caller holds no membership of its own"))

		_, err = mine.Surfaces.Identity.ListMembershipsForUser(mine.Context(t.Context()),
			&identitypb.ListMembershipsForUserRequest{UserId: theirs.UserID})
		notYours(t, err, "a neighboring directory's user's memberships")
	})

	// The per-person half of the directory wall above. Whose memberships a
	// caller may read is a question about the user named: themselves, or
	// somebody they share a live account with. A colleague in the same
	// directory is neither until they join, and then they are.
	t.Run("a user's memberships are refused to a colleague who shares no account with them", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(listMembershipsForUser, getPrincipal, acceptInvitation), conformance.AsMember())
		needsAccount(t, mine)
		stranger := colleague(t, s, mine, conformance.Making(invite))
		needsAccount(t, stranger)

		// The positive control: the caller reads its own.
		held, err := mine.Surfaces.Identity.ListMembershipsForUser(mine.Context(t.Context()),
			&identitypb.ListMembershipsForUserRequest{UserId: mine.UserID})
		must.NoError(t, err)
		must.SliceNotEmpty(t, held.GetResults(), must.Sprint("a registered caller holds no membership of its own"))

		_, err = mine.Surfaces.Identity.ListMembershipsForUser(mine.Context(t.Context()),
			&identitypb.ListMembershipsForUserRequest{UserId: stranger.UserID})
		notYours(t, err, "a colleague's memberships the caller shares no account with")

		// And the rule stated positively: sharing an account is what answers it.
		join(t, s, stranger, mine, role)

		_, err = mine.Surfaces.Identity.ListMembershipsForUser(mine.Context(t.Context()),
			&identitypb.ListMembershipsForUserRequest{UserId: stranger.UserID})
		must.NoError(t, err, must.Sprint("a user's memberships were refused to somebody who shares an account with them"))
	})

	t.Run("an account's roster joins each membership to its user and renders no credential", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(invite, listAccountMembers))
		needsAccount(t, owner)
		member := colleague(t, s, owner, conformance.Making(getPrincipal, acceptInvitation))
		join(t, s, owner, member, role)

		roster, err := owner.Surfaces.Identity.ListAccountMembers(owner.Context(t.Context()),
			&identitypb.ListAccountMembersRequest{AccountId: owner.AccountID})
		must.NoError(t, err)
		test.NotNil(t, roster.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		var users []string

		for _, m := range roster.GetResults() {
			must.NotNil(t, m.GetUser(), must.Sprint("a roster row carried a membership and no user"))
			must.NotNil(t, m.GetMembership(), must.Sprint("a roster row carried a user and no membership"))
			test.EqOp(t, m.GetUser().GetId(), m.GetMembership().GetBelongsToUser())
			test.StrNotContains(t, m.GetUser().String(), "argon2")

			users = append(users, m.GetUser().GetId())
		}

		test.True(t, slices.Contains(users, owner.UserID), test.Sprint("the owner is missing from the roster"))
		test.True(t, slices.Contains(users, member.UserID), test.Sprint("the member is missing from the roster"))
	})
}
