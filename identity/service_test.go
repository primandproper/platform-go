package identity

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errHookRefused is what a hook returns when a case is about the abort, rather
// than about what the hook saw.
var errHookRefused = platformerrors.New("hook said no")

// recordingHooks is the Hooks a case reads back: which hook ran, and with what.
//
// It embeds NoopHooks rather than implementing all twenty-four, which is the shape
// the documentation tells consumers to use — so the suite exercises that shape as
// well as the hooks it overrides.
type recordingHooks struct {
	NoopHooks

	// probe runs at the end of every hook below. It is how a case aborts an
	// operation (by returning an error) and how one proves the hook is inside
	// the operation's own transaction (by reading a row nothing has committed).
	probe func(ctx context.Context, tx database.Tx) error

	registration        *Registration
	invitedRegistration *InvitedRegistration
	invitation          *Invitation
	acceptance          *Acceptance
	account             *Account
	membership          *Membership
	user                *User

	// What the credential hooks were told about the column their write cleared.
	previousVerifiedAt *time.Time

	previousOwnerUserID string
	previousAccountID   string
	previousStatus      AccountStatus
	newDefaultAccountID string

	calls            []string
	previousRoles    []string
	changed          []string
	agreements       []Agreement
	endedMemberships []*Membership

	mu                       sync.Mutex
	previouslyRequiredChange bool
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

func (h *recordingHooks) AfterRegisterWithInvitation(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, registration *InvitedRegistration,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.invitedRegistration = registration

	return h.record(ctx, tx, "register_with_invitation")
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

func (h *recordingHooks) AfterCreateAccount(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, account *Account, membership *Membership,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.account, h.membership = account, membership

	return h.record(ctx, tx, "create_account")
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

func (h *recordingHooks) AfterArchiveAccount(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, account *Account, endedMemberships []*Membership,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.account, h.endedMemberships = account, endedMemberships

	return h.record(ctx, tx, "archive_account")
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

// The seven credential hooks. They record the user and, for the three that get
// one, the value of the column their write cleared.

func (h *recordingHooks) AfterUpdateUserPassword(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User, previouslyRequiredChange bool,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user, h.previouslyRequiredChange = user, previouslyRequiredChange

	return h.record(ctx, tx, "password")
}

func (h *recordingHooks) AfterSetUserRequiresPasswordChange(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user = user

	return h.record(ctx, tx, "requires_password_change")
}

func (h *recordingHooks) AfterUpdateUserTwoFactorSecret(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User, previousSecretVerifiedAt *time.Time,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user, h.previousVerifiedAt = user, previousSecretVerifiedAt

	return h.record(ctx, tx, "totp_secret")
}

func (h *recordingHooks) AfterMarkUserTwoFactorSecretVerified(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user = user

	return h.record(ctx, tx, "totp_verified")
}

func (h *recordingHooks) AfterSetUserEmailAddressVerificationToken(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User, previousAddressVerifiedAt *time.Time,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user, h.previousVerifiedAt = user, previousAddressVerifiedAt

	return h.record(ctx, tx, "email_token")
}

func (h *recordingHooks) AfterMarkUserEmailAddressVerified(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user = user

	return h.record(ctx, tx, "email_verified")
}

func (h *recordingHooks) AfterMarkUserEmailAddressUnverified(
	ctx context.Context, tx database.Tx, _ tenancy.Scope, user *User,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.user = user

	return h.record(ctx, tx, "email_unverified")
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

	t.Run("a registration answers with the rows and leaves the caller's values alone", func(t *testing.T) {
		t.Parallel()

		// The three writes stopped writing to what they are handed, so the
		// Registration is where a caller reads what landed. Nothing about the
		// values passed in changes, which is what makes them safe to hold on to
		// — and what makes reading an id off one a mistake the compiler cannot
		// catch but this pins.
		service, _ := env.newService(t, &recordingHooks{})

		user := newUser("ada")
		user.ID = ""

		account := newAccount("Ada's account", "")
		account.ID = ""

		registration, err := service.Register(t.Context(), testScope, user, account, []string{"account_admin"})
		must.NoError(t, err)

		test.NotEq(t, "", registration.User.ID)
		test.NotEq(t, "", registration.Account.ID)
		test.EqOp(t, registration.User.ID, registration.Account.OwnerUserID)

		test.EqOp(t, "", user.ID)
		test.EqOp(t, "", account.ID)
		test.EqOp(t, "", account.OwnerUserID)
		test.True(t, user.CreatedAt.IsZero())
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

	t.Run("registers against an invitation, answering it in the same transaction", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		sender := registerAda(t, service, "ada")

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry()))
		must.NoError(t, err)

		joiner := newUser("grace")
		joiner.EmailAddress = "grace@example.com"
		mintVerificationLink(joiner, "verify-me")

		registration, err := service.RegisterWithInvitation(t.Context(), testScope,
			joiner, issued.ID, "the-token", "glad to")
		must.NoError(t, err)

		test.NotEq(t, "", registration.User.ID)
		test.EqOp(t, "grace", registration.User.Username)
		test.False(t, registration.User.CreatedAt.IsZero())

		// The invitation was answered in the name of the user this call
		// created, which is the thing a consumer doing it in two transactions
		// cannot have.
		test.EqOp(t, InvitationAccepted, registration.Invitation.Status)
		test.EqOp(t, "glad to", registration.Invitation.StatusNote)
		must.NotNil(t, registration.Invitation.ToUser)
		test.EqOp(t, registration.User.ID, *registration.Invitation.ToUser)

		// Read back by the Service, and so redacted: the token accepted, and it
		// has been spent.
		test.EqOp(t, "", registration.Invitation.Token)
		test.EqOp(t, "", registration.Invitation.TokenDigest)

		// The account is the inviter's, the roles are the invitation's, and it
		// is the only membership this user holds — so it is where they land.
		test.EqOp(t, sender.Account.ID, registration.Membership.BelongsToAccount)
		test.EqOp(t, registration.User.ID, registration.Membership.BelongsToUser)
		test.Eq(t, []string{"account_member"}, registration.Membership.Roles)
		test.True(t, registration.Membership.DefaultAccount)

		// All of it committed, read outside the transaction that wrote it.
		user, err := store.GetUser(t.Context(), env.reader(), testScope, registration.User.ID)
		must.NoError(t, err)
		test.EqOp(t, "grace", user.Username)

		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, issued.ID)
		must.NoError(t, err)
		test.EqOp(t, InvitationAccepted, read.Status)

		membership, err := store.GetMembership(t.Context(), env.reader(),
			testScope, registration.User.ID, sender.Account.ID)
		must.NoError(t, err)
		test.True(t, membership.DefaultAccount)

		// One hook, holding the value the caller got back. Neither of the two
		// hooks this operation resembles ran: the register hook's single call
		// is the sender's own registration above, and the accept hook did not
		// run at all, because this is neither of those operations.
		test.EqOp(t, 1, hooks.ran("register_with_invitation"))
		test.EqOp(t, 1, hooks.ran("register"))
		test.EqOp(t, 0, hooks.ran("accept"))
		test.EqOp(t, registration, hooks.invitedRegistration)
	})

	t.Run("carries the verification token out and leaves a live link behind", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		sender := registerAda(t, service, "ada")

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry()))
		must.NoError(t, err)

		joiner := newUser("grace")
		mintVerificationLink(joiner, "verify-me")

		registration, err := service.RegisterWithInvitation(t.Context(), testScope,
			joiner, issued.ID, "the-token", "")
		must.NoError(t, err)

		// The secret, back out where a mail queue can reach it. No read hands
		// it back, so the user on the registration carries neither it nor its
		// digest's secret — only the digest the column holds.
		test.EqOp(t, "verify-me", registration.EmailAddressVerificationToken)
		test.EqOp(t, "", registration.User.EmailAddressVerificationToken)
		test.NotEq(t, "", registration.User.EmailAddressVerificationTokenDigest)

		// The hook is handed the same value, which is the whole reason it is a
		// field: the outbox row that mails the link is written on this
		// transaction and has nowhere else to take the secret from.
		must.NotNil(t, hooks.invitedRegistration)
		test.EqOp(t, "verify-me", hooks.invitedRegistration.EmailAddressVerificationToken)

		// And the link works, which is what "minted on the same transaction"
		// buys: the digest committed with the row it proves.
		found, err := store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "verify-me")
		must.NoError(t, err)
		test.EqOp(t, registration.User.ID, found.ID)
	})

	t.Run("a registration by invitation minting no link says so", func(t *testing.T) {
		t.Parallel()

		service, store := env.newService(t, &recordingHooks{})

		sender := registerAda(t, service, "ada")

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry()))
		must.NoError(t, err)

		// A consumer who treats the invitation itself as proof of the address
		// mints no verification token, and the empty string is the honest
		// answer rather than a value that went missing.
		registration, err := service.RegisterWithInvitation(t.Context(), testScope,
			newUser("grace"), issued.ID, "the-token", "")
		must.NoError(t, err)

		test.EqOp(t, "", registration.EmailAddressVerificationToken)
		test.EqOp(t, "", registration.User.EmailAddressVerificationTokenDigest)

		// The empty digest is how "no link outstanding" is stored, and it is
		// reachable by nobody: the read refuses an empty token outright rather
		// than matching every row that has none.
		_, err = store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "")
		must.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	t.Run("an invitation that cannot be answered leaves no user behind", func(t *testing.T) {
		t.Parallel()

		// The acceptance criterion this method exists for: the registration and
		// the answer are one transaction, so a dead invitation takes the user
		// with it rather than leaving somebody committed and unaffiliated.
		//
		// Four ways for an invitation not to admit the caller, and one
		// assertion under all of them: nobody named grace is in the directory.
		cases := []struct {
			wants   error
			prepare func(t *testing.T, service *Service, invitationID string)
			expires time.Time
			name    string
			token   string
		}{
			{
				name:    "wrong token",
				token:   "not-the-token",
				expires: futureExpiry(),
				wants:   ErrInvitationNotFound,
			},
			{
				name:    "no token at all",
				token:   "",
				expires: futureExpiry(),
				wants:   ErrInvitationNotFound,
			},
			{
				name:    "already withdrawn",
				token:   "the-token",
				expires: futureExpiry(),
				prepare: func(t *testing.T, service *Service, invitationID string) {
					t.Helper()

					_, err := service.CancelInvitation(t.Context(), testScope, invitationID, "never mind")
					must.NoError(t, err)
				},
				wants: ErrInvitationNotFound,
			},
			{
				name: "expired",
				// The service suite runs on the real clock, so an invitation
				// that has expired is one issued with its window already shut.
				token:   "the-token",
				expires: time.Now().UTC().Add(-time.Hour),
				wants:   ErrInvitationExpired,
			},
		}

		for i := range cases {
			testCase := &cases[i]

			t.Run(testCase.name, func(t *testing.T) {
				t.Parallel()

				hooks := &recordingHooks{}
				service, store := env.newService(t, hooks)

				sender := registerAda(t, service, "ada")

				issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User,
					sender.Account.ID, "grace@example.com", "the-token", testCase.expires))
				must.NoError(t, err)

				if testCase.prepare != nil {
					testCase.prepare(t, service, issued.ID)
				}

				joiner := newUser("grace")

				registration, err := service.RegisterWithInvitation(t.Context(), testScope,
					joiner, issued.ID, testCase.token, "")
				must.ErrorIs(t, err, testCase.wants)
				test.Nil(t, registration)

				// No user row survives — looked for by the username and by the
				// address, because the id the write minted landed on a copy.
				_, err = store.GetUserByUsername(t.Context(), env.reader(), testScope, "grace")
				must.ErrorIs(t, err, ErrUserNotFound)

				_, err = store.GetUserByEmailAddress(t.Context(), env.reader(), testScope, joiner.EmailAddress)
				must.ErrorIs(t, err, ErrUserNotFound)

				test.EqOp(t, 0, hooks.ran("register_with_invitation"))
			})
		}
	})

	t.Run("a failing hook rolls the registration and the answer back together", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		sender := registerAda(t, service, "ada")

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry()))
		must.NoError(t, err)

		hooks.probe = func(context.Context, database.Tx) error { return errHookRefused }

		registration, err := service.RegisterWithInvitation(t.Context(), testScope,
			newUser("grace"), issued.ID, "the-token", "glad to")
		must.ErrorIs(t, err, errHookRefused)
		test.Nil(t, registration)

		_, err = store.GetUserByUsername(t.Context(), env.reader(), testScope, "grace")
		must.ErrorIs(t, err, ErrUserNotFound)

		// The invitation is still answerable, which is the half a consumer
		// doing this in two transactions would have spent.
		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, issued.ID)
		must.NoError(t, err)
		test.EqOp(t, InvitationPending, read.Status)
	})

	t.Run("the register-with-invitation hook reads the writes it is committing with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		sender := registerAda(t, service, "ada")

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry()))
		must.NoError(t, err)

		var seen *Membership

		hooks.probe = func(ctx context.Context, tx database.Tx) error {
			// Uncommitted and readable, on the operation's own transaction.
			membership, probeErr := store.GetMembership(ctx, tx, testScope,
				hooks.invitedRegistration.User.ID, sender.Account.ID)
			seen = membership

			return probeErr
		}

		registration, err := service.RegisterWithInvitation(t.Context(), testScope,
			newUser("grace"), issued.ID, "the-token", "")
		must.NoError(t, err)

		must.NotNil(t, seen)
		test.EqOp(t, registration.Membership.ID, seen.ID)
	})

	t.Run("a registration by invitation leaves the caller's user alone", func(t *testing.T) {
		t.Parallel()

		service, _ := env.newService(t, &recordingHooks{})

		sender := registerAda(t, service, "ada")

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry()))
		must.NoError(t, err)

		joiner := newUser("grace")
		joiner.ID = ""

		registration, err := service.RegisterWithInvitation(t.Context(), testScope,
			joiner, issued.ID, "the-token", "")
		must.NoError(t, err)

		test.NotEq(t, "", registration.User.ID)
		test.EqOp(t, "", joiner.ID)
		test.True(t, joiner.CreatedAt.IsZero())
	})

	t.Run("refuses a registration by invitation with no user", func(t *testing.T) {
		t.Parallel()

		service, _ := env.newService(t, &recordingHooks{})

		_, err := service.RegisterWithInvitation(t.Context(), testScope, nil, "inv", "tok", "")
		must.ErrorIs(t, err, ErrNilUser)
	})

	t.Run("refuses a registration against an invitation that is not there", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		joiner := newUser("grace")

		_, err := service.RegisterWithInvitation(t.Context(), testScope, joiner, "nonesuch", "tok", "")
		must.ErrorIs(t, err, ErrInvitationNotFound)

		_, err = store.GetUserByUsername(t.Context(), env.reader(), testScope, "grace")
		must.ErrorIs(t, err, ErrUserNotFound)

		test.EqOp(t, 0, hooks.ran("register_with_invitation"))
	})

	t.Run("issues an invitation and hands the hook the token", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		invitation := newInvitation(registration.User, registration.Account.ID,
			"grace@example.com", "the-token", futureExpiry())
		invitation.ID = ""

		issued, err := service.Invite(t.Context(), testScope, invitation)
		must.NoError(t, err)

		// The row the write wrote, carrying what only the database could
		// settle. The caller's value is left as they assembled it.
		must.NotNil(t, issued)
		test.NotEq(t, "", issued.ID)
		test.False(t, issued.CreatedAt.IsZero())
		test.EqOp(t, "", invitation.ID)
		test.True(t, invitation.CreatedAt.IsZero())

		test.EqOp(t, 1, hooks.ran("invite"))

		// The token a hook needs to mail the link survives the read-back —
		// this is the one invitation in this package that is not redacted —
		// and the hook sees the same row the caller is handed.
		must.NotNil(t, hooks.invitation)
		test.EqOp(t, "the-token", hooks.invitation.Token)
		test.EqOp(t, "the-token", issued.Token)
		test.EqOp(t, issued.ID, hooks.invitation.ID)

		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, issued.ID)
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

		issued, err := service.Invite(t.Context(), testScope, invitation)
		must.ErrorIs(t, err, errHookRefused)

		// A refused write answers with a nil row: the sentinel never arrives
		// beside a value.
		test.Nil(t, issued)

		_, err = store.GetInvitation(t.Context(), env.reader(), testScope, invitation.ID)
		must.ErrorIs(t, err, ErrInvitationNotFound)
	})

	t.Run("refuses an invitation that is not there", func(t *testing.T) {
		t.Parallel()

		service, _ := env.newService(t, &recordingHooks{})

		_, err := service.Invite(t.Context(), testScope, nil)
		must.ErrorIs(t, err, ErrNilInvitation)
	})

	t.Run("accepts an invitation, minting the membership it promised", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		sender := registerAda(t, service, "ada")
		recipient := seedUser(t, env, store, newUser("grace"))

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			recipient.EmailAddress, "the-token", futureExpiry()))
		must.NoError(t, err)

		acceptance, err := service.AcceptInvitation(t.Context(), testScope,
			issued.ID, "the-token", recipient.ID, "glad to")
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

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			recipient.EmailAddress, "the-token", futureExpiry()))
		must.NoError(t, err)

		hooks.probe = func(context.Context, database.Tx) error { return errHookRefused }

		_, err = service.AcceptInvitation(t.Context(), testScope,
			issued.ID, "the-token", recipient.ID, "glad to")
		must.ErrorIs(t, err, errHookRefused)

		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, issued.ID)
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

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry()))
		must.NoError(t, err)

		// The ID alone answers the store's status write, and must not answer
		// this one: a rejection comes from whoever followed the link.
		_, err = service.RejectInvitation(t.Context(), testScope, issued.ID, "guessed", "no thanks")
		must.ErrorIs(t, err, ErrInvitationNotFound)
		test.EqOp(t, 0, hooks.ran("reject"))

		still, err := store.GetInvitation(t.Context(), env.reader(), testScope, issued.ID)
		must.NoError(t, err)
		test.EqOp(t, InvitationPending, still.Status)

		rejected, err := service.RejectInvitation(t.Context(), testScope,
			issued.ID, "the-token", "no thanks")
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

		issued, err := service.Invite(t.Context(), testScope, newInvitation(sender.User, sender.Account.ID,
			"grace@example.com", "the-token", futureExpiry()))
		must.NoError(t, err)

		cancelled, err := service.CancelInvitation(t.Context(), testScope, issued.ID, "hired somebody")
		must.NoError(t, err)
		test.EqOp(t, InvitationCancelled, cancelled.Status)
		test.EqOp(t, "hired somebody", cancelled.StatusNote)
		test.EqOp(t, "", cancelled.Token)
		test.EqOp(t, 1, hooks.ran("cancel"))

		_, err = service.CancelInvitation(t.Context(), testScope, issued.ID, "again")
		must.ErrorIs(t, err, ErrInvitationNotFound)
		test.EqOp(t, 1, hooks.ran("cancel"))
	})

	// The gap this closes: Store.CreateAccount existed, nothing above it did,
	// and a user could only ever create the account they registered with — so
	// every other account had to arrive by invitation, which makes starting
	// something of your own a thing only somebody else can do for you.
	t.Run("opens a second account owned by the user, without moving their default", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		owner := registerAda(t, service, "ada")

		second, err := service.CreateAccount(t.Context(), testScope, owner.User.ID,
			&Account{Name: "second household"}, []string{"owner"})
		must.NoError(t, err)
		test.EqOp(t, owner.User.ID, second.OwnerUserID)
		test.NotEq(t, owner.Account.ID, second.ID)

		// The hook ran, with both rows the call wrote.
		test.EqOp(t, 1, hooks.ran("create_account"))
		must.NotNil(t, hooks.membership)
		test.EqOp(t, second.ID, hooks.membership.BelongsToAccount)

		// The owner membership exists and is not the default: a second account
		// is somewhere a user may go, not somewhere they are moved to.
		membership, err := store.GetMembership(t.Context(), env.reader(), testScope, owner.User.ID, second.ID)
		must.NoError(t, err)
		test.False(t, membership.DefaultAccount,
			test.Sprint("opening a second account moved the user out of their first"))

		first, err := store.GetMembership(t.Context(), env.reader(), testScope, owner.User.ID, owner.Account.ID)
		must.NoError(t, err)
		test.True(t, first.DefaultAccount, test.Sprint("the first account stopped being the default"))
	})

	t.Run("refuses an account naming somebody else as owner", func(t *testing.T) {
		t.Parallel()

		service, _ := env.newService(t, &recordingHooks{})

		owner := registerAda(t, service, "ada")

		_, err := service.CreateAccount(t.Context(), testScope, owner.User.ID,
			&Account{Name: "not mine", OwnerUserID: "somebody_else"}, []string{"owner"})
		test.Error(t, err)
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

		// The row the store's archival answered with, redacted — an archival's
		// audit entry has no business carrying a password hash — and stamped,
		// which is what the Service's own read before the write could not have
		// said. It is the same value the hook is handed.
		test.EqOp(t, "", archived.HashedPassword)
		test.EqOp(t, "grace", archived.Username)
		test.True(t, archived.Archived())
		test.EqOp(t, archived, hooks.user)

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

	t.Run("archives an account with the memberships the archival ended", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		owner := registerAda(t, service, "ada")
		member := seedUserInto(t, env, store, newUser("grace"), owner.Account.ID)

		// The member belongs somewhere else too, and this account is where they
		// land — so the archival has a default to move as well as memberships to
		// end.
		elsewhere := seedAccountFor(t, env, store, member, "grace's own account")

		archived, err := service.ArchiveAccount(t.Context(), testScope, owner.Account.ID)
		must.NoError(t, err)

		// The row the store's archival answered with, stamped — which is what a
		// read before the write could not have said. It is the same value the
		// hook is handed.
		test.EqOp(t, owner.Account.ID, archived.ID)
		test.True(t, archived.Archived())
		test.EqOp(t, archived, hooks.account)

		// Both members, as they stood before the write: the owner and the
		// member, the latter still carrying the default flag this account held
		// for them. A hook told only about the account cannot strike either from
		// a roster it keeps of its own.
		test.EqOp(t, 1, hooks.ran("archive_account"))
		must.SliceLen(t, 2, hooks.endedMemberships)

		members := make([]string, 0, len(hooks.endedMemberships))
		for _, m := range hooks.endedMemberships {
			members = append(members, m.BelongsToUser)

			test.EqOp(t, owner.Account.ID, m.BelongsToAccount)
		}

		test.SliceContains(t, members, owner.User.ID)
		test.SliceContains(t, members, member.ID)

		// Committed: the account is gone, its memberships with it, and the
		// member who landed here now lands on the account they still have.
		_, err = store.GetAccount(t.Context(), env.reader(), testScope, owner.Account.ID)
		must.ErrorIs(t, err, ErrAccountNotFound)

		_, err = store.GetMembership(t.Context(), env.reader(), testScope, member.ID, owner.Account.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)

		remaining, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, member.ID)
		must.NoError(t, err)
		must.SliceLen(t, 1, remaining)
		test.EqOp(t, elsewhere.ID, remaining[0].BelongsToAccount)
		test.True(t, remaining[0].DefaultAccount,
			test.Sprint("a member stranded by an archival was left with nowhere to land"))
	})

	t.Run("hands the archival hook a roster larger than one page", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		owner := registerAda(t, service, "ada")

		// One more member than a single page holds, so the roster the hook is
		// handed can only be right if the read was drained rather than taken.
		// The page size is the filter's ceiling rather than its default, which
		// is what the archival asks for.
		perPage := int(filtering.MaxQueryFilterLimit)

		// One transaction for the lot rather than one each: two hundred and
		// fifty round trips to a real server is the difference between a case
		// that runs and a case somebody deletes.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			for i := range perPage {
				member, err := store.CreateUser(t.Context(), tx, testScope, newUser(fmt.Sprintf("member_%d", i)))
				if err != nil {
					return err
				}

				if _, err = store.CreateMembership(t.Context(), tx, testScope, &Membership{
					BelongsToUser:    member.ID,
					BelongsToAccount: owner.Account.ID,
					Roles:            []string{"account_member"},
				}); err != nil {
					return err
				}
			}

			return nil
		}))

		_, err := service.ArchiveAccount(t.Context(), testScope, owner.Account.ID)
		must.NoError(t, err)

		test.EqOp(t, 1, hooks.ran("archive_account"))
		must.SliceLen(t, perPage+1, hooks.endedMemberships)

		// Every one of them distinct, which is the other way a drain goes
		// wrong: a cursor that does not advance reads the first page forever.
		seen := make(map[string]struct{}, len(hooks.endedMemberships))
		for _, m := range hooks.endedMemberships {
			seen[m.ID] = struct{}{}
		}

		test.MapLen(t, perPage+1, seen)
	})

	t.Run("refuses to archive an account that is not there", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		_, err := service.ArchiveAccount(t.Context(), testScope, identifiers.New())
		must.ErrorIs(t, err, ErrAccountNotFound)
		test.EqOp(t, 0, hooks.ran("archive_account"))

		// And an account in the neighboring directory is absent by the same
		// answer, rather than being archived across the boundary.
		_, err = service.ArchiveAccount(t.Context(), otherScope, registration.Account.ID)
		must.ErrorIs(t, err, ErrAccountNotFound)
		test.EqOp(t, 0, hooks.ran("archive_account"))
	})

	t.Run("a failing account archival hook rolls the archival back", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		owner := registerAda(t, service, "ada")
		member := seedUserInto(t, env, store, newUser("grace"), owner.Account.ID)

		hooks.probe = func(context.Context, database.Tx) error { return errHookRefused }

		_, err := service.ArchiveAccount(t.Context(), testScope, owner.Account.ID)
		must.ErrorIs(t, err, errHookRefused)

		// The account, its memberships and the moved default all came back: the
		// fan-out and the row it belongs to are one fact.
		account, err := store.GetAccount(t.Context(), env.reader(), testScope, owner.Account.ID)
		must.NoError(t, err)
		test.False(t, account.Archived())

		membership, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, owner.Account.ID)
		must.NoError(t, err)
		test.True(t, membership.DefaultAccount)
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
			_, err := store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    joiner.User.ID,
				BelongsToAccount: owner.Account.ID,
				Roles:            []string{"member"},
			})

			return err
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
		var joiner *User

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			created, err := store.CreateUser(t.Context(), tx, testScope,
				&User{Username: "carol", EmailAddress: "carol@example.com"})
			if err != nil {
				return err
			}

			joiner = created

			// The first membership a user holds anywhere becomes their default,
			// so this is the one whose removal has to move it.
			if _, err = store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    joiner.ID,
				BelongsToAccount: first.Account.ID,
				Roles:            []string{"member"},
			}); err != nil {
				return err
			}

			_, err = store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    joiner.ID,
				BelongsToAccount: second.Account.ID,
				Roles:            []string{"member"},
			})

			return err
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
	// fifteen and again at twenty-two.
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
