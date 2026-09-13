package identity

import (
	"context"
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runCredentialServiceSuite covers the Service's seven credential operations:
// the transaction each opens, the hook it calls inside it, and what that hook is
// told about the column the write cleared.
//
// It is a suite of its own rather than more cases in the service suite because
// what it is about is one group of operations — the ones a flow with no current
// password to offer reaches for, which is where the hook used to be missing.
func runCredentialServiceSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("writes a password, releases the forced change, and says it was forced", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))
		must.NoError(t, env.setUserRequiresPasswordChange(t, store, testScope, user.ID, true))

		updated, err := service.UpdateUserPassword(t.Context(), testScope, user.ID, "argon2$reset")
		must.NoError(t, err)

		// The reset released the requirement it was made under, and the row
		// says when the credential moved.
		test.False(t, updated.RequiresPasswordChange)
		must.NotNil(t, updated.PasswordLastChangedAt)

		// Redacted, so the hash the caller just supplied is not handed back to
		// them a second time.
		test.EqOp(t, "", updated.HashedPassword)

		test.EqOp(t, 1, hooks.ran("password"))
		test.EqOp(t, updated, hooks.user)

		// The one thing the hook could not have read for itself: the flag this
		// write cleared on its way past.
		test.True(t, hooks.previouslyRequiredChange)

		// Committed, read outside the transaction that wrote it.
		stored, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "argon2$reset", stored.HashedPassword)
		test.False(t, stored.RequiresPasswordChange)
	})

	t.Run("a rotation nobody forced says so", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))

		_, err := service.UpdateUserPassword(t.Context(), testScope, user.ID, "argon2$chosen")
		must.NoError(t, err)

		test.EqOp(t, 1, hooks.ran("password"))
		test.False(t, hooks.previouslyRequiredChange)
	})

	t.Run("refuses an empty hash and runs no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))

		_, err := service.UpdateUserPassword(t.Context(), testScope, user.ID, "")
		must.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)

		test.EqOp(t, 0, hooks.ran("password"))

		stored, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, user.HashedPassword, stored.HashedPassword)
	})

	t.Run("a failing password hook rolls the write back", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{probe: func(context.Context, database.Tx) error { return errHookRefused }}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))

		_, err := service.UpdateUserPassword(t.Context(), testScope, user.ID, "argon2$reset")
		must.ErrorIs(t, err, errHookRefused)

		// The whole bargain: a credential a consumer could not record is a
		// credential that did not change.
		stored, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, user.HashedPassword, stored.HashedPassword)
		must.Nil(t, stored.PasswordLastChangedAt)
	})

	t.Run("a credential hook reads the write it is committing with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("grace"))

		var seen *User

		hooks.probe = func(ctx context.Context, tx database.Tx) error {
			// Uncommitted, and readable, which is the property the seam rests
			// on — and the reason a hook can write an outbox row that
			// references what it sees.
			found, err := store.GetUser(ctx, tx, testScope, user.ID)
			seen = found

			return err
		}

		_, err := service.UpdateUserPassword(t.Context(), testScope, user.ID, "argon2$reset")
		must.NoError(t, err)

		must.NotNil(t, seen)
		test.EqOp(t, "argon2$reset", seen.HashedPassword)
	})

	t.Run("forces and releases a password change", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))

		forced, err := service.SetUserRequiresPasswordChange(t.Context(), testScope, user.ID, true)
		must.NoError(t, err)
		test.True(t, forced.RequiresPasswordChange)

		test.EqOp(t, 1, hooks.ran("requires_password_change"))
		test.EqOp(t, forced, hooks.user)

		// The same call withdraws it, which is not the release a completed
		// password change performs: there is no new credential behind this one.
		released, err := service.SetUserRequiresPasswordChange(t.Context(), testScope, user.ID, false)
		must.NoError(t, err)
		test.False(t, released.RequiresPasswordChange)

		test.EqOp(t, 2, hooks.ran("requires_password_change"))

		stored, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.False(t, stored.RequiresPasswordChange)
	})

	t.Run("refuses to force a change on a user who is not there", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		_, err := service.SetUserRequiresPasswordChange(t.Context(), testScope, identifiers.New(), true)
		must.ErrorIs(t, err, ErrUserNotFound)

		test.EqOp(t, 0, hooks.ran("requires_password_change"))
	})

	t.Run("stores a second-factor secret unverified and reports the proof it dropped", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))

		// A proven second factor, put in place through the store so that the
		// operation under test is the one that replaces it.
		must.NoError(t, env.updateUserTwoFactorSecret(t, store, testScope, user.ID, "seedsecret"))

		proven, err := env.markUserTwoFactorSecretVerified(t, store, testScope, user.ID)
		must.NoError(t, err)
		must.NotNil(t, proven.TwoFactorSecretVerifiedAt)

		updated, err := service.UpdateUserTwoFactorSecret(t.Context(), testScope, user.ID, "freshsecret")
		must.NoError(t, err)

		// The new secret is unproven, so the user holds no second factor until
		// they demonstrate possession of it.
		must.Nil(t, updated.TwoFactorSecretVerifiedAt)
		test.False(t, updated.TwoFactorEnabled())

		// And the secret itself is neither returned nor handed to the hook.
		test.EqOp(t, "", updated.TwoFactorSecret)

		test.EqOp(t, 1, hooks.ran("totp_secret"))
		test.EqOp(t, updated, hooks.user)

		must.NotNil(t, hooks.previousVerifiedAt)
		test.EqOp(t, *proven.TwoFactorSecretVerifiedAt, *hooks.previousVerifiedAt)

		stored, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "freshsecret", stored.TwoFactorSecret)
	})

	t.Run("a first enrollment reports no previous proof", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))

		_, err := service.UpdateUserTwoFactorSecret(t.Context(), testScope, user.ID, "freshsecret")
		must.NoError(t, err)

		test.EqOp(t, 1, hooks.ran("totp_secret"))

		// Nil rather than a zero time: nobody ever proved the secret this one
		// replaced, which is the ordinary enrollment and not the event an alert
		// on "a proven second factor was replaced" is looking for.
		must.Nil(t, hooks.previousVerifiedAt)
	})

	t.Run("marks a second-factor secret verified, once", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))
		must.NoError(t, env.updateUserTwoFactorSecret(t, store, testScope, user.ID, "seedsecret"))

		verified, err := service.MarkUserTwoFactorSecretVerified(t.Context(), testScope, user.ID)
		must.NoError(t, err)

		// The stamp is the one this write made, which is the moment being
		// recorded.
		must.NotNil(t, verified.TwoFactorSecretVerifiedAt)
		test.EqOp(t, "", verified.TwoFactorSecret)

		test.EqOp(t, 1, hooks.ran("totp_verified"))
		test.EqOp(t, verified, hooks.user)

		// A replay matches nothing, so the timestamp does not move and no
		// second record is written.
		_, err = service.MarkUserTwoFactorSecretVerified(t.Context(), testScope, user.ID)
		must.ErrorIs(t, err, ErrUserNotFound)

		test.EqOp(t, 1, hooks.ran("totp_verified"))

		stored, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		must.NotNil(t, stored.TwoFactorSecretVerifiedAt)
		test.EqOp(t, *verified.TwoFactorSecretVerifiedAt, *stored.TwoFactorSecretVerifiedAt)
	})

	t.Run("mints a verification token and reports the proof it dropped", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "first-link"
		seedUser(t, env, store, user)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "first-link"))

		proven, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		must.NotNil(t, proven.EmailAddressVerifiedAt)

		updated, err := service.SetUserEmailAddressVerificationToken(t.Context(), testScope, user.ID, "second-link")
		must.NoError(t, err)

		// The row may not say both "proven" and "a link is outstanding", and
		// issuing the link is the statement that the address wants proving.
		must.Nil(t, updated.EmailAddressVerifiedAt)
		test.EqOp(t, "", updated.EmailAddressVerificationToken)

		test.EqOp(t, 1, hooks.ran("email_token"))
		test.EqOp(t, updated, hooks.user)

		must.NotNil(t, hooks.previousVerifiedAt)
		test.EqOp(t, *proven.EmailAddressVerifiedAt, *hooks.previousVerifiedAt)

		stored, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "second-link", stored.EmailAddressVerificationToken)
	})

	t.Run("a link for an address nobody proved reports no previous proof", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))

		_, err := service.SetUserEmailAddressVerificationToken(t.Context(), testScope, user.ID, "first-link")
		must.NoError(t, err)

		test.EqOp(t, 1, hooks.ran("email_token"))
		must.Nil(t, hooks.previousVerifiedAt)
	})

	t.Run("refuses an empty token and runs no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := seedUser(t, env, store, newUser("ada"))

		// The empty string is how "no outstanding link" is stored, so writing
		// it would be a clear dressed as an issue.
		_, err := service.SetUserEmailAddressVerificationToken(t.Context(), testScope, user.ID, "")
		must.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)

		test.EqOp(t, 0, hooks.ran("email_token"))
	})

	t.Run("verifies an address once, and hands the hook the address proved", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "verify-me"
		seedUser(t, env, store, user)

		verified, err := service.MarkUserEmailAddressVerified(t.Context(), testScope, user.ID, "verify-me")
		must.NoError(t, err)

		test.True(t, verified.EmailAddressVerified())

		// The column says when something was proven and never what; the row
		// the hook is handed says which address it was.
		test.EqOp(t, user.EmailAddress, verified.EmailAddress)
		test.EqOp(t, "", verified.EmailAddressVerificationToken)

		test.EqOp(t, 1, hooks.ran("email_verified"))
		test.EqOp(t, verified, hooks.user)

		// The token is burned, so a second click on the same link writes
		// nothing and records nothing.
		_, err = service.MarkUserEmailAddressVerified(t.Context(), testScope, user.ID, "verify-me")
		must.ErrorIs(t, err, ErrUserNotFound)

		test.EqOp(t, 1, hooks.ran("email_verified"))
	})

	t.Run("a failing verification hook leaves the address unproven", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{probe: func(context.Context, database.Tx) error { return errHookRefused }}
		service, store := env.newService(t, hooks)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "verify-me"
		seedUser(t, env, store, user)

		_, err := service.MarkUserEmailAddressVerified(t.Context(), testScope, user.ID, "verify-me")
		must.ErrorIs(t, err, errHookRefused)

		// The token survives with the proof, so the link the person was mailed
		// still works.
		stored, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.False(t, stored.EmailAddressVerified())
		test.EqOp(t, "verify-me", stored.EmailAddressVerificationToken)
	})

	t.Run("withdraws a proof and leaves an outstanding link alone", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "verify-me"
		seedUser(t, env, store, user)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "verify-me"))

		// A second link, minted after the proof, which is the state an
		// administrative unverify has to leave alone.
		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, "next-link"))
		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "next-link"))
		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, "third-link"))

		unverified, err := service.MarkUserEmailAddressUnverified(t.Context(), testScope, user.ID)
		must.NoError(t, err)

		test.False(t, unverified.EmailAddressVerified())
		test.EqOp(t, user.EmailAddress, unverified.EmailAddress)
		test.EqOp(t, "", unverified.EmailAddressVerificationToken)

		test.EqOp(t, 1, hooks.ran("email_unverified"))
		test.EqOp(t, unverified, hooks.user)

		stored, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.False(t, stored.EmailAddressVerified())
		test.EqOp(t, "third-link", stored.EmailAddressVerificationToken)
	})

	t.Run("every credential operation refuses a user in another directory", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		service, store := env.newService(t, hooks)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "verify-me"
		seedUser(t, env, store, user)
		must.NoError(t, env.updateUserTwoFactorSecret(t, store, testScope, user.ID, "seedsecret"))

		// Every operation binds the scope, so a caller naming the neighbor's
		// directory reaches nobody rather than somebody else's credential.
		calls := []struct {
			run  func() error
			name string
		}{
			{name: "password", run: func() error {
				_, err := service.UpdateUserPassword(t.Context(), otherScope, user.ID, "argon2$reset")

				return err
			}},
			{name: "requires_password_change", run: func() error {
				_, err := service.SetUserRequiresPasswordChange(t.Context(), otherScope, user.ID, true)

				return err
			}},
			{name: "totp_secret", run: func() error {
				_, err := service.UpdateUserTwoFactorSecret(t.Context(), otherScope, user.ID, "freshsecret")

				return err
			}},
			{name: "totp_verified", run: func() error {
				_, err := service.MarkUserTwoFactorSecretVerified(t.Context(), otherScope, user.ID)

				return err
			}},
			{name: "email_token", run: func() error {
				_, err := service.SetUserEmailAddressVerificationToken(t.Context(), otherScope, user.ID, "link")

				return err
			}},
			{name: "email_verified", run: func() error {
				_, err := service.MarkUserEmailAddressVerified(t.Context(), otherScope, user.ID, "verify-me")

				return err
			}},
			{name: "email_unverified", run: func() error {
				_, err := service.MarkUserEmailAddressUnverified(t.Context(), otherScope, user.ID)

				return err
			}},
		}

		// The seven, counted against the hooks the group declares, so an
		// operation added here without a case fails as a count rather than as
		// an absence.
		test.SliceLen(t, 7, calls)

		for _, call := range calls {
			t.Run(call.name, func(t *testing.T) {
				t.Parallel()

				must.ErrorIs(t, call.run(), ErrUserNotFound)
				test.EqOp(t, 0, hooks.ran(call.name))
			})
		}
	})
}
