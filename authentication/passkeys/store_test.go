package passkeys

import (
	"errors"
	"slices"
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore_SQLite runs the behavioral suite against SQLite, which needs no
// container. The same suite runs against real servers in containers_test.go.
func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is every behavior this store promises, against whatever database
// the environment holds.
//
// It is one function rather than a file of top-level tests because it runs three
// times, on three dialects. A case that lived outside it would be a case one
// dialect is checked for and the other two are not, which is the failure mode
// this module least wants: the bug whose symptom depends on which database the
// deployment happens to run.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("create answers with the row it wrote", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		written := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0xA1, 0xB2}))

		test.NotEqOp(t, "", written.ID)
		test.EqOp(t, testScope, written.Scope)
		test.EqOp(t, "user_1", written.BelongsToUser)
		test.Eq(t, []byte{0xA1, 0xB2}, written.CredentialID)
		test.Eq(t, []string{"internal", "hybrid"}, written.Transports)
		test.False(t, written.CreatedAt.IsZero())

		// A freshly registered passkey has verified nothing. The column is what
		// makes "enrolled and never used" answerable, so the create leaves it
		// alone rather than stamping the registration into it.
		test.Nil(t, written.LastUsedAt)
		test.Nil(t, written.ArchivedAt)
	})

	t.Run("create does not write to the caller's argument", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		argument := newCredential("user_1", []byte{0x01})
		written := env.mustCreate(t, store, testScope, argument)

		test.EqOp(t, "", argument.ID)
		test.EqOp(t, tenancy.Scope{}, argument.Scope)
		test.True(t, argument.CreatedAt.IsZero())
		test.NotEqOp(t, argument.ID, written.ID)
	})

	t.Run("a credential naming no scope adopts the write's", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		written := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x02}))
		test.EqOp(t, testScope, written.Scope)
	})

	t.Run("a credential naming another scope is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		elsewhere := newCredential("user_1", []byte{0x03})
		elsewhere.Scope = otherScope

		_, err := env.create(t, store, testScope, elsewhere)
		must.ErrorIs(t, err, ErrScopeMismatch)
	})

	t.Run("a credential naming the write's own scope is accepted", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		agreeing := newCredential("user_1", []byte{0x04})
		agreeing.Scope = testScope

		written := env.mustCreate(t, store, testScope, agreeing)
		test.EqOp(t, testScope, written.Scope)
	})

	t.Run("a live credential id cannot be registered twice", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		credentialID := []byte{0x10, 0x11}
		env.mustCreate(t, store, testScope, newCredential("user_1", credentialID))

		_, err := env.create(t, store, testScope, newCredential("user_2", credentialID))
		must.ErrorIs(t, err, ErrCredentialRegistered)
	})

	t.Run("another scope may enroll the same authenticator", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		credentialID := []byte{0x20, 0x21}
		env.mustCreate(t, store, testScope, newCredential("user_1", credentialID))

		written := env.mustCreate(t, store, otherScope, newCredential("user_1", credentialID))
		test.EqOp(t, otherScope, written.Scope)
	})

	t.Run("a revoked credential id is free again", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		credentialID := []byte{0x30, 0x31}
		first := env.mustCreate(t, store, testScope, newCredential("user_1", credentialID))
		env.mustArchive(t, store, testScope, first.ID, "user_1")

		// The whole reason the unique index is partial. A plain UNIQUE would
		// refuse this forever, and the person would be told their own
		// authenticator is already registered to a passkey they cannot see.
		second := env.mustCreate(t, store, testScope, newCredential("user_1", credentialID))
		test.NotEqOp(t, first.ID, second.ID)
	})

	t.Run("the login's read finds the live row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		credentialID := []byte{0x40}
		written := env.mustCreate(t, store, testScope, newCredential("user_1", credentialID))

		found, err := store.GetCredentialByCredentialID(t.Context(), env.reader(), testScope, credentialID)
		must.NoError(t, err)
		test.EqOp(t, written.ID, found.ID)
		test.Eq(t, []byte{0x01, 0x02, 0x03}, found.PublicKey)
	})

	t.Run("the login's read does not cross scopes", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		credentialID := []byte{0x41}
		env.mustCreate(t, store, testScope, newCredential("user_1", credentialID))

		_, err := store.GetCredentialByCredentialID(t.Context(), env.reader(), otherScope, credentialID)
		must.ErrorIs(t, err, ErrCredentialNotFound)
	})

	t.Run("a revoked passkey verifies nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		credentialID := []byte{0x42}
		written := env.mustCreate(t, store, testScope, newCredential("user_1", credentialID))
		env.mustArchive(t, store, testScope, written.ID, "user_1")

		_, err := store.GetCredentialByCredentialID(t.Context(), env.reader(), testScope, credentialID)
		must.ErrorIs(t, err, ErrCredentialNotFound)
	})

	t.Run("a user's passkeys come back oldest first", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x50}))
		second := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x51}))
		env.mustCreate(t, store, testScope, newCredential("user_2", []byte{0x52}))
		env.mustCreate(t, store, otherScope, newCredential("user_1", []byte{0x53}))

		found, err := store.GetCredentialsForUser(t.Context(), env.reader(), testScope, "user_1")
		must.NoError(t, err)
		must.SliceLen(t, 2, found)

		// created_at is the leading ordering term and the id breaks the tie,
		// which is what makes this deterministic on a dialect whose timestamps
		// have second resolution: two passkeys enrolled in the same second come
		// back in id order rather than in whichever order the engine scanned.
		ids := []string{found[0].ID, found[1].ID}
		test.SliceContains(t, ids, first.ID)
		test.SliceContains(t, ids, second.ID)

		expected := slices.Clone(ids)
		slices.Sort(expected)
		test.Eq(t, expected, ids)
	})

	t.Run("a user with no passkeys reads as an empty slice", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		found, err := store.GetCredentialsForUser(t.Context(), env.reader(), testScope, "nobody")
		must.NoError(t, err)
		test.SliceEmpty(t, found)
	})

	t.Run("a revoked passkey leaves the user's list", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		written := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x60}))
		env.mustArchive(t, store, testScope, written.ID, "user_1")

		found, err := store.GetCredentialsForUser(t.Context(), env.reader(), testScope, "user_1")
		must.NoError(t, err)
		test.SliceEmpty(t, found)
	})

	t.Run("the sign count is written back and answered with", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		written := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x70}))
		test.EqOp(t, uint32(0), written.SignCount)

		used := env.mustRecordUse(t, store, testScope, written.ID, 42)
		test.EqOp(t, uint32(42), used.SignCount)
		must.NotNil(t, used.LastUsedAt)
		test.EqOp(t, testUsedAt.Unix(), used.LastUsedAt.Unix())

		// The row's own stamp is the server's clock, and it is a different fact
		// from the instant the caller named.
		must.NotNil(t, used.LastUpdatedAt)

		found, err := store.GetCredentialByCredentialID(t.Context(), env.reader(), testScope, []byte{0x70})
		must.NoError(t, err)
		test.EqOp(t, uint32(42), found.SignCount)
	})

	t.Run("a sign count write that reaches nothing is reported", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		written := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x71}))

		// The failure this table exists to prevent is a write-back nobody
		// noticed, so every way of reaching no row has to be an error rather
		// than a quiet success.
		_, wrongScope := env.recordUse(t, store, otherScope, written.ID, 1)
		must.ErrorIs(t, wrongScope, ErrCredentialNotFound)

		_, absent := env.recordUse(t, store, testScope, "no_such_row", 1)
		must.ErrorIs(t, absent, ErrCredentialNotFound)

		env.mustArchive(t, store, testScope, written.ID, "user_1")

		_, revoked := env.recordUse(t, store, testScope, written.ID, 1)
		must.ErrorIs(t, revoked, ErrCredentialNotFound)
	})

	t.Run("the archive answers with the row it hid", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		written := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x80}))
		archived := env.mustArchive(t, store, testScope, written.ID, "user_1")

		test.EqOp(t, written.ID, archived.ID)
		must.NotNil(t, archived.ArchivedAt)
	})

	t.Run("the archive is the authorization", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		written := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x81}))

		// Somebody else's passkey, another tenant's, and one already revoked
		// each move nothing — and each is told the same thing, which is the
		// answer that does not say which of the three it was.
		_, notOwner := env.archive(t, store, testScope, written.ID, "user_2")
		must.ErrorIs(t, notOwner, ErrCredentialNotFound)

		_, notScope := env.archive(t, store, otherScope, written.ID, "user_1")
		must.ErrorIs(t, notScope, ErrCredentialNotFound)

		env.mustArchive(t, store, testScope, written.ID, "user_1")

		_, again := env.archive(t, store, testScope, written.ID, "user_1")
		must.ErrorIs(t, again, ErrCredentialNotFound)
	})

	t.Run("a read inside the writing transaction sees the row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// The reason the reads take the wider executor. A caller that has just
		// registered a passkey and wants to list the user's passkeys before the
		// transaction commits passes the Tx, and sees its own write.
		err := env.inTx(t, func(tx database.Tx) error {
			if _, createErr := store.CreateCredential(t.Context(), tx, testScope,
				newCredential("user_1", []byte{0x90})); createErr != nil {
				return createErr
			}

			found, readErr := store.GetCredentialsForUser(t.Context(), tx, testScope, "user_1")
			if readErr != nil {
				return readErr
			}

			must.SliceLen(t, 1, found)

			return nil
		})
		must.NoError(t, err)
	})

	t.Run("a refused write leaves no row behind", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Two credentials in one transaction, the second colliding with a row
		// already committed. The transaction is the caller's, so the whole of it
		// rolls back — which is the property the interface is shaped for.
		committed := []byte{0xA0}
		env.mustCreate(t, store, testScope, newCredential("user_1", committed))

		err := env.inTx(t, func(tx database.Tx) error {
			if _, createErr := store.CreateCredential(t.Context(), tx, testScope,
				newCredential("user_1", []byte{0xA1})); createErr != nil {
				return createErr
			}

			_, collideErr := store.CreateCredential(t.Context(), tx, testScope,
				newCredential("user_1", committed))

			return collideErr
		})
		must.ErrorIs(t, err, ErrCredentialRegistered)

		found, readErr := store.GetCredentialsForUser(t.Context(), env.reader(), testScope, "user_1")
		must.NoError(t, readErr)
		must.SliceLen(t, 1, found)
		test.Eq(t, committed, found[0].CredentialID)
	})

	t.Run("every method refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		ctx := t.Context()

		_, create := store.CreateCredential(ctx, nil, testScope, newCredential("user_1", []byte{0xB0}))
		must.ErrorIs(t, create, ErrNilExecutor)

		_, lookup := store.GetCredentialByCredentialID(ctx, nil, testScope, []byte{0xB0})
		must.ErrorIs(t, lookup, ErrNilExecutor)

		_, list := store.GetCredentialsForUser(ctx, nil, testScope, "user_1")
		must.ErrorIs(t, list, ErrNilExecutor)

		_, use := store.RecordUse(ctx, nil, testScope, "row", 1, testUsedAt)
		must.ErrorIs(t, use, ErrNilExecutor)

		_, archive := store.ArchiveCredentialForUser(ctx, nil, testScope, "row", "user_1")
		must.ErrorIs(t, archive, ErrNilExecutor)
	})

	t.Run("the arguments a write cannot do without", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, nilCredential := env.create(t, store, testScope, nil)
		must.ErrorIs(t, nilCredential, ErrNilCredential)

		_, noUser := env.create(t, store, testScope, newCredential("", []byte{0xC0}))
		must.ErrorIs(t, noUser, ErrEmptyUserID)

		_, noCredentialID := env.create(t, store, testScope, newCredential("user_1", nil))
		must.ErrorIs(t, noCredentialID, ErrEmptyCredentialID)

		noKey := newCredential("user_1", []byte{0xC1})
		noKey.PublicKey = nil

		_, missingKey := env.create(t, store, testScope, noKey)
		must.ErrorIs(t, missingKey, ErrEmptyPublicKey)

		_, emptyLookup := store.GetCredentialByCredentialID(t.Context(), env.reader(), testScope, nil)
		must.ErrorIs(t, emptyLookup, ErrEmptyCredentialID)

		_, emptyList := store.GetCredentialsForUser(t.Context(), env.reader(), testScope, "")
		must.ErrorIs(t, emptyList, ErrEmptyUserID)

		_, emptyOwner := env.archive(t, store, testScope, "row", "")
		must.ErrorIs(t, emptyOwner, ErrEmptyUserID)
	})

	t.Run("an unset scope is refused rather than widened", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		unset := tenancy.Scope{}

		_, create := env.create(t, store, unset, newCredential("user_1", []byte{0xD0}))
		must.Error(t, create)
		test.False(t, errors.Is(create, ErrScopeMismatch))

		_, list := store.GetCredentialsForUser(t.Context(), env.reader(), unset, "user_1")
		must.Error(t, list)
	})
}
