package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database/ddl"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestTokenDigest pins the function every token in this schema goes through.
//
// The expected values are computed with crypto/sha256 rather than with
// tokenDigest itself, which would assert only that the function is
// deterministic — true of returning the argument unchanged, which is the bug
// this exists to catch.
func TestTokenDigest(T *testing.T) {
	T.Parallel()

	T.Run("is the hex SHA-256 of the token", func(t *testing.T) {
		t.Parallel()

		const token = "prove-it"

		sum := sha256.Sum256([]byte(token))

		test.EqOp(t, hex.EncodeToString(sum[:]), tokenDigest(token))
		test.StrNotEqFold(t, token, tokenDigest(token))
		test.EqOp(t, 64, len(tokenDigest(token)))
	})

	T.Run("passes the empty string through", func(t *testing.T) {
		t.Parallel()

		// It is not a token: it is how "no outstanding link" is stored, and
		// what the verification index's partial clause tests for. A digest of
		// it would give every user in the directory a link nobody minted.
		test.EqOp(t, "", tokenDigest(""))
	})

	T.Run("two tokens do not share a digest", func(t *testing.T) {
		t.Parallel()

		test.NotEqOp(t, tokenDigest("first-link"), tokenDigest("second-link"))
	})
}

// runTokenDigestSuite reads the two token columns as the database holds them.
//
// It is the one suite here that runs SQL of its own rather than going through
// the Store, and that is the point: every other case can only see what the
// store hands back, so a store that dutifully returned a digest while writing
// the raw token to the column would pass all of them. What is asserted is a
// property of the row, so the row is what it reads.
//
// The column names are spelled here rather than taken from a constant for the
// same reason. A rename that took the constant with it would carry this test
// along; a literal means the schema and the assertion can disagree.
func runTokenDigestSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	// column reads the single row of a prefixed table, which is exactly what
	// every case below has just written. No predicate, so no placeholder, so
	// one statement text across all three dialects.
	column := func(t *testing.T, ctx context.Context, prefix, table, name string) string {
		t.Helper()

		var value string

		query := fmt.Sprintf("SELECT %s FROM %s%s", name, ddl.Qualify(prefix), table)
		must.NoError(t, env.reader().QueryRowContext(ctx, query).Scan(&value),
			must.Sprintf("reading %q", query))

		return value
	}

	t.Run("the verification token column holds a digest", func(t *testing.T) {
		t.Parallel()

		const token = "mailed-to-ada"

		prefix := env.migrate(t)

		store, err := NewSQLStore(env.client, WithTablePrefix(prefix))
		must.NoError(t, err)

		user := seedUser(t, env, store, newUser("ada"))
		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, user.ID, token))

		stored := column(t, t.Context(), prefix, "identity_users", "email_address_verification_token_digest")

		// The whole of the issue: a backup, a replica, or a support engineer's
		// query must not be a verification for every outstanding address.
		test.StrNotEqFold(t, token, stored,
			test.Sprint("the verification token is stored in the clear"))
		test.EqOp(t, tokenDigest(token), stored)

		// And the link still works, which is what makes the digest a change of
		// storage rather than of behavior.
		found, err := store.GetUserByEmailVerificationToken(t.Context(), env.reader(), testScope, token)
		must.NoError(t, err)
		test.EqOp(t, user.ID, found.ID)

		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, user.ID, token))

		// Burnt, and burnt to the empty string rather than to a digest of one:
		// the partial index is on the column being non-empty, and a user with
		// no outstanding link has to fall out of it.
		test.EqOp(t, "", column(t, t.Context(), prefix, "identity_users",
			"email_address_verification_token_digest"))
	})

	t.Run("the invitation token column holds a digest", func(t *testing.T) {
		t.Parallel()

		const token = "tok-mailed"

		prefix := env.migrate(t)

		store, err := NewSQLStore(env.client, WithTablePrefix(prefix), WithClock(newFixedClock(baseTime)))
		must.NoError(t, err)

		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		created, err := env.createInvitation(t, store, testScope,
			newInvitation(owner, account.ID, "brian@example.com", token, baseTime.Add(time.Hour)))
		must.NoError(t, err)

		stored := column(t, t.Context(), prefix, "identity_invitations", "token_digest")

		test.StrNotEqFold(t, token, stored,
			test.Sprint("the invitation token is stored in the clear"))
		test.EqOp(t, tokenDigest(token), stored)

		// The create's answer carries the secret anyway, because that is what
		// the invitation exists to mail — and it does not come off the row.
		test.EqOp(t, token, created.Token)
		test.EqOp(t, stored, created.TokenDigest)

		// A read is not a way back to it, and the link still redeems.
		read, err := store.GetInvitation(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, "", read.Token)

		byToken, err := store.GetInvitationByToken(t.Context(), env.reader(), testScope, created.ID, token)
		must.NoError(t, err)
		test.EqOp(t, created.ID, byToken.ID)

		// A guess at the digest is not a token: presenting what the column
		// holds is refused like any other wrong value.
		_, err = store.GetInvitationByToken(t.Context(), env.reader(), testScope, created.ID, stored)
		must.ErrorIs(t, err, ErrInvitationNotFound)
	})
}
