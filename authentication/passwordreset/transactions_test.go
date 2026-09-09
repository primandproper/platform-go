package passwordreset

import (
	"database/sql"
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// callerPasswordsDDL is the caller's own table.
//
// It stands in for the thing this package cannot see and exists to protect: the
// password a redemption authorizes, written by whatever store owns users. The
// only assertions that can be made about the seam are made across two tables,
// so these tests need a second one.
//
// It is raw SQL in a test, which this package allows itself in exactly the
// places where the SQL is the subject rather than the tool.
const callerPasswordsDDL = `CREATE TABLE caller_passwords (
	belongs_to_user TEXT NOT NULL PRIMARY KEY,
	password_hash   TEXT NOT NULL
)`

// errCallerWrite is what a caller's own failure looks like from inside the
// callback, for the cases where the failure is the point rather than the
// mechanism that produced it.
var errCallerWrite = platformerrors.New("the caller's own write failed")

// newCallerStore builds a store beside a caller's password table.
func newCallerStore(t *testing.T) *SQLStore {
	t.Helper()

	store, _ := newTestStore(t)

	_, err := store.db.Writer().ExecContext(t.Context(), callerPasswordsDDL)
	must.NoError(t, err)

	return store
}

// setPassword writes the caller's row outside any transaction, for the tests
// that need a password to already be there.
func setPassword(t *testing.T, store *SQLStore, userID, hash string) {
	t.Helper()

	_, err := store.db.Writer().ExecContext(t.Context(),
		"INSERT INTO caller_passwords (belongs_to_user, password_hash) VALUES (?, ?)", userID, hash)
	must.NoError(t, err)
}

// passwordFor reads the caller's row back, reporting whether there is one.
func passwordFor(t *testing.T, store *SQLStore, userID string) (hash string, found bool) {
	t.Helper()

	err := store.db.Writer().QueryRowContext(t.Context(),
		"SELECT password_hash FROM caller_passwords WHERE belongs_to_user = ?", userID).Scan(&hash)
	if stderrors.Is(err, sql.ErrNoRows) {
		return "", false
	}

	must.NoError(t, err)

	return hash, true
}

// TestSQLStore_callerTransaction is the seam this package's writes were ported
// for: the redemption and the password it authorizes reach the database as one
// fact or as none.
//
// Nothing here is about SQL. Every case is about the commit boundary, and about
// which side of it each of the two writes ends up on — which is the question the
// store used to answer for its caller by opening a transaction of its own, and
// answered wrong.
func TestSQLStore_callerTransaction(T *testing.T) {
	T.Parallel()

	// The flow the package exists for, start to finish, on one commit: the page
	// load's read, the redemption, the password, and the revocation of whatever
	// else was outstanding.
	T.Run("verify, consume and the caller's own write land on one commit", func(t *testing.T) {
		t.Parallel()

		store := newCallerStore(t)
		issuance := issue(t, store, time.Hour)
		superseded := issue(t, store, time.Hour)

		var token *Token

		must.NoError(t, withTx(t, store, func(tx database.Tx) error {
			// The read runs on the transaction that is about to spend the
			// token, which is what the wider executor type is for.
			if _, verifyErr := store.Verify(t.Context(), tx, testScope(), issuance.Secret); verifyErr != nil {
				return verifyErr
			}

			var consumeErr error
			if token, consumeErr = store.Consume(t.Context(), tx, testScope(), issuance.Secret); consumeErr != nil {
				return consumeErr
			}

			if _, execErr := tx.ExecContext(t.Context(),
				"INSERT INTO caller_passwords (belongs_to_user, password_hash) VALUES (?, ?)",
				token.UserID, "argon2-of-the-new-one",
			); execErr != nil {
				return execErr
			}

			_, revokeErr := store.RevokeForUser(t.Context(), tx, testScope(), token.UserID)

			return revokeErr
		}))

		must.NotNil(t, token)
		must.NotNil(t, token.RedeemedAt)

		hash, found := passwordFor(t, store, testUserID)
		test.True(t, found)
		test.EqOp(t, "argon2-of-the-new-one", hash)

		// The spent link says it was spent, and the one it superseded is gone.
		_, err := verify(t, store, testScope(), issuance.Secret)
		test.ErrorIs(t, err, ErrTokenRedeemed)

		_, err = verify(t, store, testScope(), superseded.Secret)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	// The half that is a vulnerability rather than a bookkeeping error. A
	// redemption that committed on its own would leave this user holding a spent
	// link and the password they could not change — and the store, having
	// decided in its own transaction, would have no way to know.
	T.Run("the caller's failed write takes the redemption back with it", func(t *testing.T) {
		t.Parallel()

		store := newCallerStore(t)
		setPassword(t, store, testUserID, "argon2-of-the-old-one")

		issuance := issue(t, store, time.Hour)

		// The primary key already holds this user, so the reset's write
		// collides — a failing password write, made of a real constraint rather
		// than a returned sentinel.
		err := withTx(t, store, func(tx database.Tx) error {
			if _, consumeErr := store.Consume(t.Context(), tx, testScope(), issuance.Secret); consumeErr != nil {
				return consumeErr
			}

			_, execErr := tx.ExecContext(t.Context(),
				"INSERT INTO caller_passwords (belongs_to_user, password_hash) VALUES (?, ?)",
				testUserID, "argon2-of-the-new-one")

			return execErr
		})
		test.Error(t, err)

		hash, found := passwordFor(t, store, testUserID)
		test.True(t, found)
		test.EqOp(t, "argon2-of-the-old-one", hash)

		// And the link is still spendable, so the user can try again rather
		// than having to ask for a second email.
		token, consumeErr := consume(t, store, testScope(), issuance.Secret)
		must.NoError(t, consumeErr)
		must.NotNil(t, token)
	})

	// The read shape's reason for being the wider type. A caller that has just
	// spent a token and reads it back in the same transaction has to see its own
	// redemption; a read pinned to a committed view would report the token live
	// and invite a second spend of it.
	T.Run("a verify on the transaction sees the redemption it just made", func(t *testing.T) {
		t.Parallel()

		store := newCallerStore(t)
		issuance := issue(t, store, time.Hour)

		var seen error

		must.NoError(t, withTx(t, store, func(tx database.Tx) error {
			if _, consumeErr := store.Consume(t.Context(), tx, testScope(), issuance.Secret); consumeErr != nil {
				return consumeErr
			}

			_, seen = store.Verify(t.Context(), tx, testScope(), issuance.Secret)

			return nil
		}))

		test.ErrorIs(t, seen, ErrTokenRedeemed)
	})

	// Issue returns the secret before the row is durable, which is the one place
	// this shape asks something of the caller: mail the link after the commit.
	T.Run("an issuance rolls back with the caller that asked for it", func(t *testing.T) {
		t.Parallel()

		store := newCallerStore(t)

		var issuance *Issuance

		err := withTx(t, store, func(tx database.Tx) error {
			var issueErr error
			if issuance, issueErr = store.Issue(t.Context(), tx, testScope(), testUserID, time.Hour); issueErr != nil {
				return issueErr
			}

			return errCallerWrite
		})
		test.ErrorIs(t, err, errCallerWrite)

		// The secret was handed back and the row was not, so a link mailed from
		// inside that callback would have been a reset nobody could complete.
		must.NotNil(t, issuance)
		test.NotEqOp(t, "", issuance.Secret)

		_, verifyErr := verify(t, store, testScope(), issuance.Secret)
		test.ErrorIs(t, verifyErr, ErrTokenNotFound)
	})

	// The companion write, and the reason it is in the same transaction: a
	// revocation that landed while the reset it belonged to did not would stop
	// links working for a password that never changed.
	T.Run("a revocation rolls back with the reset it belonged to", func(t *testing.T) {
		t.Parallel()

		store := newCallerStore(t)
		outstanding := issue(t, store, time.Hour)

		err := withTx(t, store, func(tx database.Tx) error {
			revoked, revokeErr := store.RevokeForUser(t.Context(), tx, testScope(), testUserID)
			if revokeErr != nil {
				return revokeErr
			}

			test.EqOp(t, int64(1), revoked)

			return errCallerWrite
		})
		test.ErrorIs(t, err, errCallerWrite)

		_, verifyErr := verify(t, store, testScope(), outstanding.Secret)
		test.NoError(t, verifyErr)
	})
}
