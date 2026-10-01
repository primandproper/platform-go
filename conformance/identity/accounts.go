package identity

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func accounts(t *testing.T, s *conformance.Session) {
	t.Helper()

	role, _ := membershipRoles(s)

	t.Run("an account read is confined to the caller's directory", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s, conformance.Making(getAccount))
		needsAccount(t, mine)
		needsAccount(t, theirs)

		found, err := mine.Surfaces.Identity.GetAccount(mine.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: mine.AccountID})
		must.NoError(t, err, must.Sprint("this caller cannot read its own account; the refusal below proves nothing"))
		test.EqOp(t, mine.UserID, found.GetAccount().GetOwnerUserId())

		// This module's default refuses it as forbidden, since a caller has no
		// standing in an account they do not belong to; a deployment whose
		// operators have standing everywhere reads it and finds nothing. Both
		// are the wall.
		_, err = mine.Surfaces.Identity.GetAccount(mine.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: theirs.AccountID})
		notYours(t, err, "a neighboring directory's account")
	})

	// The read twin of the archival refusal below, and the per-person half of
	// the directory wall above: in one directory, an account is readable by
	// the people in it. An ordinary member, because an administrator whose
	// standing reaches every account is entitled to read this one.
	t.Run("an account the caller is not in is not the caller's to read", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(getAccount), conformance.AsMember())
		needsAccount(t, mine)
		theirs := colleague(t, s, mine)
		needsAccount(t, theirs)

		found, err := mine.Surfaces.Identity.GetAccount(mine.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: mine.AccountID})
		must.NoError(t, err, must.Sprint("this caller cannot read its own account; the refusal below proves nothing"))
		test.EqOp(t, mine.AccountID, found.GetAccount().GetId())

		_, err = mine.Surfaces.Identity.GetAccount(mine.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: theirs.AccountID})
		notYours(t, err, "a colleague's account the caller holds no membership in")
	})

	t.Run("an account listing pages the caller's directory only", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)
		needsAccount(t, mine)
		needsAccount(t, theirs)
		other := colleague(t, s, mine)
		needsAccount(t, other)

		// Every account in the directory, read by whoever the subject mints for
		// ListAccounts.
		operator := s.Subject(t, conformance.Making(listAccounts), conformance.InTenant(surface, mine.ScopeFor(surface)))

		page, err := operator.Surfaces.Identity.ListAccounts(operator.Context(t.Context()), &identitypb.ListAccountsRequest{})
		must.NoError(t, err)

		ids := accountIDs(page.GetResults())
		test.SliceContains(t, ids, mine.AccountID)
		test.SliceContains(t, ids, other.AccountID,
			test.Sprint("a colleague's account in the same directory was missing from its listing"))
		test.SliceNotContains(t, ids, theirs.AccountID)
		test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))
	})

	t.Run("a user's accounts are the ones they belong to", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(listAccountsForUser))
		needsAccount(t, mine)
		other := colleague(t, s, mine)
		needsAccount(t, other)

		page, err := mine.Surfaces.Identity.ListAccountsForUser(mine.Context(t.Context()),
			&identitypb.ListAccountsForUserRequest{UserId: mine.UserID})
		must.NoError(t, err)

		ids := accountIDs(page.GetResults())
		test.SliceContains(t, ids, mine.AccountID)
		test.SliceNotContains(t, ids, other.AccountID,
			test.Sprint("an account the named user belongs to nothing of was listed for them"))
	})

	t.Run("a transfer of ownership moves the account and puts the new owner on its roster", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(transferAccountOwnership, listAccountMembers, getPrincipal, acceptInvitation))
		needsAccount(t, owner)
		successor := colleague(t, s, owner, conformance.Making(invite))
		needsAccount(t, successor)

		// The successor is somebody the owner may name because they share an
		// account: the owner joins the successor's. They are not yet on the
		// roster of the account being handed over, which is what gives the
		// roster assertion below something to prove.
		join(t, s, successor, owner, role)

		response, err := owner.Surfaces.Identity.TransferAccountOwnership(owner.Context(t.Context()),
			&identitypb.TransferAccountOwnershipRequest{AccountId: owner.AccountID, NewOwnerUserId: successor.UserID})
		must.NoError(t, err)
		test.EqOp(t, successor.UserID, response.GetAccount().GetOwnerUserId())

		test.SliceContains(t, memberIDs(t, owner, owner.AccountID), successor.UserID,
			test.Sprint("the new owner is not on the roster of the account they own"))
	})

	t.Run("a transfer to somebody in another directory is refused", func(t *testing.T) {
		t.Parallel()

		owner, stranger := twoDirectories(t, s, conformance.Making(transferAccountOwnership, getAccount))
		needsAccount(t, owner)

		_, err := owner.Surfaces.Identity.TransferAccountOwnership(owner.Context(t.Context()),
			&identitypb.TransferAccountOwnershipRequest{AccountId: owner.AccountID, NewOwnerUserId: stranger.UserID})
		notYours(t, err, "a transfer to a user in a neighboring directory")

		// And the account is still the owner's.
		found, err := owner.Surfaces.Identity.GetAccount(owner.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: owner.AccountID})
		must.NoError(t, err)
		test.EqOp(t, owner.UserID, found.GetAccount().GetOwnerUserId())
	})

	// The per-person half of the directory wall above. The new owner has to be
	// somebody the owner shares an account with, and a colleague in the same
	// directory is not that until they do — which the join after the refusal
	// makes them, so the refusal stands next to the transfer it then allows.
	t.Run("a transfer to somebody who shares no account with the owner is refused until they do", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(transferAccountOwnership, getAccount, getPrincipal, acceptInvitation), conformance.AsMember())
		needsAccount(t, owner)
		stranger := colleague(t, s, owner, conformance.Making(invite))
		needsAccount(t, stranger)

		_, err := owner.Surfaces.Identity.TransferAccountOwnership(owner.Context(t.Context()),
			&identitypb.TransferAccountOwnershipRequest{AccountId: owner.AccountID, NewOwnerUserId: stranger.UserID})
		notYours(t, err, "a transfer to a colleague who shares no account with the owner")

		// And the account is still the owner's.
		found, err := owner.Surfaces.Identity.GetAccount(owner.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: owner.AccountID})
		must.NoError(t, err)
		test.EqOp(t, owner.UserID, found.GetAccount().GetOwnerUserId())

		// The positive control: once the two share an account, the same call
		// is answered.
		join(t, s, stranger, owner, role)

		response, err := owner.Surfaces.Identity.TransferAccountOwnership(owner.Context(t.Context()),
			&identitypb.TransferAccountOwnershipRequest{AccountId: owner.AccountID, NewOwnerUserId: stranger.UserID})
		must.NoError(t, err, must.Sprint("the transfer was refused after the two came to share an account; the refusal above proves nothing"))
		test.EqOp(t, stranger.UserID, response.GetAccount().GetOwnerUserId())
	})

	t.Run("archiving an account closes it and its roster", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(invite, archiveAccount, getAccount))
		needsAccount(t, owner)
		member := colleague(t, s, owner, conformance.Making(getPrincipal, acceptInvitation, listMembershipsForUser))
		join(t, s, owner, member, role)

		response, err := owner.Surfaces.Identity.ArchiveAccount(owner.Context(t.Context()),
			&identitypb.ArchiveAccountRequest{AccountId: owner.AccountID})
		must.NoError(t, err)
		test.EqOp(t, owner.AccountID, response.GetAccount().GetId())
		test.NotNil(t, response.GetAccount().GetArchivedAt(),
			test.Sprint("an archived account came back with no archival stamp"))

		_, err = owner.Surfaces.Identity.GetAccount(owner.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: owner.AccountID})
		must.Error(t, err, must.Sprint("an archived account was still readable"))

		held, err := member.Surfaces.Identity.ListMembershipsForUser(member.Context(t.Context()),
			&identitypb.ListMembershipsForUserRequest{UserId: member.UserID})
		must.NoError(t, err)

		for _, m := range held.GetResults() {
			test.NotEqOp(t, owner.AccountID, m.GetBelongsToAccount(),
				test.Sprint("a membership in an archived account survived the closure"))
		}
	})

	// The granted half of include_archived. An administrator holds the grant
	// that closes an account, which is the grant a deployment reads the archive
	// off, so the directory asked for with the archive in it answers with the
	// closed account. The refused half is identity/grpc's own: whether an
	// ordinary caller receives it turns on grants no subject is asked to
	// describe.
	t.Run("an administrator asking for closed accounts receives them", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(archiveAccount))
		needsAccount(t, owner)
		admin := s.Subject(t, conformance.AsAdmin(), conformance.Making(listAccounts),
			conformance.InTenant(surface, owner.ScopeFor(surface)))

		_, err := owner.Surfaces.Identity.ArchiveAccount(owner.Context(t.Context()),
			&identitypb.ArchiveAccountRequest{AccountId: owner.AccountID})
		must.NoError(t, err)

		// The control: without asking, the closed account is not in the
		// directory.
		test.SliceNotContains(t, accountIDs(directoryAccounts(t, admin)), owner.AccountID)

		test.SliceContains(t, accountIDs(closedAccountsToo(t, admin)), owner.AccountID,
			test.Sprint("an administrator asked for closed accounts and was answered without them"))
	})

	t.Run("archiving an account the caller is not in is refused and changes nothing", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(archiveAccount), conformance.AsMember())
		theirs := colleague(t, s, mine, conformance.Making(getAccount))
		needsAccount(t, theirs)

		_, err := mine.Surfaces.Identity.ArchiveAccount(mine.Context(t.Context()),
			&identitypb.ArchiveAccountRequest{AccountId: theirs.AccountID})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		found, err := theirs.Surfaces.Identity.GetAccount(theirs.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: theirs.AccountID})
		must.NoError(t, err)
		test.Nil(t, found.GetAccount().GetArchivedAt())
	})

	t.Run("an account update with no input is refused", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(updateAccount))
		needsAccount(t, mine)

		_, err := mine.Surfaces.Identity.UpdateAccount(mine.Context(t.Context()),
			&identitypb.UpdateAccountRequest{AccountId: mine.AccountID})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("an account update leaves what the request did not name", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(updateAccount))
		needsAccount(t, mine)
		ctx := mine.Context(t.Context())

		seeded, err := mine.Surfaces.Identity.UpdateAccount(ctx, &identitypb.UpdateAccountRequest{
			AccountId: mine.AccountID,
			Input: &identitypb.AccountUpdateInput{
				TimeZone:       new("Europe/Amsterdam"),
				BillingAddress: &identitypb.BillingAddress{Line1: "1 Main St", City: "Springfield", Country: "US"},
			},
		})
		must.NoError(t, err)
		test.EqOp(t, "Europe/Amsterdam", seeded.GetAccount().GetTimeZone())

		renamed, err := mine.Surfaces.Identity.UpdateAccount(ctx, &identitypb.UpdateAccountRequest{
			AccountId: mine.AccountID,
			Input:     &identitypb.AccountUpdateInput{Name: new("Renamed")},
		})
		must.NoError(t, err)
		test.EqOp(t, "Renamed", renamed.GetAccount().GetName())
		test.EqOp(t, "Europe/Amsterdam", renamed.GetAccount().GetTimeZone())
		test.EqOp(t, "1 Main St", renamed.GetAccount().GetBillingAddress().GetLine1())
		test.EqOp(t, "Springfield", renamed.GetAccount().GetBillingAddress().GetCity())

		// Named empty is cleared, which is the other half of "unnamed is left".
		cleared, err := mine.Surfaces.Identity.UpdateAccount(ctx, &identitypb.UpdateAccountRequest{
			AccountId: mine.AccountID,
			Input: &identitypb.AccountUpdateInput{
				TimeZone:       new(""),
				BillingAddress: &identitypb.BillingAddress{},
			},
		})
		must.NoError(t, err)
		test.EqOp(t, "Renamed", cleared.GetAccount().GetName())
		test.EqOp(t, "", cleared.GetAccount().GetTimeZone())
		test.EqOp(t, "", cleared.GetAccount().GetBillingAddress().GetLine1())
	})
}
