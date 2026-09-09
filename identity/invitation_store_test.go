package identity

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func runInvitationStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	// newInvitedStore builds a store on a fixed clock with an owner, an account,
	// and a pending invitation already in place — the setup every case here
	// needs before it can start.
	newInvitedStore := func(t *testing.T) (*SQLStore, *fixedClock, *User, *Account, *Invitation) {
		t.Helper()

		clk := newFixedClock(baseTime)

		store, err := NewSQLStore(env.client, WithTablePrefix(env.migrate(t)), WithClock(clk))
		must.NoError(t, err)

		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		invitation := newInvitation(owner, account.ID, "brian@example.com", "tok-secret", baseTime.Add(72*time.Hour))
		must.NoError(t, env.createInvitation(t, store, invitation.Scope, invitation))

		return store, clk, owner, account, invitation
	}

	t.Run("creates and reads back", func(t *testing.T) {
		t.Parallel()

		store, _, owner, account, invitation := newInvitedStore(t)

		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, invitation.ID)
		must.NoError(t, err)
		test.EqOp(t, account.ID, read.BelongsToAccount)
		test.EqOp(t, owner.ID, read.FromUser)
		test.EqOp(t, InvitationPending, read.Status)
		test.Eq(t, []string{"account_member"}, read.Roles)
		test.Nil(t, read.ToUser)
		test.EqOp(t, time.UTC, read.ExpiresAt.Location())

		// The sender's message is stored as sent, and nobody has answered yet,
		// so there is no answer to explain.
		test.EqOp(t, senderNote, read.Note)
		test.EqOp(t, "", read.StatusNote)
	})

	t.Run("refuses an invitation created carrying a status note", func(t *testing.T) {
		t.Parallel()

		store, _, owner, account, _ := newInvitedStore(t)

		// The same objection as an invitation created already answered: nobody
		// has replied to a freshly sent invitation, so there is no answer to
		// explain. status_note is written by the two status writes and by
		// nothing else.
		answered := newInvitation(owner, account.ID, "carol@example.com", "tok-6", baseTime.Add(time.Hour))
		answered.StatusNote = "declined before it was sent"

		must.ErrorIs(t,
			env.createInvitation(t, store, answered.Scope, answered),
			platformerrors.ErrUnrecognizedInputValue,
		)
	})

	t.Run("refuses an invitation created already answered", func(t *testing.T) {
		t.Parallel()

		store, _, owner, account, _ := newInvitedStore(t)

		answered := newInvitation(owner, account.ID, "carol@example.com", "tok-2", baseTime.Add(time.Hour))
		answered.Status = InvitationAccepted

		must.ErrorIs(t, env.createInvitation(t, store, answered.Scope, answered), ErrInvalidInvitationStatus)
	})

	t.Run("refuses an invitation with no expiry", func(t *testing.T) {
		t.Parallel()

		store, _, owner, account, _ := newInvitedStore(t)

		// A link that never expires is one still valid in a mailbox somebody
		// lost control of two years ago.
		forever := newInvitation(owner, account.ID, "carol@example.com", "tok-3", time.Time{})

		must.Error(t, env.createInvitation(t, store, forever.Scope, forever))
	})

	t.Run("refuses a nil invitation", func(t *testing.T) {
		t.Parallel()

		store, _, _, _, _ := newInvitedStore(t)

		must.ErrorIs(t, env.createInvitation(t, store, testScope, nil), ErrNilInvitation)
	})

	t.Run("reads by token and refuses a wrong one", func(t *testing.T) {
		t.Parallel()

		store, _, _, _, invitation := newInvitedStore(t)

		read, err := store.GetInvitationByToken(t.Context(), env.reader(), testScope, invitation.ID, "tok-secret")
		must.NoError(t, err)
		test.EqOp(t, invitation.ID, read.ID)

		// Not found rather than forbidden, so the read is not an oracle for
		// which invitation IDs exist.
		_, err = store.GetInvitationByToken(t.Context(), env.reader(), testScope, invitation.ID, "tok-wrong")
		must.ErrorIs(t, err, ErrInvitationNotFound)

		_, err = store.GetInvitationByToken(t.Context(), env.reader(), otherScope, invitation.ID, "tok-secret")
		must.ErrorIs(t, err, ErrInvitationNotFound)
	})

	t.Run("distinguishes an expired invitation", func(t *testing.T) {
		t.Parallel()

		store, clk, _, _, invitation := newInvitedStore(t)

		clk.advance(73 * time.Hour)

		// Distinct from not-found so the recipient can be told to ask for
		// another rather than that their link was wrong.
		_, err := store.GetInvitationByToken(t.Context(), env.reader(), testScope, invitation.ID, "tok-secret")
		must.ErrorIs(t, err, ErrInvitationExpired)
	})

	t.Run("accepts an invitation and writes the membership it promised", func(t *testing.T) {
		t.Parallel()

		store, _, _, account, invitation := newInvitedStore(t)

		recipient := seedUser(t, env, store, newUser("brian"))

		var membership *Membership

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			membership, err = store.AcceptInvitation(t.Context(), tx, testScope, invitation.ID, "tok-secret", recipient.ID, "thanks")

			return err
		}))

		must.NotNil(t, membership)
		test.EqOp(t, account.ID, membership.BelongsToAccount)
		test.EqOp(t, recipient.ID, membership.BelongsToUser)

		// The roles come off the invitation. A parameter here is where an
		// escalation goes in.
		test.Eq(t, []string{"account_member"}, membership.Roles)

		// Their first account, so it is where they land.
		test.True(t, membership.DefaultAccount)

		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, invitation.ID)
		must.NoError(t, err)
		test.EqOp(t, InvitationAccepted, read.Status)

		// The answer lands in its own column. The sender's message — the thing
		// rendered into the invite email, and the thing a roster shows beside
		// the acceptance — survives being replied to.
		test.EqOp(t, "thanks", read.StatusNote)
		test.EqOp(t, senderNote, read.Note)

		must.NotNil(t, read.ToUser)
		test.EqOp(t, recipient.ID, *read.ToUser)

		stored, err := store.GetMembership(t.Context(), env.reader(), testScope, recipient.ID, account.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"account_member"}, stored.Roles)
	})

	t.Run("accepts once, however many times the link is clicked", func(t *testing.T) {
		t.Parallel()

		store, _, _, _, invitation := newInvitedStore(t)
		recipient := seedUser(t, env, store, newUser("brian"))

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			_, err := store.AcceptInvitation(t.Context(), tx, testScope, invitation.ID, "tok-secret", recipient.ID, "")

			return err
		}))

		// The second click finds nothing pending. An already-answered
		// invitation reads as absent, like a wrong token.
		err := env.inTx(t, func(tx database.Tx) error {
			_, acceptErr := store.AcceptInvitation(t.Context(), tx, testScope, invitation.ID, "tok-secret", recipient.ID, "")

			return acceptErr
		})
		must.ErrorIs(t, err, ErrInvitationNotFound)
	})

	t.Run("refuses acceptance by a user from another directory", func(t *testing.T) {
		t.Parallel()

		store, _, _, account, invitation := newInvitedStore(t)

		// The link is a bearer credential, and the id presented alongside it is
		// whatever the caller says. A neighbor's id satisfies the membership's
		// foreign key exactly as a local one does.
		neighbor := newUser("eve")
		neighbor.Scope = otherScope
		seedUser(t, env, store, neighbor)

		err := env.inTx(t, func(tx database.Tx) error {
			_, acceptErr := store.AcceptInvitation(t.Context(), tx, testScope, invitation.ID, "tok-secret", neighbor.ID, "")

			return acceptErr
		})
		must.ErrorIs(t, err, ErrUserNotFound)

		// The answer and the membership are one transaction, so refusing the
		// membership leaves the invitation pending rather than accepted by
		// somebody who never joined.
		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, invitation.ID)
		must.NoError(t, err)
		test.EqOp(t, InvitationPending, read.Status)
		test.Nil(t, read.ToUser)

		members, err := store.ListAccountMembers(t.Context(), env.reader(), testScope, account.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, members.Data)
	})

	t.Run("refuses to accept without an accepting user", func(t *testing.T) {
		t.Parallel()

		store, _, _, _, invitation := newInvitedStore(t)

		err := env.inTx(t, func(tx database.Tx) error {
			_, acceptErr := store.AcceptInvitation(t.Context(), tx, testScope, invitation.ID, "tok-secret", "", "")

			return acceptErr
		})
		must.Error(t, err)

		must.ErrorIs(t,
			func() error {
				_, acceptErr := store.AcceptInvitation(
					t.Context(), nil, testScope, invitation.ID, "tok-secret", "x", "")

				return acceptErr
			}(),
			ErrNilExecutor,
		)
	})

	t.Run("rejects and cancels, but will not set accepted", func(t *testing.T) {
		t.Parallel()

		store, _, owner, account, invitation := newInvitedStore(t)

		// Accepting writes a membership in the same transaction; a status write
		// here would leave an accepted invitation that produced nothing.
		must.ErrorIs(t,
			env.setInvitationStatus(t, store, testScope, invitation.ID, InvitationAccepted, ""),
			ErrInvalidInvitationStatus,
		)

		must.ErrorIs(t,
			env.setInvitationStatus(t, store, testScope, invitation.ID, InvitationPending, ""),
			ErrInvalidInvitationStatus,
		)

		// A status outside the known set is refused before the write rather
		// than stored and read back as something no branch handles.
		must.ErrorIs(t,
			env.setInvitationStatus(t, store, testScope, invitation.ID, InvitationStatus("nonsense"), ""),
			platformerrors.ErrUnrecognizedInputValue,
		)

		must.NoError(t, env.setInvitationStatus(t, store, testScope, invitation.ID, InvitationRejected, "no thanks"))

		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, invitation.ID)
		must.NoError(t, err)
		test.EqOp(t, InvitationRejected, read.Status)
		test.EqOp(t, "no thanks", read.StatusNote)
		test.EqOp(t, senderNote, read.Note)
		test.False(t, read.Status.Pending())

		// A rejection cannot be overwritten by a later cancellation.
		must.ErrorIs(t,
			env.setInvitationStatus(t, store, testScope, invitation.ID, InvitationCancelled, ""),
			ErrInvitationNotFound,
		)

		second := newInvitation(owner, account.ID, "carol@example.com", "tok-4", baseTime.Add(time.Hour))
		must.NoError(t, env.createInvitation(t, store, second.Scope, second))
		must.NoError(t, env.setInvitationStatus(t, store, testScope, second.ID, InvitationCancelled, "withdrawn"))
	})

	t.Run("pages pending invitations, redacted", func(t *testing.T) {
		t.Parallel()

		store, _, owner, account, invitation := newInvitedStore(t)

		answered := newInvitation(owner, account.ID, "carol@example.com", "tok-5", baseTime.Add(time.Hour))
		must.NoError(t, env.createInvitation(t, store, answered.Scope, answered))
		must.NoError(t, env.setInvitationStatus(t, store, testScope, answered.ID, InvitationRejected, ""))

		sent, err := store.ListInvitationsFromUser(t.Context(), env.reader(), testScope, owner.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, sent.Data)
		test.EqOp(t, invitation.ID, sent.Data[0].ID)

		// The token is the credential that accepts the invitation, and a
		// sender's own list would otherwise hand every recipient's link back to
		// the sender's browser.
		test.EqOp(t, "", sent.Data[0].Token)
		test.Eq(t, []string{"account_member"}, sent.Data[0].Roles)

		// Both notes are in the projection, so a roster built from a page
		// renders the message it was sent with rather than reading it back one
		// invitation at a time.
		test.EqOp(t, senderNote, sent.Data[0].Note)
		test.EqOp(t, "", sent.Data[0].StatusNote)

		received, err := store.ListInvitationsForEmailAddress(t.Context(), env.reader(), testScope, "brian@example.com", nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, received.Data)
		test.EqOp(t, "", received.Data[0].Token)
		test.EqOp(t, senderNote, received.Data[0].Note)

		none, err := store.ListInvitationsForEmailAddress(t.Context(), env.reader(), otherScope, "brian@example.com", nil)
		must.NoError(t, err)
		test.SliceEmpty(t, none.Data)
	})

	t.Run("pages pending invitations newest first when the filter says so", func(t *testing.T) {
		t.Parallel()

		// Both keyed invitation reads are list variants, so both are two
		// statements — and a direction honored on the sender's list and dropped
		// on the recipient's is the same wrong order in a different response.
		store, _, owner, account, first := newInvitedStore(t)

		second := newInvitation(owner, account.ID, "brian@example.com", "tok-6", baseTime.Add(time.Hour))
		must.NoError(t, env.createInvitation(t, store, second.Scope, second))

		newestFirst := &filtering.QueryFilter{SortBy: filtering.SortDescending}

		sent, err := store.ListInvitationsFromUser(t.Context(), env.reader(), testScope, owner.ID, newestFirst)
		must.NoError(t, err)
		must.SliceLen(t, 2, sent.Data)
		test.EqOp(t, second.ID, sent.Data[0].ID)
		test.EqOp(t, first.ID, sent.Data[1].ID)

		// Still pending-only and still redacted: the direction chooses between
		// two statements that share every predicate, and the redaction is the
		// store's rule either way.
		for _, invitation := range sent.Data {
			test.EqOp(t, InvitationPending, invitation.Status)
			test.EqOp(t, "", invitation.Token)
		}

		received, err := store.ListInvitationsForEmailAddress(t.Context(), env.reader(), testScope, "brian@example.com", newestFirst)
		must.NoError(t, err)
		must.SliceLen(t, 2, received.Data)
		test.EqOp(t, second.ID, received.Data[0].ID)
		test.EqOp(t, first.ID, received.Data[1].ID)

		// And the ascending read of the same two is the same pair the other way
		// round, which is what says the direction did the reversing rather than
		// the insertion order.
		ascending, err := store.ListInvitationsForEmailAddress(t.Context(), env.reader(), testScope, "brian@example.com", nil)
		must.NoError(t, err)
		must.SliceLen(t, 2, ascending.Data)
		test.EqOp(t, first.ID, ascending.Data[0].ID)
		test.EqOp(t, second.ID, ascending.Data[1].ID)
	})
}
