package identity

import (
	"testing"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runCredentialStoreSuite covers the single-fact credential writes and the
// verification-token read, none of which a whole-User write can reach.
func runCredentialStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("verifies an email address exactly once", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "verify-me"
		seedUser(t, env, store, user)

		found, err := store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "verify-me")
		must.NoError(t, err)
		test.EqOp(t, user.ID, found.ID)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "verify-me"))

		verified, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.True(t, verified.EmailAddressVerified())
		test.EqOp(t, "", verified.EmailAddressVerificationToken)

		// The token is burned, so the link cannot be replayed.
		err = env.markUserEmailAddressVerified(t, store, testScope, user.ID, "verify-me")
		must.ErrorIs(t, err, ErrUserNotFound)

		_, err = store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "verify-me")
		must.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("refuses an empty verification token", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))

		// Every unverified user's column holds the empty string, so this query
		// would otherwise match an arbitrary one of them.
		_, err := store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "")
		must.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	t.Run("releases a forced password change on rotation", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.setUserRequiresPasswordChange(t, store, testScope, user.ID, true))

		forced, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.True(t, forced.RequiresPasswordChange)

		must.NoError(t, env.updateUserPassword(t, store, testScope, user.ID, "argon2$new"))

		// Clearing the flag on rotation is what makes a forced change
		// terminate; leaving it set prompts forever.
		rotated, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.False(t, rotated.RequiresPasswordChange)
		must.NotNil(t, rotated.PasswordLastChangedAt)
	})

	t.Run("enrolls a second factor unverified", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.updateUserTwoFactorSecret(t, store, testScope, user.ID, "SECRET"))

		enrolled, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "SECRET", enrolled.TwoFactorSecret)
		test.False(t, enrolled.TwoFactorEnabled())

		must.NoError(t, env.markUserTwoFactorSecretVerified(t, store, testScope, user.ID))

		verified, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.True(t, verified.TwoFactorEnabled())

		// Verifying twice is either a replay or a flow that lost track of
		// itself, and either is worth surfacing.
		must.ErrorIs(t, env.markUserTwoFactorSecretVerified(t, store, testScope, user.ID), ErrUserNotFound)

		// Re-enrolling drops the proof with the secret.
		must.NoError(t, env.updateUserTwoFactorSecret(t, store, testScope, user.ID, "ROTATED"))

		rotated, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.False(t, rotated.TwoFactorEnabled())
	})

	t.Run("refuses to verify a second factor nobody enrolled", func(t *testing.T) {
		t.Parallel()

		// The other conjunct of the same guard, and the one an IS NULL could
		// not have expressed: two_factor_secret is NOT NULL, so a user who
		// never enrolled holds the empty string rather than a NULL. Without the
		// not-empty half, this would stamp a proof onto a user with no secret
		// to have proved — a second factor that reads as enabled and cannot be
		// challenged.
		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.ErrorIs(t, env.markUserTwoFactorSecretVerified(t, store, testScope, user.ID), ErrUserNotFound)

		unenrolled, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.Nil(t, unenrolled.TwoFactorSecretVerifiedAt)
	})

	t.Run("issues a verification token and replaces the outstanding one", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, "tok-first"))

		found, err := store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "tok-first")
		must.NoError(t, err)
		test.EqOp(t, user.ID, found.ID)

		// Re-sending the email issues a new link, and the previous one stops
		// working — otherwise every address change leaves a live token behind.
		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, "tok-second"))

		_, err = store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "tok-first")
		must.ErrorIs(t, err, ErrUserNotFound)

		reissued, err := store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "tok-second")
		must.NoError(t, err)
		test.EqOp(t, user.ID, reissued.ID)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "tok-second"))

		verified, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.True(t, verified.EmailAddressVerified())

		// The scope is in the predicate, so the neighbor's directory reaches
		// nobody, and an unknown user is a miss rather than a silent no-op.
		must.ErrorIs(t,
			env.setUserEmailAddressVerificationToken(t, store, otherScope, user.ID, "tok-third"),
			ErrUserNotFound,
		)

		must.ErrorIs(t,
			env.setUserEmailAddressVerificationToken(t, store, tenancy.Scope{}, user.ID, "tok-third"),
			tenancy.ErrNoScope,
		)
	})

	t.Run("drops the proof when a fresh link is issued", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "verify-me"
		seedUser(t, env, store, user)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "verify-me"))

		// A row holding both a stamp and an outstanding link is two answers to
		// one question, and which one a reader believes comes down to which
		// column it consulted. Issuing a link says the address wants proving,
		// so the column saying otherwise is the one that goes.
		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, "prove-it-again"))

		reissued, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.False(t, reissued.EmailAddressVerified())
		test.EqOp(t, "prove-it-again", reissued.EmailAddressVerificationToken)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "prove-it-again"))

		reverified, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.True(t, reverified.EmailAddressVerified())
	})

	t.Run("withdraws the proof without moving the address", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "verify-me"
		seedUser(t, env, store, user)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "verify-me"))

		// A bounce, a support decision, a deliverability sweep: the address is
		// the one the user chose and stays that way, and only the proof goes.
		must.NoError(t, env.markUserEmailAddressUnverified(t, store, testScope, user.ID))

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.False(t, read.EmailAddressVerified())
		test.EqOp(t, user.EmailAddress, read.EmailAddress)

		// Unguarded, so unlike the three writes that name a value the row must
		// still hold, a second call is not a lost race — it is the same row.
		must.NoError(t, env.markUserEmailAddressUnverified(t, store, testScope, user.ID))

		// A fresh link and the proof it earns still work afterwards, which is
		// what makes this an unverify rather than a lockout.
		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, "prove-it-again"))
		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "prove-it-again"))

		reverified, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.True(t, reverified.EmailAddressVerified())
	})

	t.Run("leaves the outstanding link alone when withdrawing a proof", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, "in-the-inbox"))
		must.NoError(t, env.markUserEmailAddressUnverified(t, store, testScope, user.ID))

		// The link was minted for this address and the address has not moved,
		// so burning it would cost the user a round trip to prove the address
		// they are being asked to prove.
		found, err := store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "in-the-inbox")
		must.NoError(t, err)
		test.EqOp(t, user.ID, found.ID)
	})

	t.Run("keeps the unverify inside the directory it was asked of", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		// The scope is in the predicate, so the neighbor's directory reaches
		// nobody, and an unknown user is a miss rather than a silent no-op.
		must.ErrorIs(t,
			env.markUserEmailAddressUnverified(t, store, otherScope, user.ID),
			ErrUserNotFound,
		)

		must.ErrorIs(t,
			env.markUserEmailAddressUnverified(t, store, testScope, identifiers.New()),
			ErrUserNotFound,
		)

		must.ErrorIs(t,
			env.markUserEmailAddressUnverified(t, store, tenancy.Scope{}, user.ID),
			tenancy.ErrNoScope,
		)
	})

	t.Run("refuses to issue an empty verification token", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		// The empty string is what the column holds for every user with no
		// outstanding link. Writing it here is what makes the read side's
		// guard necessary, so the write has to refuse it too.
		must.ErrorIs(t,
			env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, ""),
			platformerrors.ErrEmptyInputParameter,
		)

		must.ErrorIs(t,
			env.markUserEmailAddressVerified(t, store, testScope, user.ID, ""),
			platformerrors.ErrEmptyInputParameter,
		)
	})

	t.Run("refuses an empty hash and an empty second factor", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		// An empty hash would be written and then compared against at the next
		// sign-in by an engine with no way to know it was never set.
		must.ErrorIs(t,
			env.updateUserPassword(t, store, testScope, user.ID, ""),
			platformerrors.ErrEmptyInputParameter,
		)

		must.ErrorIs(t,
			env.updateUserTwoFactorSecret(t, store, testScope, user.ID, ""),
			platformerrors.ErrEmptyInputParameter,
		)

		// Neither refusal wrote anything on its way out.
		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, user.HashedPassword, read.HashedPassword)
		test.EqOp(t, "", read.TwoFactorSecret)
	})
}
