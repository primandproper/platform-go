package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestJoin(T *testing.T) {
	T.Parallel()

	// The form somebody filled in, reached by somebody who has not signed in.
	//
	// What comes back is empty, so what is asserted is what the row says. The
	// response's whole job is to say nothing — see the three subtests below it.
	T.Run("admits an anonymous caller and attributes the signup to nobody", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		res, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "Ada@example.com",
		})
		must.NoError(t, err)
		must.NotNil(t, res)

		stored := h.signupByContact(t, list.ID, "ada@example.com")
		must.NotNil(t, stored)

		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WAITING, stored.GetStatus())
		test.EqOp(t, list.ID, stored.GetListId())

		// The contact is stored as it was given, so a mail client renders the
		// capitalization somebody typed.
		test.EqOp(t, "Ada@example.com", stored.GetContact())

		// A signup that names nobody is the ordinary case for a pre-launch list.
		test.EqOp(t, "", stored.GetSubject().GetType())
		test.EqOp(t, "", stored.GetSubject().GetId())

		// The moment they joined is the database's, and the lifecycle has not
		// moved them yet.
		test.False(t, stored.GetCreatedAt().AsTime().IsZero())
		test.Nil(t, stored.GetStatusChangedAt())
	})

	// The subject is provenance and comes off the principal, never off the
	// request — waitlistspb.JoinRequest reserves the name.
	T.Run("attributes a signed-in caller's signup to them", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		_, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.NoError(t, err)

		stored := h.signupByContact(t, list.ID, "ada@example.com")
		must.NotNil(t, stored)

		test.EqOp(t, string(waitlists.SubjectUser), stored.GetSubject().GetType())
		test.EqOp(t, testUser, stored.GetSubject().GetId())
	})

	// The response carries nothing at all, which is the structural half of
	// "answers uniformly": there is no field for an outcome to differ in.
	T.Run("answers with an empty message", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		res, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.NoError(t, err)
		must.NotNil(t, res)

		encoded, err := proto.Marshal(res)
		must.NoError(t, err)
		test.SliceEmpty(t, encoded)
	})

	// The refusal that survives, and the reason it does: a closed list is a
	// fact about the list rather than about anybody's address, and a signup
	// page has to be able to say "we have stopped taking signups".
	T.Run("refuses a list that has stopped taking signups", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		closed := h.seedList(t, testScope, testNow.Add(-time.Hour))

		res, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
			ListId: closed.ID, Contact: "ada@example.com",
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrListClosed)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})

	// The membership oracle this surface refuses to be, closed from the write
	// side. Nothing establishes that the caller owns the address they typed, so
	// an address already on the list is answered rather than refused —
	// waitlists.ErrAlreadySignedUp stays inside the process.
	//
	// Two capitalizations of one address are still one person, which is what
	// makes this a suppressed refusal rather than a missed one: the second join
	// found the first row and wrote nothing.
	T.Run("says nothing about a contact already on the list", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		first := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "  ADA@Example.com ",
		})
		must.NoError(t, err)
		must.NotNil(t, res)

		// One row, and it is the one that was already there.
		signups := h.signupsOn(t, list.ID)
		must.SliceLen(t, 1, signups)
		test.EqOp(t, first.ID, signups[0].GetId())
		test.EqOp(t, "ada@example.com", signups[0].GetContact())
	})

	// The sharper half of the same decision. waitlists.ErrContactWithdrawn says
	// that the person at this address asked to be left alone, which is a
	// disclosure about somebody who asked for the opposite, so the form is told
	// what it is told about every other address: nothing.
	T.Run("says nothing about a contact that has withdrawn", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		_, err := h.server.Withdraw(h.anonCtx(t), &waitlistspb.WithdrawRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		res, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.NoError(t, err)
		must.NotNil(t, res)

		// The suppression is untouched by the answer: still one row, still
		// withdrawn, and still holding no address. An answer that read as
		// success and quietly re-subscribed somebody would be worse than the
		// oracle it replaced.
		signups := h.signupsOn(t, list.ID)
		must.SliceLen(t, 1, signups)
		test.EqOp(t, signup.ID, signups[0].GetId())
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN, signups[0].GetStatus())
		test.EqOp(t, "", signups[0].GetContact())
	})

	// The property the three subtests above are each half of, asserted as one
	// thing: a caller walking a list of addresses cannot tell them apart.
	//
	// It compares the encoded messages rather than the fields a converter
	// happens to set, which is the same reading messageCarries takes — a
	// response that grew a field would pass a field-by-field comparison written
	// before it.
	T.Run("answers a new, an existing and a withdrawn contact identically", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		existing := h.seedSignup(t, testScope, list.ID, "already@example.com")

		gone := h.seedSignup(t, testScope, list.ID, "gone@example.com")
		_, err := h.server.Withdraw(h.anonCtx(t), &waitlistspb.WithdrawRequest{
			ListId: list.ID, SignupId: gone.ID,
		})
		must.NoError(t, err)

		var answers [][]byte

		for _, contact := range []string{"new@example.com", existing.Contact, "gone@example.com"} {
			res, joinErr := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
				ListId: list.ID, Contact: contact,
			})
			must.NoError(t, joinErr, must.Sprintf("joining with %q", contact))

			encoded, marshalErr := proto.Marshal(res)
			must.NoError(t, marshalErr)

			answers = append(answers, encoded)
		}

		must.SliceLen(t, 3, answers)
		test.Eq(t, answers[0], answers[1])
		test.Eq(t, answers[0], answers[2])
	})

	// The tenant of an anonymous join is the connection's, so a list in another
	// tenant is not one an anonymous visitor can be joined to. The harness's
	// resolver places them in testScope; this list is not there.
	//
	// It is a refusal rather than a uniform answer because it is the same
	// absence a mistyped list id gets, and it says nothing about an address.
	T.Run("will not join an anonymous caller to another tenant's list", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, otherScope)

		res, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrListNotFound)
	})
}

func TestGetSignup(T *testing.T) {
	T.Parallel()

	T.Run("reads one signup on one of the caller's lists", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.GetSignup(h.ctx(t), &waitlistspb.GetSignupRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		test.EqOp(t, signup.ID, res.GetResult().GetId())
		test.EqOp(t, "ada@example.com", res.GetResult().GetContact())
	})

	// The list is half of what addresses a signup: a read that omitted it could
	// hand one list's row to a caller holding another list's id.
	T.Run("will not answer a signup named against the wrong list", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		other := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.GetSignup(h.ctx(t), &waitlistspb.GetSignupRequest{
			ListId: other.ID, SignupId: signup.ID,
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrSignupNotFound)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("answers another tenant's signup as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.GetSignup(h.otherCtx(t), &waitlistspb.GetSignupRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrSignupNotFound)
	})

	// The contact digest is absent structurally: the message has nowhere to put
	// one. schema_test.go asserts the reservation; this asserts the row.
	T.Run("carries no contact digest", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		must.NotEq(t, "", signup.ContactDigest)

		res, err := h.server.GetSignup(h.ctx(t), &waitlistspb.GetSignupRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		test.False(t, messageCarries(t, res, []byte(signup.ContactDigest)))
	})
}

func TestGetSignupByContact(T *testing.T) {
	T.Parallel()

	// The read behind "is this address on this list", behind a grant rather than
	// on the public half — which is the sharpest authorization decision on this
	// service. What it is here to check is that it requires a caller at all.
	T.Run("refuses an anonymous caller", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.GetSignupByContact(h.anonCtx(t), &waitlistspb.GetSignupByContactRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	T.Run("finds the signup by whichever capitalization the caller has", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.GetSignupByContact(h.ctx(t), &waitlistspb.GetSignupByContactRequest{
			ListId: list.ID, Contact: "ADA@EXAMPLE.COM",
		})
		must.NoError(t, err)

		test.EqOp(t, signup.ID, res.GetResult().GetId())
	})

	// A withdrawn signup is still a live row, which is what lets an unsubscribe
	// console tell somebody they are already off the list rather than that they
	// were never on it.
	T.Run("answers a withdrawn signup with its contact blank and its status saying why", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		_, err := h.server.Withdraw(h.anonCtx(t), &waitlistspb.WithdrawRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		res, err := h.server.GetSignupByContact(h.ctx(t), &waitlistspb.GetSignupByContactRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.NoError(t, err)

		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN, res.GetResult().GetStatus())
		test.EqOp(t, "", res.GetResult().GetContact())
	})
}

func TestListSignups(T *testing.T) {
	T.Parallel()

	T.Run("pages one list's signups", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		other := h.seedOpenList(t, testScope)

		h.seedSignup(t, testScope, list.ID, "ada@example.com")
		h.seedSignup(t, testScope, list.ID, "grace@example.com")
		h.seedSignup(t, testScope, other.ID, "alan@example.com")

		res, err := h.server.ListSignups(h.ctx(t), &waitlistspb.ListSignupsRequest{ListId: list.ID})
		must.NoError(t, err)

		test.SliceLen(t, 2, res.GetResults())
		must.NotNil(t, res.GetPagination())
	})

	T.Run("carries no other tenant's signups", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.ListSignups(h.otherCtx(t), &waitlistspb.ListSignupsRequest{ListId: list.ID})
		must.NoError(t, err)

		test.SliceEmpty(t, res.GetResults())
	})
}

func TestListSignupsForSubject(T *testing.T) {
	T.Parallel()

	T.Run("pages a subject's signups across the tenant's lists", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		first := h.seedOpenList(t, testScope)
		second := h.seedOpenList(t, testScope)

		for _, list := range []string{first.ID, second.ID} {
			_, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
				ListId: list, Contact: "ada@example.com",
			})
			must.NoError(t, err)
		}

		res, err := h.server.ListSignupsForSubject(h.ctx(t), &waitlistspb.ListSignupsForSubjectRequest{
			Subject: &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: testUser},
		})
		must.NoError(t, err)

		test.SliceLen(t, 2, res.GetResults())
	})

	// A withdrawal blanks the subject along with the contact, so the row that
	// remembers a suppression no longer says whose it was.
	T.Run("omits a withdrawn signup", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		_, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.NoError(t, err)

		joined := h.signupByContact(t, list.ID, "ada@example.com")
		must.NotNil(t, joined)

		_, err = h.server.Withdraw(h.ctx(t), &waitlistspb.WithdrawRequest{
			ListId: list.ID, SignupId: joined.GetId(),
		})
		must.NoError(t, err)

		res, err := h.server.ListSignupsForSubject(h.ctx(t), &waitlistspb.ListSignupsForSubjectRequest{
			Subject: &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: testUser},
		})
		must.NoError(t, err)

		test.SliceEmpty(t, res.GetResults())
	})

	// An export sets include_archived, because an archived signup still holds
	// the address it was made with.
	T.Run("reaches archived signups when the filter asks for them", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		_, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.NoError(t, err)

		joined := h.signupByContact(t, list.ID, "ada@example.com")
		must.NotNil(t, joined)

		_, err = h.server.ArchiveSignup(h.ctx(t), &waitlistspb.ArchiveSignupRequest{
			ListId: list.ID, SignupId: joined.GetId(),
		})
		must.NoError(t, err)

		subject := &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: testUser}

		live, err := h.server.ListSignupsForSubject(h.ctx(t), &waitlistspb.ListSignupsForSubjectRequest{
			Subject: subject,
		})
		must.NoError(t, err)
		test.SliceEmpty(t, live.GetResults())

		all, err := h.server.ListSignupsForSubject(h.ctx(t), &waitlistspb.ListSignupsForSubjectRequest{
			Subject: subject,
			Filter:  includeArchived(),
		})
		must.NoError(t, err)
		test.SliceLen(t, 1, all.GetResults())
	})
}

func TestUpdateSignupNotes(T *testing.T) {
	T.Parallel()

	// The one write that touches a signup without moving it: the note changes
	// and status_changed_at does not, so a typo fixed here cannot reschedule the
	// reminder somebody's invitation started.
	T.Run("rewrites the note without moving the signup", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		h.invite(t, testScope, list.ID, signup.ID)

		invited, err := h.server.GetSignup(h.ctx(t), &waitlistspb.GetSignupRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)
		must.NotNil(t, invited.GetResult().GetStatusChangedAt())

		res, err := h.server.UpdateSignupNotes(h.ctx(t), &waitlistspb.UpdateSignupNotesRequest{
			ListId: list.ID, SignupId: signup.ID, Notes: "asked for a later slot",
		})
		must.NoError(t, err)

		test.EqOp(t, "asked for a later slot", res.GetResult().GetNotes())
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_INVITED, res.GetResult().GetStatus())
		test.EqOp(t,
			invited.GetResult().GetStatusChangedAt().AsTime(),
			res.GetResult().GetStatusChangedAt().AsTime())
	})
}

func TestInvite(T *testing.T) {
	T.Parallel()

	// The read is inside the transaction, so the response carries the moment the
	// write stamped — which is the field a reminder is scheduled off.
	T.Run("moves a waiting signup and answers with the moment it moved", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.Invite(h.ctx(t), &waitlistspb.InviteRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_INVITED, res.GetResult().GetStatus())
		must.NotNil(t, res.GetResult().GetStatusChangedAt())
		test.False(t, res.GetResult().GetStatusChangedAt().AsTime().IsZero())
	})

	// The guard is the affected-row count of one update rather than a decision
	// made on a read, which is what makes a transition happen exactly once.
	T.Run("refuses a second invitation", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		_, err := h.server.Invite(h.ctx(t), &waitlistspb.InviteRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		res, err := h.server.Invite(h.ctx(t), &waitlistspb.InviteRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrWrongStatus)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})

	T.Run("will not reach another tenant's signup", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.Invite(h.otherCtx(t), &waitlistspb.InviteRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrSignupNotFound)
	})
}

func TestConvert(T *testing.T) {
	T.Parallel()

	T.Run("moves an invited signup", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		h.invite(t, testScope, list.ID, signup.ID)

		res, err := h.server.Convert(h.ctx(t), &waitlistspb.ConvertRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_CONVERTED, res.GetResult().GetStatus())
	})

	T.Run("refuses somebody who was never invited", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.Convert(h.ctx(t), &waitlistspb.ConvertRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrWrongStatus)
	})
}

func TestWithdraw(T *testing.T) {
	T.Parallel()

	// The unsubscribe link: reachable without a grant, and it erases what the
	// row said about somebody while keeping the suppression.
	T.Run("erases the contact and keeps the suppression", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.Withdraw(h.anonCtx(t), &waitlistspb.WithdrawRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)
		must.NotNil(t, res)

		read, err := h.server.GetSignup(h.ctx(t), &waitlistspb.GetSignupRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN, read.GetResult().GetStatus())
		test.EqOp(t, "", read.GetResult().GetContact())
		test.EqOp(t, "", read.GetResult().GetNotes())
		test.EqOp(t, "", read.GetResult().GetSubject().GetId())
	})

	// The response carries no signup: what is left of the row is a status and a
	// digest, and the caller who has just asked to be forgotten is not the
	// caller to hand it to.
	T.Run("answers with nothing at all", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.Withdraw(h.anonCtx(t), &waitlistspb.WithdrawRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		test.False(t, messageCarries(t, res, []byte(signup.ContactDigest)))
		test.False(t, messageCarries(t, res, []byte(signup.ID)))
	})

	// A replay reports rather than restamping, because the moment somebody asked
	// to come off a list is a fact about them.
	T.Run("refuses a second withdrawal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		_, err := h.server.Withdraw(h.anonCtx(t), &waitlistspb.WithdrawRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		res, err := h.server.Withdraw(h.anonCtx(t), &waitlistspb.WithdrawRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrAlreadyWithdrawn)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})
}

func TestWithdrawSignupsForSubject(T *testing.T) {
	T.Parallel()

	// The erasure path: a withdrawal rather than a delete, on every list in the
	// tenant, archived signups included.
	T.Run("withdraws every signup the subject holds and reports how many", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		first := h.seedOpenList(t, testScope)
		second := h.seedOpenList(t, testScope)

		for _, list := range []string{first.ID, second.ID} {
			_, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
				ListId: list, Contact: "ada@example.com",
			})
			must.NoError(t, err)
		}

		res, err := h.server.WithdrawSignupsForSubject(h.ctx(t),
			&waitlistspb.WithdrawSignupsForSubjectRequest{
				Subject: &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: testUser},
			})
		must.NoError(t, err)

		test.EqOp(t, int64(2), res.GetWithdrawn())

		// The suppression outlives the erasure on every list the person was on,
		// and the form is not told so — it is told what it is told about every
		// other address. What the suppression did is visible to the half of the
		// surface that may see it: the row is still the withdrawn one.
		rejoined, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
			ListId: first.ID, Contact: "ada@example.com",
		})
		must.NoError(t, err)
		must.NotNil(t, rejoined)

		signups := h.signupsOn(t, first.ID)
		must.SliceLen(t, 1, signups)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN, signups[0].GetStatus())
	})

	// Zero is not an error: a person who never joined a list is a person with
	// nothing here to erase.
	T.Run("reports zero for a subject with nothing here", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.WithdrawSignupsForSubject(h.ctx(t),
			&waitlistspb.WithdrawSignupsForSubjectRequest{
				Subject: &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: "nobody"},
			})
		must.NoError(t, err)

		test.EqOp(t, int64(0), res.GetWithdrawn())
	})

	// A subject bound to nobody would name every signup nobody claimed, so the
	// handler refuses it before the store sees it.
	T.Run("refuses a request that named no subject", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.WithdrawSignupsForSubject(h.ctx(t),
			&waitlistspb.WithdrawSignupsForSubjectRequest{})
		test.Nil(t, res)
		must.Error(t, err)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestArchiveSignup(T *testing.T) {
	T.Parallel()

	// Archiving is not withdrawing: the row is hidden and nothing about what it
	// holds changes, so the address is still taken.
	T.Run("hides the row and suppresses nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		_, err := h.server.ArchiveSignup(h.ctx(t), &waitlistspb.ArchiveSignupRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		read, err := h.server.GetSignup(h.ctx(t), &waitlistspb.GetSignupRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		test.Nil(t, read)
		must.Error(t, err)
		test.ErrorIs(t, err, waitlists.ErrSignupNotFound)

		// The uniqueness covers archived rows, so the next attempt collides —
		// and the form is not told that either. What the collision cost is
		// countable only from the administrative half: no second row.
		rejoined, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.NoError(t, err)
		must.NotNil(t, rejoined)

		all := h.signupsOn(t, list.ID)
		must.SliceLen(t, 1, all)
		test.EqOp(t, signup.ID, all[0].GetId())
	})
}

// includeArchived is the filter a privacy export sets.
func includeArchived() *filteringpb.QueryFilter {
	include := true

	return &filteringpb.QueryFilter{IncludeArchived: &include}
}
