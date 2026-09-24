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

	t.Run("an account read is confined to the caller's directory", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)
		needsAccount(t, mine)
		needsAccount(t, theirs)

		found, err := mine.Surfaces.Identity.GetAccount(mine.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: mine.AccountID})
		must.NoError(t, err, must.Sprint("this caller cannot read its own account; the refusal below proves nothing"))
		test.EqOp(t, mine.UserID, found.GetAccount().GetOwnerUserId())

		// Refused rather than absent: an account is a target the caller named,
		// and the directory's default rule is that a caller has no standing in
		// one they do not belong to.
		_, err = mine.Surfaces.Identity.GetAccount(mine.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: theirs.AccountID})
		must.Error(t, err, must.Sprint("a neighboring directory's account was readable"))
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("an account listing pages the caller's directory only", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)
		needsAccount(t, mine)
		needsAccount(t, theirs)
		other := colleague(t, s, mine)
		needsAccount(t, other)

		page, err := mine.Surfaces.Identity.ListAccounts(mine.Context(t.Context()), &identitypb.ListAccountsRequest{})
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

		mine := s.Subject(t)
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

		owner := s.Subject(t)
		needsAccount(t, owner)
		successor := colleague(t, s, owner)
		needsAccount(t, successor)

		// The successor is somebody the owner may name because they share an
		// account: the owner joins the successor's. They are not yet on the
		// roster of the account being handed over, which is what gives the
		// roster assertion below something to prove.
		join(t, s, successor, owner, "member")

		response, err := owner.Surfaces.Identity.TransferAccountOwnership(owner.Context(t.Context()),
			&identitypb.TransferAccountOwnershipRequest{AccountId: owner.AccountID, NewOwnerUserId: successor.UserID})
		must.NoError(t, err)
		test.EqOp(t, successor.UserID, response.GetAccount().GetOwnerUserId())

		test.SliceContains(t, memberIDs(t, owner, owner.AccountID), successor.UserID,
			test.Sprint("the new owner is not on the roster of the account they own"))
	})

	t.Run("a transfer to somebody in another directory is refused", func(t *testing.T) {
		t.Parallel()

		owner, stranger := twoDirectories(t, s)
		needsAccount(t, owner)

		_, err := owner.Surfaces.Identity.TransferAccountOwnership(owner.Context(t.Context()),
			&identitypb.TransferAccountOwnershipRequest{AccountId: owner.AccountID, NewOwnerUserId: stranger.UserID})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		// And the account is still the owner's.
		found, err := owner.Surfaces.Identity.GetAccount(owner.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: owner.AccountID})
		must.NoError(t, err)
		test.EqOp(t, owner.UserID, found.GetAccount().GetOwnerUserId())
	})

	t.Run("archiving an account closes it and its roster", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t)
		needsAccount(t, owner)
		member := colleague(t, s, owner)
		join(t, s, owner, member, "member")

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

	t.Run("archiving an account the caller is not in is refused and changes nothing", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		theirs := colleague(t, s, mine)
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

		mine := s.Subject(t)
		needsAccount(t, mine)

		_, err := mine.Surfaces.Identity.UpdateAccount(mine.Context(t.Context()),
			&identitypb.UpdateAccountRequest{AccountId: mine.AccountID})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("an account update leaves what the request did not name", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
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
