package identity

import (
	"testing"

	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runProfileWriterSuite covers what a user or an account may change about
// itself, and the columns those writes must leave alone.
func runProfileWriterSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	// The profile handler every adopter writes first: take the principal's
	// user, change a name, save. What comes back from GetPrincipal is redacted,
	// so this is the case a write that validated the whole user failed — and it
	// failed by demanding the one field the caller has no business holding.
	t.Run("round-trips a redacted user", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))
		seedAccountFor(t, env, store, user, "Acme")

		principal, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, "")
		must.NoError(t, err)
		test.False(t, principal.User.HasPassword())

		principal.User.FirstName = "Augusta"
		must.NoError(t, env.updateUser(t, store, principal.User.Scope, principal.User))

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "Augusta", read.FirstName)

		// The hash the caller never had is the hash still on the row: a save
		// that accepts a redacted user must not write the redaction back.
		test.EqOp(t, "argon2$ada", read.HashedPassword)

		// The same holds for a user out of a bulk read, which is the other
		// value a consumer has to hand.
		listed, err := store.ListUsersByIDs(t.Context(), env.reader(), testScope, []string{user.ID})
		must.NoError(t, err)
		must.SliceLen(t, 1, listed)
		test.False(t, listed[0].HasPassword())

		listed[0].LastName = "King"
		must.NoError(t, env.updateUser(t, store, listed[0].Scope, listed[0]))

		again, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "King", again.LastName)
		test.EqOp(t, "argon2$ada", again.HashedPassword)
	})

	// The ruling in the package documentation, exercised: a user who never had
	// a password registers and saves their profile like anybody else.
	t.Run("registers and saves a user who has no password", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		passkeyOnly := newUser("grace")
		passkeyOnly.HashedPassword = ""
		seedUser(t, env, store, passkeyOnly)

		stored, err := store.GetUser(t.Context(), env.reader(), testScope, passkeyOnly.ID)
		must.NoError(t, err)
		test.False(t, stored.HasPassword())

		passkeyOnly.FirstName = "Grace"
		must.NoError(t, env.updateUser(t, store, passkeyOnly.Scope, passkeyOnly))

		read, err := store.GetUser(t.Context(), env.reader(), testScope, passkeyOnly.ID)
		must.NoError(t, err)
		test.EqOp(t, "Grace", read.FirstName)
		test.False(t, read.HasPassword())

		// The column has one writer, and it refuses an empty hash — so a user
		// who acquires a password cannot be walked back to none.
		must.NoError(t, env.updateUserPassword(t, store, testScope, passkeyOnly.ID, "argon2$later"))
		must.ErrorIs(t,
			env.updateUserPassword(t, store, testScope, passkeyOnly.ID, ""),
			platformerrors.ErrEmptyInputParameter,
		)

		withPassword, err := store.GetUser(t.Context(), env.reader(), testScope, passkeyOnly.ID)
		must.NoError(t, err)
		test.True(t, withPassword.HasPassword())
	})

	t.Run("updates the profile without touching credentials", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.updateUserPassword(t, store, testScope, user.ID, "argon2$rotated"))

		// A caller writing back the User it read before the rotation. The
		// profile lands; the stale hash does not.
		user.FirstName = "Augusta"
		user.HashedPassword = "argon2$stale"
		must.NoError(t, env.updateUser(t, store, user.Scope, user))

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "Augusta", read.FirstName)
		test.EqOp(t, "argon2$rotated", read.HashedPassword)
	})

	t.Run("clears verification when the email address changes", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "verify-me"
		seedUser(t, env, store, user)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "verify-me"))

		user.EmailAddress = "moved@example.com"
		must.NoError(t, env.updateUser(t, store, user.Scope, user))

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.False(t, read.EmailAddressVerified())

		// Saving again without changing the address must not re-clear anything
		// it did not have to.
		must.NoError(t, env.updateUser(t, store, user.Scope, user))
	})

	// The other half of the rule, and the half the port had to move out of the
	// statement: an unrelated profile edit must not drop a proof the user
	// already gave. Written as its own case because clearing on every save
	// would pass the test above.
	t.Run("keeps verification when the email address does not change", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		user.EmailAddressVerificationToken = "verify-me"
		seedUser(t, env, store, user)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "verify-me"))

		user.FirstName = "Augusta"
		must.NoError(t, env.updateUser(t, store, user.Scope, user))

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "Augusta", read.FirstName)
		test.True(t, read.EmailAddressVerified())
	})

	// The reproduction the fix exists for, and the direction that mattered: a
	// link mailed to the address being left behind, presented after the move.
	// The token column records that a link was mailed, not which address it went
	// to, so a token surviving the change is proof of the old address applied to
	// the new one — an address nobody ever proved, reading as verified.
	t.Run("burns the outstanding verification token when the email address changes", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, "mailed-to-ada"))

		user.EmailAddress = "unproven@example.com"
		must.NoError(t, env.updateUser(t, store, user.Scope, user))

		// The link is dead at both ends: nothing reads back by it, and
		// presenting it writes nothing.
		_, err := store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "mailed-to-ada")
		must.ErrorIs(t, err, ErrUserNotFound)

		must.ErrorIs(t,
			env.markUserEmailAddressVerified(t, store, testScope, user.ID, "mailed-to-ada"),
			ErrUserNotFound,
		)

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "unproven@example.com", read.EmailAddress)
		test.False(t, read.EmailAddressVerified())
		test.EqOp(t, "", read.EmailAddressVerificationToken)
	})

	// The mirror, and the case that fails if the token is cleared on every save
	// rather than on a move: an unrelated profile edit must not cost the user
	// the link already sitting in their inbox.
	t.Run("keeps the outstanding token when the email address does not change", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, "still-good"))

		// The struct in hand carries no token — a caller's copy is usually a
		// redacted one, and this one was never told about the link — so the
		// update has to take the token from the row rather than from the User,
		// or every profile save would burn the outstanding link.
		test.EqOp(t, "", user.EmailAddressVerificationToken)

		user.FirstName = "Augusta"
		must.NoError(t, env.updateUser(t, store, user.Scope, user))

		found, err := store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, "still-good")
		must.NoError(t, err)
		test.EqOp(t, user.ID, found.ID)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, "still-good"))

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "Augusta", read.FirstName)
		test.True(t, read.EmailAddressVerified())
	})

	t.Run("lets a user keep their own handle on update", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		user.FirstName = "Augusta"
		must.NoError(t, env.updateUser(t, store, user.Scope, user))
	})

	t.Run("refuses an update onto somebody else's handle", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))
		grace := seedUser(t, env, store, newUser("grace"))

		grace.Username = "ada"
		must.ErrorIs(t, env.updateUser(t, store, grace.Scope, grace), ErrUsernameTaken)
	})

	t.Run("records agreements", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.recordAgreement(t, store, testScope, user.ID, TermsOfService, PrivacyPolicy))

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		must.NotNil(t, read.LastAcceptedTermsOfService)
		must.NotNil(t, read.LastAcceptedPrivacyPolicy)

		must.ErrorIs(t, env.recordAgreement(t, store, testScope, user.ID), platformerrors.ErrEmptyInputParameter)
		must.ErrorIs(t,
			env.recordAgreement(t, store, testScope, user.ID, Agreement("cookies")),
			platformerrors.ErrUnrecognizedInputValue,
		)
	})

	t.Run("records one agreement without touching the other", func(t *testing.T) {
		t.Parallel()

		// Each document is its own statement now, rather than a SET list
		// assembled from the documents a caller named, so this is the case
		// that says the two do not reach each other's column.
		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.recordAgreement(t, store, testScope, user.ID, PrivacyPolicy))

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		must.NotNil(t, read.LastAcceptedPrivacyPolicy)
		test.Nil(t, read.LastAcceptedTermsOfService)

		must.NoError(t, env.recordAgreement(t, store, testScope, user.ID, TermsOfService))

		both, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		must.NotNil(t, both.LastAcceptedTermsOfService)

		// The second call did not move the first acceptance.
		test.EqOp(t, *read.LastAcceptedPrivacyPolicy, *both.LastAcceptedPrivacyPolicy)
	})

	t.Run("refuses an agreement for a user it cannot see", func(t *testing.T) {
		t.Parallel()

		// Every statement is keyed on the scope, so a write aimed at another
		// directory's user touches no row — and a write that touched no row is
		// the entity not being there rather than a success.
		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.ErrorIs(t,
			env.recordAgreement(t, store, otherScope, user.ID, TermsOfService, PrivacyPolicy),
			ErrUserNotFound,
		)

		must.ErrorIs(t,
			env.recordAgreement(t, store, testScope, "not-a-user", TermsOfService),
			ErrUserNotFound,
		)

		// Nothing was written on the way to the refusal: the documents are one
		// statement each inside one transaction, so a refusal on either rolls
		// the other back.
		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.Nil(t, read.LastAcceptedTermsOfService)
		test.Nil(t, read.LastAcceptedPrivacyPolicy)
	})

	t.Run("writes name and address but not billing or owner", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		other := seedUser(t, env, store, newUser("grace"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		must.NoError(t, env.setAccountBillingStatus(t, store, testScope, account.ID, BillingPaid))

		// A caller writing back the Account it read before the webhook landed.
		// The name lands; the stale billing status and a substituted owner do
		// not.
		account.Name = "Acme Ltd"
		account.BillingAddress = BillingAddress{Line1: "1 High St", City: "London", Country: "GB"}
		account.BillingStatus = BillingUnpaid
		account.OwnerUserID = other.ID

		must.NoError(t, env.updateAccount(t, store, account.Scope, account))

		read, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, "Acme Ltd", read.Name)
		test.EqOp(t, "1 High St", read.BillingAddress.Line1)
		test.EqOp(t, BillingPaid, read.BillingStatus)
		test.EqOp(t, owner.ID, read.OwnerUserID)
	})

	t.Run("round-trips a time zone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		// An account with none stated reads back as none stated rather than as
		// whatever the database or the driver would default to.
		test.EqOp(t, "", account.TimeZone)

		account.TimeZone = "America/Chicago"
		must.NoError(t, env.updateAccount(t, store, account.Scope, account))

		read, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, "America/Chicago", read.TimeZone)

		loc, err := read.Location()
		must.NoError(t, err)
		test.EqOp(t, "America/Chicago", loc.String())
	})

	t.Run("refuses a nil user and a nil account", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Both writes read the scope off the value they were handed, so a nil
		// one has to be refused rather than dereferenced for a predicate.
		must.ErrorIs(t, env.updateUser(t, store, testScope, nil), ErrNilUser)
		must.ErrorIs(t, env.updateAccount(t, store, testScope, nil), ErrNilAccount)
	})
}
