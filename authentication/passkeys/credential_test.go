package passkeys

import (
	"math"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestTransports is the round trip the column's contract rests on. Whatever the
// encoding is, a caller hands over a list and reads the same list back.
func TestTransports(T *testing.T) {
	T.Parallel()

	T.Run("a list survives the column", func(t *testing.T) {
		t.Parallel()

		transports := []string{"usb", "nfc", "internal", "hybrid"}

		encoded, err := encodeTransports(t.Context(), transports)
		must.NoError(t, err)

		decoded, err := decodeTransports(t.Context(), encoded)
		must.NoError(t, err)
		test.Eq(t, transports, decoded)
	})

	T.Run("a value carrying the delimiter a naive encoding would use", func(t *testing.T) {
		t.Parallel()

		// The reason the column holds an array rather than a joined string. A
		// transport is whatever the authenticator reported, and the set is not
		// closed: an unrecognized one carrying a comma or a quote has to come
		// back as it went in rather than as two transports.
		transports := []string{`smart-card`, `a,b`, `has"quote`}

		encoded, err := encodeTransports(t.Context(), transports)
		must.NoError(t, err)

		decoded, err := decodeTransports(t.Context(), encoded)
		must.NoError(t, err)
		test.Eq(t, transports, decoded)
	})

	T.Run("no transports and none reported are the same row", func(t *testing.T) {
		t.Parallel()

		nilEncoded, err := encodeTransports(t.Context(), nil)
		must.NoError(t, err)

		emptyEncoded, err := encodeTransports(t.Context(), []string{})
		must.NoError(t, err)

		test.EqOp(t, emptyTransports, nilEncoded)
		test.EqOp(t, nilEncoded, emptyEncoded)

		decoded, err := decodeTransports(t.Context(), nilEncoded)
		must.NoError(t, err)
		test.SliceEmpty(t, decoded)
	})

	T.Run("a column some other writer left blank", func(t *testing.T) {
		t.Parallel()

		decoded, err := decodeTransports(t.Context(), "")
		must.NoError(t, err)
		test.SliceEmpty(t, decoded)
	})

	T.Run("a column holding something else is reported", func(t *testing.T) {
		t.Parallel()

		_, err := decodeTransports(t.Context(), "not an encoded list")
		must.Error(t, err)
	})
}

// TestSignCount is the narrowing between the column and the counter, which is
// the one conversion here that can be asked for something it cannot answer.
func TestSignCount(T *testing.T) {
	T.Parallel()

	T.Run("a counter survives the column", func(t *testing.T) {
		t.Parallel()

		for _, count := range []uint32{0, 1, math.MaxUint32} {
			narrowed, err := signCountFromRow(storedSignCount(count))
			must.NoError(t, err)
			test.EqOp(t, count, narrowed)
		}
	})

	T.Run("a row this package did not write is reported", func(t *testing.T) {
		t.Parallel()

		// Clamping would be the tempting answer and the wrong one: a counter
		// stuck at a bound is clone detection that never fires again, which is
		// precisely the silence this table exists to break.
		for _, stored := range []int64{-1, int64(math.MaxUint32) + 1} {
			_, err := signCountFromRow(stored)
			must.ErrorIs(t, err, ErrSignCountOutOfRange)
		}
	})
}

// TestCredentialValidation covers what a write refuses before it reaches a
// statement.
func TestCredentialValidation(T *testing.T) {
	T.Parallel()

	T.Run("a complete credential validates", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, newCredential("user_1", []byte{0x01}).ValidateWithContext(t.Context()))
	})

	T.Run("the three things it cannot do without", func(t *testing.T) {
		t.Parallel()

		var nilCredential *Credential
		must.ErrorIs(t, nilCredential.ValidateWithContext(t.Context()), ErrNilCredential)

		must.ErrorIs(t, newCredential("", []byte{0x01}).ValidateWithContext(t.Context()), ErrEmptyUserID)
		must.ErrorIs(t, newCredential("user_1", nil).ValidateWithContext(t.Context()), ErrEmptyCredentialID)

		noKey := newCredential("user_1", []byte{0x01})
		noKey.PublicKey = nil
		must.ErrorIs(t, noKey.ValidateWithContext(t.Context()), ErrEmptyPublicKey)
	})
}

// TestWebAuthnCredential is the conversion a ceremony verifies against, and the
// flags are the whole of what is interesting about it.
func TestWebAuthnCredential(T *testing.T) {
	T.Parallel()

	T.Run("the three values a verification reads", func(t *testing.T) {
		t.Parallel()

		stored := newCredential("user_1", []byte{0xAA, 0xBB})
		stored.SignCount = 7

		converted := stored.WebAuthnCredential(AuthenticatorFlags{})

		test.Eq(t, []byte{0xAA, 0xBB}, converted.ID)
		test.Eq(t, []byte{0x01, 0x02, 0x03}, converted.PublicKey)
		test.EqOp(t, uint32(7), converted.Authenticator.SignCount)
		must.SliceLen(t, 2, converted.Transport)
		test.EqOp(t, "internal", string(converted.Transport[0]))
	})

	T.Run("the backup flags come from the argument", func(t *testing.T) {
		t.Parallel()

		// The row carries no such column, deliberately. go-webauthn compares
		// the credential's BackupEligible against the one the assertion
		// reported and refuses a mismatch, and a synced passkey reports
		// something different from whatever the registration saw — so replaying
		// a stored flag rejects a valid login.
		stored := newCredential("user_1", []byte{0xAA})

		eligible := stored.WebAuthnCredential(AuthenticatorFlags{BackupEligible: true, BackupState: true})
		test.True(t, eligible.Flags.BackupEligible)
		test.True(t, eligible.Flags.BackupState)

		neither := stored.WebAuthnCredential(AuthenticatorFlags{})
		test.False(t, neither.Flags.BackupEligible)
		test.False(t, neither.Flags.BackupState)
	})
}
