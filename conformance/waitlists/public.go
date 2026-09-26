package waitlists

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	domain "github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// signupPage is the public half: the catalog a visitor browses, the form they
// submit and the unsubscribe link in the mail that follows.
func signupPage(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a visitor sees the open catalog of the tenant they land in, and only that", func(t *testing.T) {
		t.Parallel()

		anonymous, operator := visitor(t, s)
		taking := openList(t, operator, open())
		stopped := openList(t, operator, closed())

		// A list in a tenant of its own, which a visitor landing anywhere but
		// there must not be offered.
		elsewhere := openList(t, s.Subject(t), open())

		ids := openListIDs(t, t.Context(), anonymous)
		test.SliceContains(t, ids, taking.GetId(),
			test.Sprint("an open list was missing from the catalog of the tenant a visitor lands in; the absences below prove nothing"))
		test.SliceNotContains(t, ids, stopped.GetId(), test.Sprint("a closed list reached a visitor's catalog"))
		test.SliceNotContains(t, ids, elsewhere.GetId(), test.Sprint("another tenant's list reached a visitor's catalog"))
	})

	// The form somebody filled in, reached by somebody who has not signed in.
	// What comes back is empty, so what is asserted is what the row says.
	t.Run("a visitor's signup is kept as typed and attributed to nobody", func(t *testing.T) {
		t.Parallel()

		anonymous, operator := visitor(t, s)
		list := openList(t, operator, open())
		typed := "Conf." + freshContact()

		_, err := anonymous.Join(t.Context(), &waitlistspb.JoinRequest{ListId: list.GetId(), Contact: typed})
		must.NoError(t, err, must.Sprint("the signup form refused a visitor"))

		stored := byContact(t, operator, list.GetId(), strings.ToLower(typed))
		must.NotNil(t, stored, must.Sprint("a visitor's join left no row the console can find"))

		test.EqOp(t, list.GetId(), stored.GetListId())
		test.EqOp(t, typed, stored.GetContact(),
			test.Sprint("the address was stored as something other than what was typed"))

		// A signup that names nobody is the ordinary case for a pre-launch
		// list, and a subject is provenance: it comes off a principal, and a
		// visitor has none.
		test.EqOp(t, "", stored.GetSubject().GetType())
		test.EqOp(t, "", stored.GetSubject().GetId())

		test.False(t, stored.GetCreatedAt().AsTime().IsZero())
		test.Nil(t, stored.GetStatusChangedAt())

		if !confirms(s) {
			test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WAITING, stored.GetStatus())

			return
		}

		// A deployment that confirms holds it until somebody at the address
		// says so, and the visitor who typed it is the one who follows the
		// link — still without signing in.
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_PENDING, stored.GetStatus(),
			test.Sprint("a deployment that confirms counted a signup nobody had confirmed"))

		confirm(t, s, anonymous, t.Context(), operator.Scope, list.GetId(), typed)

		confirmed := byContact(t, operator, list.GetId(), strings.ToLower(typed))
		must.NotNil(t, confirmed)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WAITING, confirmed.GetStatus())
		test.NotNil(t, confirmed.GetStatusChangedAt(), test.Sprint("the confirmation stamped no moment"))
	})

	// The tenant of an anonymous join is the connection's, so a list in another
	// tenant is not one a visitor can be joined to. It is the same absence a
	// mistyped list id gets, and it says nothing about an address.
	t.Run("a visitor cannot be joined to a list outside the tenant they land in", func(t *testing.T) {
		t.Parallel()

		anonymous, operator := visitor(t, s)
		home := openList(t, operator, open())

		owner := s.Subject(t)
		elsewhere := openList(t, owner, open())

		// The positive control: the same visitor joins a list where they land.
		_, err := anonymous.Join(t.Context(), &waitlistspb.JoinRequest{ListId: home.GetId(), Contact: freshContact()})
		must.NoError(t, err, must.Sprint("a visitor could not join a list where they land; the refusal below proves nothing"))

		contact := freshContact()
		_, err = anonymous.Join(t.Context(), &waitlistspb.JoinRequest{ListId: elsewhere.GetId(), Contact: contact})
		must.Error(t, err, must.Sprint("a visitor was joined to another tenant's list"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.Nil(t, byContact(t, owner, elsewhere.GetId(), contact),
			test.Sprint("a refused join still wrote a row"))
	})

	// The subject is provenance and comes off the principal, never off the
	// request — JoinRequest reserves the name.
	t.Run("a signed-in caller's signup is attributed to them", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)
		list := openList(t, caller, open())

		stored := signedUp(t, s, caller, caller, list.GetId(), freshContact())
		test.EqOp(t, string(domain.SubjectUser), stored.GetSubject().GetType())
		test.EqOp(t, caller.UserID, stored.GetSubject().GetId())
	})

	t.Run("a signed-in caller cannot join another tenant's list", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		list := openList(t, mine, open())

		// The positive control: the owner's own join lands.
		signedUp(t, s, mine, mine, list.GetId(), freshContact())

		contact := freshContact()
		_, err := theirs.Surfaces.Waitlists.Join(theirs.Context(t.Context()),
			&waitlistspb.JoinRequest{ListId: list.GetId(), Contact: contact})
		must.Error(t, err, must.Sprint("a caller was joined to a neighboring tenant's list"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.Nil(t, byContact(t, mine, list.GetId(), contact), test.Sprint("a refused join still wrote a row"))
	})

	// The refusal that survives the uniform answer, and the reason it does: a
	// closed list is a fact about the list rather than about anybody's address,
	// and a signup page has to be able to say "we have stopped taking signups"
	// in those words.
	t.Run("a list that has stopped taking signups refuses them, in words a person can read", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, closed())
		contact := freshContact()

		_, err := operator.Surfaces.Waitlists.Join(operator.Context(t.Context()),
			&waitlistspb.JoinRequest{ListId: list.GetId(), Contact: contact})
		must.Error(t, err, must.Sprint("a closed list took a signup"))
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.StrContains(t, status.Convert(err).Message(), "closed")

		test.Nil(t, byContact(t, operator, list.GetId(), contact), test.Sprint("a refused join still wrote a row"))
	})

	// The membership oracle this surface refuses to be, closed from the write
	// side. Nothing establishes that the caller owns the address they typed,
	// so an address already on the list is answered rather than refused. Two
	// capitalizations of one address are one person, which is what makes this a
	// suppressed refusal rather than a missed one.
	t.Run("a join says nothing about an address already on the list, and writes nothing", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		contact := freshContact()
		first := signedUp(t, s, operator, operator, list.GetId(), contact)

		_, err := operator.Surfaces.Waitlists.Join(operator.Context(t.Context()), &waitlistspb.JoinRequest{
			ListId: list.GetId(), Contact: "  " + strings.ToUpper(contact) + " ",
		})
		must.NoError(t, err, must.Sprint("a join from an address already on the list was refused, which tells the form it is there"))

		// The row for the address is still the first one, as it was typed, and
		// the list holds no row this assertion did not make.
		found := byContact(t, operator, list.GetId(), contact)
		must.NotNil(t, found)
		test.EqOp(t, first.GetId(), found.GetId())
		test.EqOp(t, contact, found.GetContact())

		for _, signup := range signupsOn(t, operator, list.GetId()) {
			test.EqOp(t, first.GetId(), signup.GetId(),
				test.Sprint("a join from an address already on the list wrote a second row"))
		}
	})

	// The sharper half of the same decision. That the person at an address
	// asked to be left alone is a disclosure about somebody who asked for the
	// opposite, so the form is told what it is told about every other address:
	// nothing. The suppression is untouched by the answer — an answer that read
	// as success and quietly re-subscribed somebody would be worse than the
	// oracle it replaced.
	t.Run("a join says nothing about an address that withdrew, and re-subscribes nobody", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		needsUser(t, operator)
		list := openList(t, operator, open())
		contact := freshContact()
		signup := signedUp(t, s, operator, operator, list.GetId(), contact)

		eraseSubject(t, operator, operator)

		_, err := operator.Surfaces.Waitlists.Join(operator.Context(t.Context()),
			&waitlistspb.JoinRequest{ListId: list.GetId(), Contact: contact})
		must.NoError(t, err, must.Sprint("a join from a withdrawn address was refused, which tells the form somebody withdrew"))

		read, err := operator.Surfaces.Waitlists.GetSignup(operator.Context(t.Context()),
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN, read.GetResult().GetStatus(),
			test.Sprint("a join re-subscribed an address that had withdrawn"))
		test.EqOp(t, "", read.GetResult().GetContact())

		for _, other := range signupsOn(t, operator, list.GetId()) {
			test.EqOp(t, signup.GetId(), other.GetId(),
				test.Sprint("a join from a withdrawn address wrote a second row"))
		}
	})

	// The property the two assertions above are each half of, asserted as one
	// thing: a caller walking a list of addresses cannot tell them apart. The
	// encoded messages are compared rather than the fields a converter sets,
	// so a response that grew a field fails here without this being edited.
	t.Run("a join answers a new, an existing and a withdrawn address identically, and with nothing", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())

		existing := freshContact()
		join(t, s, operator, list.GetId(), existing)

		// The withdrawn address is a colleague's, so the erasure that
		// withdraws it leaves the operator's own signup where it is.
		leaver := colleague(t, s, operator)
		needsUser(t, leaver)
		gone := freshContact()
		join(t, s, leaver, list.GetId(), gone)
		eraseSubject(t, operator, leaver)

		var answers [][]byte

		for _, contact := range []string{freshContact(), existing, gone} {
			answer, err := operator.Surfaces.Waitlists.Join(operator.Context(t.Context()),
				&waitlistspb.JoinRequest{ListId: list.GetId(), Contact: contact})
			must.NoError(t, err, must.Sprintf("joining with %q", contact))

			encoded, err := proto.Marshal(answer)
			must.NoError(t, err)

			answers = append(answers, encoded)
		}

		// Empty, which is the structural half of "uniformly": there is no field
		// for an outcome to differ in.
		test.SliceEmpty(t, answers[0], test.Sprint("a join answered with something, which is somewhere for an outcome to differ"))
		test.Eq(t, answers[0], answers[1], test.Sprint("a join told an existing address apart from a new one"))
		test.Eq(t, answers[0], answers[2], test.Sprint("a join told a withdrawn address apart from a new one"))
	})

	// A withdrawal names a signup rather than a person, and the identifier is
	// not a credential. Whether this caller may take this signup off the list
	// is the deployment's SignupAuthorizer's to say, and a request carrying
	// nothing but the two identifiers is one no authorizer can tie to the
	// person who signed up.
	//
	// What is asserted is the shape of the refusal. NotFound and not
	// PermissionDenied, and read identically for an identifier nobody minted:
	// two answers a caller walking identifiers could tell apart are an oracle
	// over which signups exist. Only a real connection shows this, because the
	// handler's code is a default the encoding interceptor may overrule.
	t.Run("a withdrawal nobody can be tied to reads as an identifier nobody minted, and moves nothing", func(t *testing.T) {
		t.Parallel()

		anonymous, operator := visitor(t, s)
		list := openList(t, operator, open())
		contact := freshContact()
		signup := signedUp(t, s, operator, operator, list.GetId(), contact)
		madeUp := identifiers.New()

		_, refused := anonymous.Withdraw(t.Context(),
			&waitlistspb.WithdrawRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.Error(t, refused, must.Sprint("a withdrawal carrying nothing but identifiers was honored"))
		test.EqOp(t, codes.NotFound, status.Code(refused),
			test.Sprint("a refused withdrawal read as something other than an absence"))

		_, absent := anonymous.Withdraw(t.Context(),
			&waitlistspb.WithdrawRequest{ListId: list.GetId(), SignupId: madeUp})
		must.Error(t, absent)
		test.EqOp(t, status.Code(absent), status.Code(refused))

		// The messages may echo the identifier each request named, which tells
		// the caller nothing it did not send; with that removed they must be
		// the same words.
		test.EqOp(t,
			strings.ReplaceAll(status.Convert(absent).Message(), madeUp, ""),
			strings.ReplaceAll(status.Convert(refused).Message(), signup.GetId(), ""),
			test.Sprint("a refused withdrawal and an absent signup were answered in different words"))

		// Asked before anything was written, so the row is as it was.
		read, err := operator.Surfaces.Waitlists.GetSignup(operator.Context(t.Context()),
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: signup.GetId()})
		must.NoError(t, err)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WAITING, read.GetResult().GetStatus())
		test.EqOp(t, contact, read.GetResult().GetContact())
	})

	// The loop itself, on a deployment that runs it. What is asserted is the
	// part a consumer's page depends on: that a join is not a subscription
	// until its link is followed, and that the link in the same mail takes the
	// address off the list without anybody signing in.
	t.Run("an unconfirmed signup cannot be invited, and its link confirms it once", func(t *testing.T) {
		t.Parallel()

		needsConfirmation(t, s)

		anonymous, operator := visitor(t, s)
		list := openList(t, operator, open())
		contact := freshContact()

		_, err := anonymous.Join(t.Context(), &waitlistspb.JoinRequest{ListId: list.GetId(), Contact: contact})
		must.NoError(t, err)

		pending := byContact(t, operator, list.GetId(), contact)
		must.NotNil(t, pending)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_PENDING, pending.GetStatus())

		// Nobody said yes, so nobody may be let in.
		_, err = operator.Surfaces.Waitlists.Invite(operator.Context(t.Context()),
			&waitlistspb.InviteRequest{ListId: list.GetId(), SignupId: pending.GetId()})
		must.Error(t, err, must.Sprint("a signup nobody confirmed was invited"))
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))

		mailed := linksFor(t, t.Context(), s, operator.Scope, list.GetId(), contact)

		_, err = anonymous.Confirm(t.Context(), &waitlistspb.ConfirmRequest{Token: mailed.Confirm})
		must.NoError(t, err, must.Sprint("a visitor could not follow their confirmation link"))

		// The same link again — the person's click after the scanner's — is
		// refused, and reads as a token nobody minted.
		_, spent := anonymous.Confirm(t.Context(), &waitlistspb.ConfirmRequest{Token: mailed.Confirm})
		must.Error(t, spent, must.Sprint("a confirmation link worked twice"))
		test.EqOp(t, codes.NotFound, status.Code(spent))

		_, madeUp := anonymous.Confirm(t.Context(), &waitlistspb.ConfirmRequest{Token: identifiers.New()})
		must.Error(t, madeUp)
		test.EqOp(t, status.Code(spent), status.Code(madeUp))
		test.EqOp(t, status.Convert(spent).Message(), status.Convert(madeUp).Message(),
			test.Sprint("a spent link and a made-up one were answered in different words"))

		_, err = operator.Surfaces.Waitlists.Invite(operator.Context(t.Context()),
			&waitlistspb.InviteRequest{ListId: list.GetId(), SignupId: pending.GetId()})
		must.NoError(t, err, must.Sprint("a confirmed signup could not be invited"))
	})

	t.Run("the unsubscribe link in the confirmation takes the address off the list with nobody signed in", func(t *testing.T) {
		t.Parallel()

		needsConfirmation(t, s)

		anonymous, operator := visitor(t, s)
		list := openList(t, operator, open())
		contact := freshContact()

		_, err := anonymous.Join(t.Context(), &waitlistspb.JoinRequest{ListId: list.GetId(), Contact: contact})
		must.NoError(t, err)

		pending := byContact(t, operator, list.GetId(), contact)
		must.NotNil(t, pending)

		mailed := linksFor(t, t.Context(), s, operator.Scope, list.GetId(), contact)

		// A confirmation link is not an unsubscribe link, and presenting one at
		// the wrong door must not spend it.
		_, wrongDoor := anonymous.Unsubscribe(t.Context(), &waitlistspb.UnsubscribeRequest{Token: mailed.Confirm})
		must.Error(t, wrongDoor, must.Sprint("a confirmation link unsubscribed somebody"))
		test.EqOp(t, codes.NotFound, status.Code(wrongDoor))

		// "This was not me": withdrawn while still pending, and suppressed.
		_, err = anonymous.Unsubscribe(t.Context(), &waitlistspb.UnsubscribeRequest{Token: mailed.Unsubscribe})
		must.NoError(t, err, must.Sprint("a visitor could not follow their unsubscribe link"))

		read, err := operator.Surfaces.Waitlists.GetSignup(operator.Context(t.Context()),
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: pending.GetId()})
		must.NoError(t, err)
		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN, read.GetResult().GetStatus())
		test.EqOp(t, "", read.GetResult().GetContact())

		// And the confirmation link that was left unspent above now has
		// nowhere to take anybody.
		_, err = anonymous.Confirm(t.Context(), &waitlistspb.ConfirmRequest{Token: mailed.Confirm})
		must.Error(t, err, must.Sprint("a withdrawn signup was confirmed"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}
