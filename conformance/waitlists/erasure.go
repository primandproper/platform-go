package waitlists

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	domain "github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// erasure is a person's signups as a person: the read an export makes and the
// withdrawal an erasure makes, both keyed by who signed up rather than by list.
func erasure(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a person's signups are found across the tenant's lists", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)
		operator := s.OperatorIn(t, surface, caller.ScopeFor(surface),
			waitlistspb.WaitlistsService_CreateList_FullMethodName,
			waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName,
		)
		first := openList(t, operator, open())
		second := openList(t, operator, open())

		onFirst := signedUp(t, caller, operator, first.GetId(), freshContact())
		onSecond := signedUp(t, caller, operator, second.GetId(), freshContact())

		// A person reading their own signups is an ordinary caller's read, and
		// a promise: the export a person asks for is theirs to ask for.
		page, err := caller.Surfaces.Waitlists.ListSignupsForSubject(caller.Context(t.Context()),
			&waitlistspb.ListSignupsForSubjectRequest{Subject: userSubject(caller)})
		must.NoError(t, err)
		test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		ids := signupIDs(page.GetResults())
		test.SliceContains(t, ids, onFirst.GetId())
		test.SliceContains(t, ids, onSecond.GetId())
	})

	// Whose signups these are is a per-request question no grant on the method
	// can answer, so it is the deployment's SignupAuthorizer's — and a refusal
	// reads as an absence, because "permission denied" would confirm the
	// person named is one this tenant knows about.
	t.Run("a person's signups are not readable by a colleague who names them, and the refusal is an absence", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t)
		needsUser(t, owner)
		operator := s.OperatorIn(t, surface, owner.ScopeFor(surface),
			waitlistspb.WaitlistsService_CreateList_FullMethodName,
			waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName,
		)
		list := openList(t, operator, open())

		person := colleague(t, s, owner)
		needsUser(t, person)
		theirs := signedUp(t, person, operator, list.GetId(), freshContact())

		// The positive control: the person whose signup it is reads it, so the
		// refusal below is about who asked and not about a read that reaches
		// nothing.
		own, err := person.Surfaces.Waitlists.ListSignupsForSubject(person.Context(t.Context()),
			&waitlistspb.ListSignupsForSubjectRequest{Subject: userSubject(person)})
		must.NoError(t, err, must.Sprint("a person cannot read their own signups; the refusal below proves nothing"))
		must.SliceContains(t, signupIDs(own.GetResults()), theirs.GetId())

		_, err = owner.Surfaces.Waitlists.ListSignupsForSubject(owner.Context(t.Context()),
			&waitlistspb.ListSignupsForSubjectRequest{Subject: userSubject(person)})
		must.Error(t, err, must.Sprint("a colleague read somebody else's signups by naming them"))
		test.EqOp(t, codes.NotFound, status.Code(err),
			test.Sprint("a refused subject read said something other than that there was nothing there"))
	})

	// The erasure path is a withdrawal rather than a delete, on every list in
	// the tenant: a delete would free the address for the next form submission
	// to re-subscribe somebody erased at their own request.
	t.Run("an erasure withdraws every signup a person holds and keeps the suppression", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)
		operator := s.OperatorIn(t, surface, caller.ScopeFor(surface),
			waitlistspb.WaitlistsService_CreateList_FullMethodName,
			waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName,
			waitlistspb.WaitlistsService_GetSignup_FullMethodName,
			waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName,
		)
		first := openList(t, operator, open())
		second := openList(t, operator, open())

		contact := freshContact()
		onFirst := signedUp(t, caller, operator, first.GetId(), contact)
		onSecond := signedUp(t, caller, operator, second.GetId(), freshContact())

		// At least the two made here, rather than exactly two: the count is of
		// rows, and a deployment is entitled to have signed this person up to
		// something of its own on the way in.
		test.GreaterEq(t, int64(2), eraseSubject(t, operator, caller),
			test.Sprint("an erasure reported fewer signups than this person held"))

		for _, erased := range []*waitlistspb.Signup{onFirst, onSecond} {
			read, err := operator.Surfaces.Waitlists.GetSignup(operator.Context(t.Context()),
				&waitlistspb.GetSignupRequest{ListId: erased.GetListId(), SignupId: erased.GetId()})
			must.NoError(t, err, must.Sprint("an erased signup is gone rather than withdrawn, which frees its address"))
			test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN, read.GetResult().GetStatus())
			test.EqOp(t, "", read.GetResult().GetContact(), test.Sprint("an erased signup still holds its address"))
			test.EqOp(t, "", read.GetResult().GetNotes())
			test.EqOp(t, "", read.GetResult().GetSubject().GetId(), test.Sprint("an erased signup still names its person"))
		}

		// The suppression outlives the erasure, and the form is not told so.
		_, err := caller.Surfaces.Waitlists.Join(caller.Context(t.Context()),
			&waitlistspb.JoinRequest{ListId: first.GetId(), Contact: contact})
		must.NoError(t, err)

		// The address finds the withdrawn row and no other: the join neither
		// revived it nor wrote a fresh one beside it.
		found := byContact(t, operator, first.GetId(), contact)
		must.NotNil(t, found, must.Sprint("an erased address no longer finds the row that suppresses it"))
		test.EqOp(t, onFirst.GetId(), found.GetId(),
			test.Sprint("a join after an erasure wrote a fresh signup for the person erased"))
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN, found.GetStatus(),
			test.Sprint("a join after an erasure re-subscribed the person erased"))
	})

	// A withdrawal blanks the subject along with the address, so the row that
	// remembers a suppression no longer says whose it was.
	t.Run("an erased signup leaves the person's listing", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)
		operator := s.OperatorIn(t, surface, caller.ScopeFor(surface),
			waitlistspb.WaitlistsService_CreateList_FullMethodName,
			waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName,
			waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName,
		)
		list := openList(t, operator, open())
		signup := signedUp(t, caller, operator, list.GetId(), freshContact())

		// The positive control: before the erasure the listing carries it.
		before, err := caller.Surfaces.Waitlists.ListSignupsForSubject(caller.Context(t.Context()),
			&waitlistspb.ListSignupsForSubjectRequest{Subject: userSubject(caller)})
		must.NoError(t, err)
		must.SliceContains(t, signupIDs(before.GetResults()), signup.GetId())

		eraseSubject(t, operator, caller)

		after, err := caller.Surfaces.Waitlists.ListSignupsForSubject(caller.Context(t.Context()),
			&waitlistspb.ListSignupsForSubjectRequest{Subject: userSubject(caller)})
		must.NoError(t, err)
		test.SliceNotContains(t, signupIDs(after.GetResults()), signup.GetId(),
			test.Sprint("an erased signup still names the person it was erased for"))
	})

	// Zero is not an error: a person who never joined a list is a person with
	// nothing here to erase. The subject is an identifier minted for this
	// assertion, so nothing anywhere can have signed it up.
	t.Run("an erasure of a person with nothing here reports zero", func(t *testing.T) {
		t.Parallel()

		operator := s.Operator(t, waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName)

		erased, err := operator.Surfaces.Waitlists.WithdrawSignupsForSubject(operator.Context(t.Context()),
			&waitlistspb.WithdrawSignupsForSubjectRequest{
				Subject: &waitlistspb.SignupSubject{Type: string(domain.SubjectUser), Id: identifiers.New()},
			})
		must.NoError(t, err)
		test.EqOp(t, int64(0), erased.GetWithdrawn())
	})

	// A subject bound to nobody would name every signup nobody claimed — every
	// visitor's — so it is refused before the store sees it.
	t.Run("an erasure that names no person is refused as a bad request", func(t *testing.T) {
		t.Parallel()

		operator := s.Operator(t, waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName)

		_, err := operator.Surfaces.Waitlists.WithdrawSignupsForSubject(operator.Context(t.Context()),
			&waitlistspb.WithdrawSignupsForSubjectRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("an erasure reaches the caller's tenant only", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		needsUser(t, theirs)
		making := []string{
			waitlistspb.WaitlistsService_CreateList_FullMethodName,
			waitlistspb.WaitlistsService_GetSignup_FullMethodName,
			waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName,
			waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName,
		}
		myOperator, theirOperator := s.OperatorIn(t, surface, mine.ScopeFor(surface), making...), s.OperatorIn(t, surface, theirs.ScopeFor(surface), making...)
		list := openList(t, theirOperator, open())
		signup := signedUp(t, theirs, theirOperator, list.GetId(), freshContact())

		// Naming a person from another tenant erases nothing there.
		_, err := myOperator.Surfaces.Waitlists.WithdrawSignupsForSubject(myOperator.Context(t.Context()),
			&waitlistspb.WithdrawSignupsForSubjectRequest{Subject: userSubject(theirs)})
		must.NoError(t, err)

		read, err := theirOperator.Surfaces.Waitlists.GetSignup(theirOperator.Context(t.Context()),
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WAITING, read.GetResult().GetStatus(),
			test.Sprint("an erasure in one tenant withdrew a signup in another"))

		// The positive control, after the fact: the same erasure from the
		// person's own tenant does withdraw it, so the survival above is about
		// the tenant and not an erasure that reaches nothing.
		eraseSubject(t, theirOperator, theirs)

		read, err = theirOperator.Surfaces.Waitlists.GetSignup(theirOperator.Context(t.Context()),
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN, read.GetResult().GetStatus())
	})
}
