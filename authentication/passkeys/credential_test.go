package passkeys

import (
	"bytes"
	"math"
	"strings"
	"testing"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

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

// TestCredentialBounds pins the widths that keep one registration meaning one
// thing on three dialects.
//
// Each refusal case is one byte over its column, because one byte over is the
// case a limit written down as the wrong number still passes.
func TestCredentialBounds(T *testing.T) {
	T.Parallel()

	T.Run("each stored value is bounded by its column", func(T *testing.T) {
		T.Parallel()

		cases := []struct {
			mutate func(*Credential)
			name   string
		}{
			{
				name:   "id",
				mutate: func(c *Credential) { c.ID = strings.Repeat("i", MaxCredentialRowIDLength+1) },
			},
			{
				name:   "user id",
				mutate: func(c *Credential) { c.BelongsToUser = strings.Repeat("u", MaxUserIDLength+1) },
			},
			{
				name:   "credential id",
				mutate: func(c *Credential) { c.CredentialID = bytes.Repeat([]byte{0x01}, MaxCredentialIDLength+1) },
			},
			{
				name:   "public key",
				mutate: func(c *Credential) { c.PublicKey = bytes.Repeat([]byte{0x02}, MaxPublicKeyLength+1) },
			},
			{
				name:   "friendly name",
				mutate: func(c *Credential) { c.FriendlyName = strings.Repeat("n", MaxFriendlyNameLength+1) },
			},
		}

		for _, tc := range cases {
			T.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				credential := newCredential("user_1", []byte{0x01})
				tc.mutate(credential)

				err := credential.ValidateWithContext(t.Context())

				test.ErrorIs(t, err, ErrCredentialValueTooLong)

				// Answered as a bad request by the platform mapper, which is why
				// a package that ships no mappers of its own can refuse this at
				// all — see ErrCredentialValueTooLong.
				test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)

				// The message says which value it was, because "too long" with
				// five candidates is a refusal the caller has to guess at.
				test.StrContains(t, err.Error(), tc.name)
			})
		}
	})

	T.Run("a credential exactly at each limit is admitted", func(t *testing.T) {
		t.Parallel()

		credential := &Credential{
			ID:            strings.Repeat("i", MaxCredentialRowIDLength),
			BelongsToUser: strings.Repeat("u", MaxUserIDLength),
			CredentialID:  bytes.Repeat([]byte{0x01}, MaxCredentialIDLength),
			PublicKey:     bytes.Repeat([]byte{0x02}, MaxPublicKeyLength),
			FriendlyName:  strings.Repeat("n", MaxFriendlyNameLength),
		}

		must.NoError(t, credential.ValidateWithContext(t.Context()))
	})

	// The transports bound is on the encoding rather than on the list, because
	// the encoding is what the column holds.
	T.Run("the encoded transports are bounded too", func(t *testing.T) {
		t.Parallel()

		_, err := encodeTransports(t.Context(), []string{strings.Repeat("t", MaxTransportsLength)})

		test.ErrorIs(t, err, ErrCredentialValueTooLong)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
	})

	T.Run("the transports a real authenticator reports are well inside it", func(t *testing.T) {
		t.Parallel()

		encoded, err := encodeTransports(t.Context(), []string{"usb", "nfc", "ble", "internal", "hybrid"})
		must.NoError(t, err)

		test.Less(t, MaxTransportsLength, len(encoded))
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
