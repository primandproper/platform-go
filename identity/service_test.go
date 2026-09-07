package identity

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/database"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/pointer"
	"github.com/primandproper/platform-go/v14/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errHookRefused is what a hook returns when a case is about the abort, rather
// than about what the hook saw.
var errHookRefused = platformerrors.New("hook said no")

// recordingHooks is the Hooks a case reads back: which hook ran, and with what.
//
// It embeds NoopHooks rather than implementing all fifteen, which is the shape the
// documentation tells consumers to use — so the suite exercises that shape as
// well as the hooks it overrides.
type recordingHooks struct {
	NoopHooks

	// probe runs at the end of every hook below. It is how a case aborts an
	// operation (by returning an error) and how one proves the hook is inside
	// the operation's own transaction (by reading a row nothing has committed).
	probe func(ctx context.Context, tx database.Tx) error

	registration *Registration
	invitation   *Invitation
	acceptance   *Acceptance
	account      *Account
	membership   *Membership
	user         *User

	previousOwnerUserID string
	previousAccountID   string
	previousStatus      AccountStatus
	newDefaultAccountID string

	calls            []string
	previousRoles    []string
	changed          []string
	agreements       []Agreement
	endedMemberships []*Membership

	mu sync.Mutex
}

var _ Hooks = (*recordingHooks)(nil)

func (h *recordingHooks) record(ctx context.Context, tx database.Tx, name string) error {
	h.calls = append(h.calls, name)

	if h.probe != nil {
		return h.probe(ctx, tx)
	}

	return nil
}

// ran reports how many times the named hook was called.
func (h *recordingHooks) ran(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	count := 0

	for _, call := range h.calls {
		if call == name {
			count++
		}
	}

	return count
}

func (h *recordingHooks) AfterRegister(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, registration *Registration,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.registration = registration

	return h.record(ctx, tx, "register")
}

func (h *recordingHooks) AfterInvite(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, invitation *Invitation,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.invitation = invitation

	return h.record(ctx, tx, "invite")
}

func (h *recordingHooks) AfterAcceptInvitation(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, acceptance *Acceptance,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.acceptance = acceptance

	return h.record(ctx, tx, "accept")
}

func (h *recordingHooks) AfterRejectInvitation(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, invitation *Invitation,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.invitation = invitation

	return h.record(ctx, tx, "reject")
}

func (h *recordingHooks) AfterCancelInvitation(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, invitation *Invitation,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.invitation = invitation

	return h.record(ctx, tx, "cancel")
}

func (h *recordingHooks) AfterTransferAccountOwnership(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, account *Account, previousOwnerUserID string,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.account, h.previousOwnerUserID = account, previousOwnerUserID

	return h.record(ctx, tx, "transfer")
}

func (h *recordingHooks) AfterSetDefaultAccount(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, membership *Membership, previousAccountID string,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.membership, h.previousAccountID = membership, previousAccountID

	return h.record(ctx, tx, "default")
}

func (h *recordingHooks) AfterArchiveUser(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User, endedMemberships []*Membership,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user, h.endedMemberships = user, endedMemberships

	return h.record(ctx, tx, "archive")
}

func (h *recordingHooks) AfterUpdateUserAccountStatus(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User, previousStatus AccountStatus,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user, h.previousStatus = user, previousStatus

	return h.record(ctx, tx, "status")
}

func (h *recordingHooks) AfterSetUserServiceRoles(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User, previousRoles []string,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user, h.previousRoles = user, previousRoles

	return h.record(ctx, tx, "roles")
}

func (h *recordingHooks) AfterUpdateProfile(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User, changed []string,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user, h.changed = user, changed

	return h.record(ctx, tx, "profile")
}

func (h *recordingHooks) AfterUpdateAccount(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, account *Account, changed []string,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.account, h.changed = account, changed

	return h.record(ctx, tx, "account")
}

func (h *recordingHooks) AfterRecordAgreement(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User, agreements []Agreement,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user, h.agreements = user, agreements

	return h.record(ctx, tx, "agreement")
}

func (h *recordingHooks) AfterSetMembershipRoles(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, membership *Membership, previousRoles []string,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.membership, h.previousRoles = membership, previousRoles

	return h.record(ctx, tx, "membership_roles")
}

func (h *recordingHooks) AfterRemoveMembership(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, membership *Membership, newDefaultAccountID string,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.membership, h.newDefaultAccountID = membership, newDefaultAccountID

	return h.record(ctx, tx, "remove_membership")
}

// newService builds a Service over a freshly migrated set of tables, and hands
// back the store beneath it so a case can read the rows the Service wrote
// without going through the thing it is testing.
func (e *storeEnv) newService(t *testing.T, hooks Hooks) (*Service, *SQLStore) {
	t.Helper()

	store := e.newStore(t)

	service, err := NewService(e.client, store, WithHooks(hooks))
	must.NoError(t, err)

	return service, store
}

// futureExpiry is an expiry the store's own clock has not reached.
//
// The service suite builds its stores on the real clock rather than the fixed
// one the invitation store's cases use, because what it is about is the
// transaction rather than the timestamps — so an invitation it issues has to
// expire in the running process's future rather than in baseTime's.
func futureExpiry() time.Time { return time.Now().UTC().Add(24 * time.Hour) }

// registerAda is the registration nearly every case below starts from.
func registerAda(t *testing.T, service *Service, username string) *Registration {
	t.Helper()

	registration, err := service.Register(t.Context(), testScope,
		newUser(username), newAccount(username+"'s account", ""), []string{"account_admin"})
	must.NoError(t, err)

	return registration
}

// runServiceSuite covers the orchestration over the store: the operations that
// are more than one write, and the hooks that commit with them.
//
// It runs per dialect with the store suites, because what it asserts is what
// committed — and "the hook's write and the row landed together" is a claim
// about a transaction on a real server rather than about this package's Go.
func runServiceSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("registers a user, an account, and the owner membership", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		test.NotEq(t, "", registration.User.ID)
		test.NotEq(t, "", registration.Account.ID)
		test.NotEq(t, "", registration.Membership.ID)
		test.False(t, registration.User.CreatedAt.IsZero())

		// The registrant owns the account they registered with, and is on its
		// roster: an owner who is not a member is the state every ownership
		// check resolves through and finds nobody.
		test.EqOp(t, registration.User.ID, registration.Account.OwnerUserID)
		test.EqOp(t, registration.Account.ID, registration.Membership.BelongsToAccount)
		test.True(t, registration.Membership.DefaultAccount)
		test.Eq(t, []string{"account_admin"}, registration.Membership.Roles)

		// All three committed, read outside the transaction that wrote them.
		user, err := store.GetUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)
		test.EqOp(t, "ada", user.Username)

		account, err := store.GetAccount(t.Context(), env.reader(), testScope, registration.Account.ID)
		must.NoError(t, err)
		test.EqOp(t, registration.User.ID, account.OwnerUserID)

		membership, err := store.GetMembership(t.Context(), env.reader(),
			testScope, registration.User.ID, registration.Account.ID)
		must.NoError(t, err)
		test.True(t, membership.DefaultAccount)

		// One hook, holding the same value the caller got back.
		test.EqOp(t, 1, hooks.ran("register"))
		test.EqOp(t, registration, hooks.registration)
	})

	t.Run("the register hook reads the writes it is committing with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		var seen *User

		hooks.probe = func(ctx context.Context, tx database.Tx) error {
			// Uncommitted, and readable — which is the property the whole hooks
			// seam rests on. A hook running after the commit, or on a
			// connection of its own, could not do this.
			user, err := store.GetUser(ctx, tx, testScope, hooks.registration.User.ID)
			seen = user

			return err
		}

		registration := registerAda(t, service, "grace")

		must.NotNil(t, seen)
		test.EqOp(t, registration.User.ID, seen.ID)
		test.EqOp(t, "grace", seen.Username)
	})

	t.Run("a failing register hook rolls the whole registration back", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{probe: func(context.Context, database.Tx) error { return errHookRefused }}
		service, store := env.newService(t, hooks)

		user, account := newUser("ada"), newAccount("ada's account", "")

		_, err := service.Register(t.Context(), testScope, user, account, []string{"account_admin"})
		must.ErrorIs(t, err, errHookRefused)

		// The user's ID was written back onto the caller's value before the
		// abort, so the row is looked for by the id the store generated — and
		// there is none.
		_, err = store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.ErrorIs(t, err, ErrUserNotFound)

		_, err = store.GetUserByUsername(t.Context(), env.reader(), testScope, "ada")
		must.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("refuses an account naming somebody other than the registrant", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		_, err := service.Register(t.Context(), testScope,
			newUser("ada"), newAccount("ada's account", "somebody-else"), []string{"account_admin"})
		must.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)

		test.EqOp(t, 0, hooks.ran("register"))

		_, err = store.GetUserByUsername(t.Context(), env.reader(), testScope, "ada")
		must.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("refuses a registration missing its user or its account", func(t *testing.T) {
		t.Parallel()

		service, _ := env.newService(t, &recordingHooks{})

		_, err := service.Register(t.Context(), testScope, nil, newAccount("x", ""), []string{"r"})
		must.ErrorIs(t, err, ErrNilUser)

		_, err = service.Register(t.Context(), testScope, newUser("ada"), nil, []string{"r"})
		must.ErrorIs(t, err, ErrNilAccount)
	})

	t.Run("issues an invitation and hands the hook the token", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		invitation := newInvitation(registration.User, registration.Account.ID,
			"grace@example.com", "the-token", futureExpiry())
		invitation.ID = ""

		must.NoError(t, service.Invite(t.Context(), testScope, invitation))

		test.NotEq(t, "", invitation.ID)
		test.False(t, invitation.CreatedAt.IsZero())

		test.EqOp(t, 1, hooks.ran("invite"))

		// The caller's own value, so the token a hook needs to mail the link is
		// still on it — the one invitation in this package that is not redacted.
		must.NotNil(t, hooks.invitation)
		test.EqOp(t, "the-token", hooks.invitation.Token)

		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, invitation.ID)
		must.NoError(t, err)
		test.EqOp(t, InvitationPending, read.Status)
	})

	t.Run("a failing invite hook leaves no invitation", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		hooks.probe = func(context.Context, database.Tx) error { return errHookRefused }

		invitation := newInvitation(registration.User, registration.Account.ID,
			"grace@example.com", "the-token", futureExpiry())

		must.ErrorIs(t, service.Invite(t.Context(), testScope, invitation), errHookRefused)

		_, err := store.GetInvitation(t.Context(), env.reader(), testScope, invitation.ID)
		must.ErrorIs(t, err, ErrInvitationNotFound)
	})

	t.Run("refuses an invitation that is not there", func(t *testing.T) {
		t.Parallel()

		service, _ := env.newService(t, &recordingHooks{})

		must.ErrorIs(t, service.Invite(t.Context(), testScope, nil), ErrNilInvitation)
	})

	t.Run("accepts an invitation, minting the membership it promised", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		sender := registerAda(t, service, "ada")
		recipient := seedUser(t, env, store, newUser("grace"))

		invitation := newInvitation(sender.User, sender.Account.ID,
			recipient.EmailAddress, "the-token", futureExpiry())
		must.NoError(t, service.Invite(t.Context(), testScope, invitation))

		acceptance, err := service.AcceptInvitation(t.Context(), testScope,
			invitation.ID, "the-token", recipient.ID, "glad to")
		must.NoError(t, err)

		test.EqOp(t, InvitationAccepted, acceptance.Invitation.Status)
		test.EqOp(t, "glad to", acceptance.Invitation.StatusNote)

		// The sender's message survives the answer written beside it.
		test.EqOp(t, senderNote, acceptance.Invitation.Note)

		// Read back by the Service, and so redacted: the token is the
		// credential that accepts, and it has been spent.
		test.EqOp(t, "", acceptance.Invitation.Token)

		// The roles come off the invitation rather than from a parameter.
		test.Eq(t, []string{"account_member"}, acceptance.Membership.Roles)
		test.EqOp(t, sender.Account.ID, acceptance.Membership.BelongsToAccount)

		// The recipient belonged to nothing, so this is where they land.
		test.True(t, acceptance.Membership.DefaultAccount)

		test.EqOp(t, 1, hooks.ran("accept"))
		test.EqOp(t, acceptance, hooks.acceptance)
	})

	t.Run("a failing accept hook leaves the invitation pending and mints nothing", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		sender := registerAda(t, service, "ada")
		recipient := seedUser(t, env, store, newUser("grace"))

		invitation := newInvitation(sender.User, sender.Account.ID,
			recipient.EmailAddress, "the-token", futureExpiry())
		must.NoError(t, service.Invite(t.Context(), testScope, invitation))

		hooks.probe = func(context.Context, database.Tx) error { return errHookRefused }

		_, err := service.AcceptInvitation(t.Context(), testScope,
			invitation.ID, "the-token", recipient.ID, "glad to")
		must.ErrorIs(t, err, errHookRefused)

		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, invitation.ID)
		must.NoError(t, err)
		test.EqOp(t, InvitationPending, read.Status)

		_, err = store.GetMembership(t.Context(), env.reader(), testScope, recipient.ID, sender.Account.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)
	})

	t.Run("rejects an invitation only with its token", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		sender := registerAda(t, service, "ada")

		invitation := newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry())
		must.NoError(t, service.Invite(t.Context(), testScope, invitation))

		// The ID alone answers the store's status write, and must not answer
		// this one: a rejection comes from whoever followed the link.
		_, err := service.RejectInvitation(t.Context(), testScope, invitation.ID, "guessed", "no thanks")
		must.ErrorIs(t, err, ErrInvitationNotFound)
		test.EqOp(t, 0, hooks.ran("reject"))

		still, err := store.GetInvitation(t.Context(), env.reader(), testScope, invitation.ID)
		must.NoError(t, err)
		test.EqOp(t, InvitationPending, still.Status)

		rejected, err := service.RejectInvitation(t.Context(), testScope,
			invitation.ID, "the-token", "no thanks")
		must.NoError(t, err)
		test.EqOp(t, InvitationRejected, rejected.Status)
		test.EqOp(t, "no thanks", rejected.StatusNote)
		test.EqOp(t, senderNote, rejected.Note)
		test.EqOp(t, "", rejected.Token)
		test.EqOp(t, 1, hooks.ran("reject"))
	})

	t.Run("cancels an invitation, and a second answer finds nothing pending", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		sender := registerAda(t, service, "ada")

		invitation := newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry())
		must.NoError(t, service.Invite(t.Context(), testScope, invitation))

		cancelled, err := service.CancelInvitation(t.Context(), testScope, invitation.ID, "hired somebody")
		must.NoError(t, err)
		test.EqOp(t, InvitationCancelled, cancelled.Status)
		test.EqOp(t, "hired somebody", cancelled.StatusNote)
		test.EqOp(t, "", cancelled.Token)
		test.EqOp(t, 1, hooks.ran("cancel"))

		_, err = service.CancelInvitation(t.Context(), testScope, invitation.ID, "again")
		must.ErrorIs(t, err, ErrInvitationNotFound)
		test.EqOp(t, 1, hooks.ran("cancel"))
	})

	t.Run("transfers ownership and names the owner it came from", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		owner := registerAda(t, service, "ada")
		successor := seedUserInto(t, env, store, newUser("grace"), owner.Account.ID)

		account, err := service.TransferAccountOwnership(t.Context(), testScope,
			owner.Account.ID, successor.ID)
		must.NoError(t, err)
		test.EqOp(t, successor.ID, account.OwnerUserID)

		test.EqOp(t, 1, hooks.ran("transfer"))
		test.EqOp(t, owner.User.ID, hooks.previousOwnerUserID)
		test.EqOp(t, successor.ID, hooks.account.OwnerUserID)

		// The old owner keeps their membership: transferring and ejecting are
		// different acts.
		_, err = store.GetMembership(t.Context(), env.reader(), testScope, owner.User.ID, owner.Account.ID)
		must.NoError(t, err)
	})

	t.Run("a failing transfer hook leaves the account with the owner it had", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		owner := registerAda(t, service, "ada")
		successor := seedUserInto(t, env, store, newUser("grace"), owner.Account.ID)

		hooks.probe = func(context.Context, database.Tx) error { return errHookRefused }

		_, err := service.TransferAccountOwnership(t.Context(), testScope, owner.Account.ID, successor.ID)
		must.ErrorIs(t, err, errHookRefused)

		account, err := store.GetAccount(t.Context(), env.reader(), testScope, owner.Account.ID)
		must.NoError(t, err)
		test.EqOp(t, owner.User.ID, account.OwnerUserID)
	})

	t.Run("sets the default account and names the one it replaced", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")
		second := seedAccountFor(t, env, store, registration.User, "the second account")

		membership, err := service.SetDefaultAccount(t.Context(), testScope, registration.User.ID, second.ID)
		must.NoError(t, err)
		test.True(t, membership.DefaultAccount)
		test.EqOp(t, second.ID, membership.BelongsToAccount)

		test.EqOp(t, 1, hooks.ran("default"))
		test.EqOp(t, registration.Account.ID, hooks.previousAccountID)

		// One default per user, not one per call.
		memberships, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)
		must.SliceLen(t, 2, memberships)

		defaults := 0

		for _, m := range memberships {
			if m.DefaultAccount {
				defaults++

				test.EqOp(t, second.ID, m.BelongsToAccount)
			}
		}

		test.EqOp(t, 1, defaults)
	})

	t.Run("refuses a default account the user is not a member of", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")
		stranger := seedUser(t, env, store, newUser("grace"))
		elsewhere := seedAccountFor(t, env, store, stranger, "somebody else's account")

		_, err := service.SetDefaultAccount(t.Context(), testScope, registration.User.ID, elsewhere.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)
		test.EqOp(t, 0, hooks.ran("default"))
	})

	t.Run("archives a user with the memberships the archival ended", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		owner := registerAda(t, service, "ada")
		member := seedUserInto(t, env, store, newUser("grace"), owner.Account.ID)

		archived, err := service.ArchiveUser(t.Context(), testScope, member.ID)
		must.NoError(t, err)

		// Read back by the Service, and so redacted — an archival's audit
		// entry has no business carrying a password hash.
		test.EqOp(t, "", archived.HashedPassword)
		test.EqOp(t, "grace", archived.Username)

		test.EqOp(t, 1, hooks.ran("archive"))
		must.SliceLen(t, 1, hooks.endedMemberships)
		test.EqOp(t, owner.Account.ID, hooks.endedMemberships[0].BelongsToAccount)

		_, err = store.GetUser(t.Context(), env.reader(), testScope, member.ID)
		must.ErrorIs(t, err, ErrUserNotFound)

		_, err = store.GetMembership(t.Context(), env.reader(), testScope, member.ID, owner.Account.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)
	})

	t.Run("refuses to archive an account's last owner", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		owner := registerAda(t, service, "ada")

		_, err := service.ArchiveUser(t.Context(), testScope, owner.User.ID)
		must.ErrorIs(t, err, ErrLastAccountOwner)
		test.EqOp(t, 0, hooks.ran("archive"))

		_, err = store.GetUser(t.Context(), env.reader(), testScope, owner.User.ID)
		must.NoError(t, err)
	})

	t.Run("moves a user between statuses and names the one before", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		banned, err := service.UpdateUserAccountStatus(t.Context(), testScope,
			registration.User.ID, StatusBanned, "spam")
		must.NoError(t, err)
		test.EqOp(t, StatusBanned, banned.AccountStatus)
		test.EqOp(t, "spam", banned.AccountStatusExplanation)
		test.EqOp(t, "", banned.HashedPassword)

		test.EqOp(t, 1, hooks.ran("status"))
		test.EqOp(t, StatusGood, hooks.previousStatus)

		read, err := store.GetUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusBanned, read.AccountStatus)
	})

	t.Run("a failing status hook leaves the standing alone", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		hooks.probe = func(context.Context, database.Tx) error { return errHookRefused }

		_, err := service.UpdateUserAccountStatus(t.Context(), testScope,
			registration.User.ID, StatusBanned, "spam")
		must.ErrorIs(t, err, errHookRefused)

		read, err := store.GetUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusGood, read.AccountStatus)
	})

	t.Run("replaces service roles and names the set before", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := newUser("ada")
		user.ServiceRoles = []string{"service_user"}

		registration, err := service.Register(t.Context(), testScope,
			user, newAccount("ada's account", ""), []string{"account_admin"})
		must.NoError(t, err)

		updated, err := service.SetUserServiceRoles(t.Context(), testScope,
			registration.User.ID, []string{"service_admin"})
		must.NoError(t, err)
		test.Eq(t, []string{"service_admin"}, updated.ServiceRoles)

		test.EqOp(t, 1, hooks.ran("roles"))
		test.Eq(t, []string{"service_user"}, hooks.previousRoles)

		read, err := store.GetUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"service_admin"}, read.ServiceRoles)
	})

	t.Run("answers for a user in another directory as absent", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		_, err := service.UpdateUserAccountStatus(t.Context(), otherScope,
			registration.User.ID, StatusBanned, "spam")
		must.ErrorIs(t, err, ErrUserNotFound)

		_, err = service.ArchiveUser(t.Context(), otherScope, registration.User.ID)
		must.ErrorIs(t, err, ErrUserNotFound)

		test.EqOp(t, 0, hooks.ran("status"))
		test.EqOp(t, 0, hooks.ran("archive"))
	})

	t.Run("a profile save writes only the fields that moved", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		updated, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{FirstName: pointer.To("Augusta"), Username: pointer.To("ada")})
		must.NoError(t, err)

		test.EqOp(t, "Augusta", updated.FirstName)
		test.EqOp(t, "ada", updated.Username)

		// Only FirstName is reported, because Username was set to what it
		// already held. A hook recording "the username changed" on a save that
		// did not touch it is an audit trail nobody can trust.
		test.EqOp(t, 1, hooks.ran("profile"))
		test.Eq(t, []string{"firstName"}, hooks.changed)

		// Committed, read outside the transaction that wrote it.
		saved, err := store.GetUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)
		test.EqOp(t, "Augusta", saved.FirstName)
	})

	t.Run("a profile save that changes nothing writes nothing", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		before, err := store.GetUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)

		updated, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{Username: pointer.To(before.Username)})
		must.NoError(t, err)
		must.NotNil(t, updated)

		// No hook, and no LastUpdatedAt: a form submitted unedited is the common
		// case, and recording it as a change makes an audit trail mostly noise.
		test.EqOp(t, 0, hooks.ran("profile"))

		after, err := store.GetUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)
		test.Nil(t, after.LastUpdatedAt)
	})

	t.Run("a failing profile hook rolls the save back", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		// Installed after the registration, so the refusal lands on the save
		// rather than on the setup.
		hooks.probe = func(context.Context, database.Tx) error { return errHookRefused }

		_, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{FirstName: pointer.To("Augusta")})
		must.ErrorIs(t, err, errHookRefused)

		saved, err := store.GetUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)
		test.NotEq(t, "Augusta", saved.FirstName)
	})

	t.Run("an account save reports the fields that moved", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		updated, err := service.UpdateAccount(t.Context(), testScope, registration.Account.ID,
			&AccountUpdate{Name: pointer.To("Analytical Engines"), TimeZone: pointer.To("Europe/London")})
		must.NoError(t, err)

		test.EqOp(t, "Analytical Engines", updated.Name)
		test.EqOp(t, 1, hooks.ran("account"))
		test.Eq(t, []string{"name", "timeZone"}, hooks.changed)

		saved, err := store.GetAccount(t.Context(), env.reader(), testScope, registration.Account.ID)
		must.NoError(t, err)
		test.EqOp(t, "Analytical Engines", saved.Name)
	})

	t.Run("an account save that changes nothing writes nothing", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		before, err := store.GetAccount(t.Context(), env.reader(), testScope, registration.Account.ID)
		must.NoError(t, err)

		updated, err := service.UpdateAccount(t.Context(), testScope, registration.Account.ID,
			&AccountUpdate{Name: pointer.To(before.Name)})
		must.NoError(t, err)
		must.NotNil(t, updated)

		// The same bargain the profile save makes, for the other noun: no hook,
		// and no LastUpdatedAt to make an unedited form look like an edit.
		test.EqOp(t, 0, hooks.ran("account"))

		after, err := store.GetAccount(t.Context(), env.reader(), testScope, registration.Account.ID)
		must.NoError(t, err)
		test.Nil(t, after.LastUpdatedAt)
	})

	t.Run("an account save moves the billing address", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		address := BillingAddress{
			Line1:      "12 Analytical Way",
			City:       "London",
			PostalCode: "SW1A 1AA",
			Country:    "GB",
		}

		updated, err := service.UpdateAccount(t.Context(), testScope, registration.Account.ID,
			&AccountUpdate{BillingAddress: &address})
		must.NoError(t, err)

		test.EqOp(t, address, updated.BillingAddress)
		test.Eq(t, []string{"billingAddress"}, hooks.changed)

		saved, err := store.GetAccount(t.Context(), env.reader(), testScope, registration.Account.ID)
		must.NoError(t, err)
		test.EqOp(t, address, saved.BillingAddress)

		// Sent again unchanged, the address is not a change: the comparison is
		// on the whole address, so a form that round-trips one writes nothing.
		_, err = service.UpdateAccount(t.Context(), testScope, registration.Account.ID,
			&AccountUpdate{BillingAddress: &address})
		must.NoError(t, err)
		test.EqOp(t, 1, hooks.ran("account"))
	})

	t.Run("a save with no update at all is refused", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		// Distinct from a form with every field absent, which is a save that
		// changes nothing: a nil update is a caller who assembled no form.
		_, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID, nil)
		must.ErrorIs(t, err, ErrNilProfileUpdate)

		_, err = service.UpdateAccount(t.Context(), testScope, registration.Account.ID, nil)
		must.ErrorIs(t, err, ErrNilAccountUpdate)

		test.EqOp(t, 0, hooks.ran("profile"))
		test.EqOp(t, 0, hooks.ran("account"))
	})

	t.Run("recording an agreement stamps the user and hooks once", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		updated, err := service.RecordAgreement(t.Context(), testScope, registration.User.ID,
			TermsOfService, PrivacyPolicy)
		must.NoError(t, err)

		must.NotNil(t, updated.LastAcceptedTermsOfService)
		must.NotNil(t, updated.LastAcceptedPrivacyPolicy)

		// One clock read for both, so a later comparison cannot order them.
		test.EqOp(t, *updated.LastAcceptedTermsOfService, *updated.LastAcceptedPrivacyPolicy)

		test.EqOp(t, 1, hooks.ran("agreement"))
		test.Eq(t, []Agreement{TermsOfService, PrivacyPolicy}, hooks.agreements)
	})

	t.Run("recording no agreement is refused", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		_, err := service.RecordAgreement(t.Context(), testScope, registration.User.ID)
		must.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)

		test.EqOp(t, 0, hooks.ran("agreement"))
	})

	t.Run("setting membership roles reports the set held before", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		updated, err := service.SetMembershipRoles(t.Context(), testScope,
			registration.User.ID, registration.Account.ID, []string{"account_admin", "billing"})
		must.NoError(t, err)

		test.Eq(t, []string{"account_admin", "billing"}, updated.Roles)

		test.EqOp(t, 1, hooks.ran("membership_roles"))
		test.Eq(t, []string{"account_admin"}, hooks.previousRoles)

		saved, err := store.GetMembership(t.Context(), env.reader(),
			testScope, registration.User.ID, registration.Account.ID)
		must.NoError(t, err)
		test.SliceContains(t, saved.Roles, "billing")
	})

	t.Run("setting membership roles for a non-member is refused", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")
		outsider := registerAda(t, service, "grace")

		_, err := service.SetMembershipRoles(t.Context(), testScope,
			outsider.User.ID, registration.Account.ID, []string{"billing"})
		must.ErrorIs(t, err, ErrMembershipNotFound)

		test.EqOp(t, 0, hooks.ran("membership_roles"))
	})

	t.Run("removing a membership hands the hook the row it just ended", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		owner := registerAda(t, service, "ada")
		joiner := registerAda(t, service, "grace")

		// Put the joiner on the owner's account, so there is a membership to end
		// that is not somebody's last standing as an owner.
		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			return store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    joiner.User.ID,
				BelongsToAccount: owner.Account.ID,
				Roles:            []string{"member"},
			})
		}))

		removed, err := service.RemoveMembership(t.Context(), testScope, joiner.User.ID, owner.Account.ID)
		must.NoError(t, err)
		must.NotNil(t, removed)

		// The row as it stood before it ended, which is the last moment anything
		// could produce it: an ended membership is returned by no read here.
		test.EqOp(t, owner.Account.ID, removed.BelongsToAccount)
		test.Eq(t, []string{"member"}, removed.Roles)

		test.EqOp(t, 1, hooks.ran("remove_membership"))

		_, err = store.GetMembership(t.Context(), env.reader(), testScope, joiner.User.ID, owner.Account.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)
	})

	t.Run("removing the default membership reports where the default moved", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		first := registerAda(t, service, "ada")
		second := registerAda(t, service, "grace")

		// A user who owns nothing, so that the membership being removed is not
		// somebody's last standing as an owner — which RemoveMembership refuses
		// outright, and which every registered user's own account makes them.
		joiner := &User{Username: "carol", EmailAddress: "carol@example.com"}

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			if err := store.CreateUser(t.Context(), tx, testScope, joiner); err != nil {
				return err
			}

			// The first membership a user holds anywhere becomes their default,
			// so this is the one whose removal has to move it.
			if err := store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    joiner.ID,
				BelongsToAccount: first.Account.ID,
				Roles:            []string{"member"},
			}); err != nil {
				return err
			}

			return store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    joiner.ID,
				BelongsToAccount: second.Account.ID,
				Roles:            []string{"member"},
			})
		}))

		removed, err := service.RemoveMembership(t.Context(), testScope, joiner.ID, first.Account.ID)
		must.NoError(t, err)
		test.True(t, removed.DefaultAccount, test.Sprint("the membership removed was not the default"))

		// The store moves the default to another live membership rather than
		// leaving a user with memberships and nowhere to land, and the hook is
		// told where it went.
		test.EqOp(t, second.Account.ID, hooks.newDefaultAccountID)
	})

	t.Run("removing the last owner of an account is refused", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		_, err := service.RemoveMembership(t.Context(), testScope,
			registration.User.ID, registration.Account.ID)
		must.ErrorIs(t, err, ErrLastAccountOwner)

		test.EqOp(t, 0, hooks.ran("remove_membership"))
	})
}

func TestNewService(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		service, err := NewService(nil, &unusedStore{})
		must.ErrorIs(t, err, ErrNilDatabaseClient)
		test.Nil(t, service)
	})

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		service, err := NewService(env.client, nil)
		must.ErrorIs(t, err, ErrNilStore)
		test.Nil(t, service)
	})

	T.Run("defaults to hooks that do nothing", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		// A nil option is skipped, and WithHooks(nil) leaves the default in
		// place rather than installing a nil interface nothing could call.
		service, err := NewService(env.client, store, nil, WithHooks(nil))
		must.NoError(t, err)
		must.NotNil(t, service)
		_, isNoop := service.hooks.(NoopHooks)
		test.True(t, isNoop)

		// The whole point of the default: an operation runs with no hooks
		// configured at all.
		_, err = service.Register(t.Context(), testScope,
			newUser("ada"), newAccount("ada's account", ""), []string{"account_admin"})
		must.NoError(t, err)
	})
}

// unusedStore is the smallest thing that satisfies Store, for the one case that
// needs a non-nil store and never calls it.
type unusedStore struct{ Store }

func TestNoopHooks(t *testing.T) {
	t.Parallel()

	var hooks Hooks = NoopHooks{}

	// Every method, enumerated off the interface rather than listed. The point
	// of the type is that a consumer embedding it gets a working implementation
	// of all of them — one that returned an error would abort an operation its
	// embedder never opted into — and a list here is a second place to forget a
	// method, which is what a written-out one did when Hooks grew from ten to
	// fifteen.
	hooksType := reflect.TypeFor[Hooks]()
	noop := reflect.ValueOf(hooks)

	must.True(t, hooksType.NumMethod() > 0, must.Sprint("Hooks declares no methods, so this asserted nothing"))

	for method := range hooksType.Methods() {
		t.Run(method.Name, func(t *testing.T) {
			t.Parallel()

			// The zero value of every argument, which is what a noop has to
			// tolerate: a nil entity, a nil Tx and the zero scope all reach it
			// from a Service whose operation failed on the way past.
			args := make([]reflect.Value, 0, method.Type.NumIn())
			for in := range method.Type.Ins() {
				args = append(args, reflect.New(in).Elem())
			}

			args[0] = reflect.ValueOf(t.Context())

			returned := noop.MethodByName(method.Name).Call(args)
			must.SliceLen(t, 1, returned)
			test.Nil(t, returned[0].Interface(),
				test.Sprintf("NoopHooks.%s returned an error", method.Name))
		})
	}
}
