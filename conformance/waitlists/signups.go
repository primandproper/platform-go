package waitlists

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func signups(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a signup read by id is scoped to the caller's tenant", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		list := openList(t, mine, open())
		contact := freshContact()
		signup := signedUp(t, mine, mine, list.GetId(), contact)

		// The positive control, through the same RPC the neighbor is refused.
		read, err := mine.Surfaces.Waitlists.GetSignup(mine.Context(t.Context()),
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err, must.Sprint("this caller cannot read its own signup; the absence below proves nothing"))
		test.EqOp(t, signup.GetId(), read.GetResult().GetId())
		test.EqOp(t, contact, read.GetResult().GetContact())

		_, err = theirs.Surfaces.Waitlists.GetSignup(theirs.Context(t.Context()),
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.Error(t, err, must.Sprint("a neighboring tenant's signup was readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	// The list is half of what addresses a signup: a read that omitted it
	// could hand one list's row to a caller holding another list's id.
	t.Run("a signup named against the wrong list is absent", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		other := openList(t, operator, open())
		signup := signedUp(t, operator, operator, list.GetId(), freshContact())

		_, err := operator.Surfaces.Waitlists.GetSignup(operator.Context(t.Context()),
			&waitlistspb.GetSignupRequest{ListId: other.GetId(), SignupId: signup.GetId()})
		must.Error(t, err, must.Sprint("a signup was answered under a list it is not on"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	// The address as it was given is what is stored, so a mail client renders
	// the capitalization somebody typed; the address as it is compared is
	// folded, so an operator finds it whichever way they have it.
	t.Run("a signup is found by whichever capitalization the operator has", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		typed := "Conf." + freshContact()

		join(t, operator, list.GetId(), typed)

		found := byContact(t, operator, list.GetId(), strings.ToUpper(typed))
		must.NotNil(t, found, must.Sprint("a signup was not found by its address in another case"))
		test.EqOp(t, typed, found.GetContact(),
			test.Sprint("the address was stored as something other than what was typed"))
	})

	t.Run("a signup read by its address is scoped to the caller's tenant", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		list := openList(t, mine, open())
		contact := freshContact()

		// signedUp is the positive control: it fails unless this caller finds
		// its own signup by the address.
		signedUp(t, mine, mine, list.GetId(), contact)

		// The read behind "is this address on this list" is the one this
		// surface is most careful with, and a neighbor must find nothing.
		test.Nil(t, byContact(t, theirs, list.GetId(), contact),
			test.Sprint("a neighboring tenant's signup was found by its address"))
	})

	t.Run("a list's signups are its own and its tenant's", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		list := openList(t, mine, open())
		other := openList(t, mine, open())

		first := signedUp(t, mine, mine, list.GetId(), freshContact())
		second := signedUp(t, mine, mine, list.GetId(), freshContact())
		elsewhere := signedUp(t, mine, mine, other.GetId(), freshContact())

		page, err := mine.Surfaces.Waitlists.ListSignups(mine.Context(t.Context()),
			&waitlistspb.ListSignupsRequest{ListId: list.GetId()})
		must.NoError(t, err)
		test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		ids := signupIDs(page.GetResults())
		test.SliceContains(t, ids, first.GetId())
		test.SliceContains(t, ids, second.GetId())
		test.SliceNotContains(t, ids, elsewhere.GetId(),
			test.Sprint("another list's signup reached this list's page"))

		// The neighbor names the list and gets none of it: the page is
		// answered, and it is empty of this tenant's rows.
		theirPage, err := theirs.Surfaces.Waitlists.ListSignups(theirs.Context(t.Context()),
			&waitlistspb.ListSignupsRequest{ListId: list.GetId()})
		if status.Code(err) != codes.NotFound {
			must.NoError(t, err)
			test.SliceNotContains(t, signupIDs(theirPage.GetResults()), first.GetId(),
				test.Sprint("a neighboring tenant's signup reached this caller's page"))
		}
	})

	// The read is inside the transaction the write ran in, so the response
	// carries the moment the write stamped — the field a reminder is scheduled
	// off.
	t.Run("an invitation moves a waiting signup and answers with the moment it moved", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		signup := signedUp(t, operator, operator, list.GetId(), freshContact())

		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WAITING, signup.GetStatus())
		test.Nil(t, signup.GetStatusChangedAt(),
			test.Sprint("a signup nobody has moved says it was moved"))

		invited, err := operator.Surfaces.Waitlists.Invite(operator.Context(t.Context()),
			&waitlistspb.InviteRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_INVITED, invited.GetResult().GetStatus())
		must.NotNil(t, invited.GetResult().GetStatusChangedAt(),
			must.Sprint("an invitation answered with no moment it moved"))
		test.False(t, invited.GetResult().GetStatusChangedAt().AsTime().IsZero())
	})

	// The guard is the affected-row count of one update rather than a decision
	// made on a read, which is what makes a transition happen exactly once —
	// and the refusal is a client-safe one, in the sentinel's own words,
	// because the page rendering it has three FailedPreconditions to tell
	// apart.
	t.Run("a second invitation is refused, in words a person can read", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		signup := signedUp(t, operator, operator, list.GetId(), freshContact())
		ctx := operator.Context(t.Context())

		_, err := operator.Surfaces.Waitlists.Invite(ctx,
			&waitlistspb.InviteRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)

		_, err = operator.Surfaces.Waitlists.Invite(ctx,
			&waitlistspb.InviteRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.Error(t, err, must.Sprint("one signup was invited twice"))
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.StrContains(t, status.Convert(err).Message(), "status")
	})

	t.Run("an invitation will not reach another tenant's signup", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		list := openList(t, mine, open())
		signup := signedUp(t, mine, mine, list.GetId(), freshContact())

		_, err := theirs.Surfaces.Waitlists.Invite(theirs.Context(t.Context()),
			&waitlistspb.InviteRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.Error(t, err, must.Sprint("a neighboring tenant's signup was invited"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		// The control, and the proof nothing moved: the owner reads it still
		// waiting.
		read, err := mine.Surfaces.Waitlists.GetSignup(mine.Context(t.Context()),
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WAITING, read.GetResult().GetStatus())
	})

	t.Run("a conversion moves an invited signup", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		signup := signedUp(t, operator, operator, list.GetId(), freshContact())
		ctx := operator.Context(t.Context())

		_, err := operator.Surfaces.Waitlists.Invite(ctx,
			&waitlistspb.InviteRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)

		converted, err := operator.Surfaces.Waitlists.Convert(ctx,
			&waitlistspb.ConvertRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_CONVERTED, converted.GetResult().GetStatus())
	})

	t.Run("a conversion of somebody never invited is refused", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		signup := signedUp(t, operator, operator, list.GetId(), freshContact())

		_, err := operator.Surfaces.Waitlists.Convert(operator.Context(t.Context()),
			&waitlistspb.ConvertRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.Error(t, err, must.Sprint("a waiting signup skipped its invitation"))
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.StrContains(t, status.Convert(err).Message(), "status")
	})

	// The one write that touches a signup without moving it: the note changes
	// and status_changed_at does not, so a typo fixed here cannot reschedule
	// the reminder somebody's invitation started. The two instants compared
	// are one stored value read twice, so the comparison is exact on every
	// dialect.
	t.Run("rewriting a note does not move the signup", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		signup := signedUp(t, operator, operator, list.GetId(), freshContact())
		ctx := operator.Context(t.Context())

		invited, err := operator.Surfaces.Waitlists.Invite(ctx,
			&waitlistspb.InviteRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)
		must.NotNil(t, invited.GetResult().GetStatusChangedAt())

		noted, err := operator.Surfaces.Waitlists.UpdateSignupNotes(ctx, &waitlistspb.UpdateSignupNotesRequest{
			ListId: list.GetId(), SignupId: signup.GetId(), Notes: "asked for a later slot",
		})
		must.NoError(t, err)
		test.EqOp(t, "asked for a later slot", noted.GetResult().GetNotes())
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_INVITED, noted.GetResult().GetStatus())
		test.EqOp(t,
			invited.GetResult().GetStatusChangedAt().AsTime(),
			noted.GetResult().GetStatusChangedAt().AsTime(),
			test.Sprint("rewriting a note moved the moment the signup last changed status"))
	})

	// Archiving is not withdrawing: the row is hidden and nothing it holds
	// changes, so the address is still taken — and the form is not told that
	// either.
	t.Run("an archived signup is hidden and suppresses nothing", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		contact := freshContact()
		signup := signedUp(t, operator, operator, list.GetId(), contact)
		ctx := operator.Context(t.Context())

		_, err := operator.Surfaces.Waitlists.ArchiveSignup(ctx,
			&waitlistspb.ArchiveSignupRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)

		_, err = operator.Surfaces.Waitlists.GetSignup(ctx,
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.Error(t, err, must.Sprint("an archived signup is still readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		// The uniqueness covers archived rows, so a second join from the same
		// address collides — answered, like every join, with nothing.
		_, err = operator.Surfaces.Waitlists.Join(ctx,
			&waitlistspb.JoinRequest{ListId: list.GetId(), Contact: contact})
		must.NoError(t, err, must.Sprint("a join from an archived signup's address was refused rather than answered"))

		// What the collision cost is visible only to the console: no live row
		// for the address, so the join wrote nothing.
		test.Nil(t, byContact(t, operator, list.GetId(), contact),
			test.Sprint("a join from an archived signup's address wrote a second row"))
	})
}
