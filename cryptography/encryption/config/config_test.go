package encryptioncfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	perrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var (
	keyOne = encryption.MasterKey("0123456789abcdef0123456789abcdef")
	keyTwo = encryption.MasterKey("fedcba9876543210fedcba9876543210")
)

func validConfig() *Config {
	return &Config{Provider: ProviderAES, CurrentKeyID: "k1"}
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a valid config", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, validConfig().ValidateWithContext(t.Context()))
	})

	T.Run("accepts a provider needing normalization", func(t *testing.T) {
		t.Parallel()

		// Dispatch trims and lowercases, so validation has to as well or it
		// rejects values the factory would have accepted.
		cfg := &Config{Provider: "  AES  ", CurrentKeyID: "k1"}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an unknown provider", func(t *testing.T) {
		t.Parallel()

		// Asserted on the message rather than with errors.Is: ozzo collects
		// field errors into a type that does not unwrap, so a sentinel put in
		// by a field rule is readable but not traversable. That is the
		// library's behavior, not this package's, and pinning it here keeps
		// the next person from assuming the sentinel travels.
		cfg := &Config{Provider: "salsa20", CurrentKeyID: "k1"}

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), perrors.ErrUnknownProvider.Error())
	})

	T.Run("rejects an empty provider", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{CurrentKeyID: "k1"}).ValidateWithContext(t.Context()))
	})

	T.Run("rejects an absent current key", func(t *testing.T) {
		t.Parallel()

		// Rotation works by changing this value, so there is nothing sensible
		// to default it to.
		test.Error(t, (&Config{Provider: ProviderAES}).ValidateWithContext(t.Context()))
	})
}

func TestNewKeyring(T *testing.T) {
	T.Parallel()

	T.Run("builds a ring over every supplied key", func(t *testing.T) {
		t.Parallel()

		ed, err := NewKeyring(t.Context(), validConfig(), encryption.Keyset{"k1": keyOne, "k2": keyTwo})
		must.NoError(t, err)
		must.NotNil(t, ed)

		ring, ok := ed.(*encryption.Keyring)
		must.True(t, ok)

		test.EqOp(t, encryption.KeyID("k1"), ring.CurrentKeyID())
		test.SliceLen(t, 2, ring.KeyIDs())
	})

	T.Run("round trips through the built ring", func(t *testing.T) {
		t.Parallel()

		ed, err := NewKeyring(t.Context(), validConfig(), encryption.Keyset{"k1": keyOne})
		must.NoError(t, err)

		ciphertext, err := ed.Encrypt(t.Context(), []byte("secret"), []byte("row-7"))
		must.NoError(t, err)

		plaintext, err := ed.Decrypt(t.Context(), ciphertext, []byte("row-7"))
		must.NoError(t, err)

		test.Eq(t, []byte("secret"), plaintext)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewKeyring(t.Context(), nil, encryption.Keyset{"k1": keyOne})
		test.ErrorIs(t, err, perrors.ErrNilInputParameter)
	})

	T.Run("rejects an invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := NewKeyring(t.Context(), &Config{Provider: "nope", CurrentKeyID: "k1"}, encryption.Keyset{"k1": keyOne})
		must.Error(t, err)
		test.StrContains(t, err.Error(), perrors.ErrUnknownProvider.Error())
	})

	T.Run("rejects an empty keyset", func(t *testing.T) {
		t.Parallel()

		_, err := NewKeyring(t.Context(), validConfig(), nil)
		test.ErrorIs(t, err, encryption.ErrEmptyKeyring)
	})

	T.Run("rejects a keyset missing the current key", func(t *testing.T) {
		t.Parallel()

		ed, err := NewKeyring(t.Context(), validConfig(), encryption.Keyset{"k2": keyTwo})
		test.ErrorIs(t, err, encryption.ErrNoCurrentKey)

		// Compared against nil directly rather than with test.Nil, which is
		// satisfied by a nil pointer inside a non-nil interface — the exact
		// value this asserts is absent. Returning encryption.NewKeyring's
		// (*Keyring, error) straight through produced one, and a caller's
		// `if ed != nil` accepted it and panicked on the first Encrypt.
		test.True(t, ed == nil)
	})

	T.Run("surfaces a key the provider will not accept", func(t *testing.T) {
		t.Parallel()

		// One short key in a keyset fails the whole ring rather than being
		// dropped: a ring quietly missing a key is a ring that cannot read
		// everything it was supposed to.
		_, err := NewKeyring(t.Context(), validConfig(), encryption.Keyset{
			"k1": keyOne,
			"k2": encryption.MasterKey("short"),
		})
		test.ErrorIs(t, err, encryption.ErrIncorrectKeyLength)
	})
}

func TestNewCipher_unknownProvider(T *testing.T) {
	T.Parallel()

	T.Run("the dispatch default refuses rather than defaulting", func(t *testing.T) {
		t.Parallel()

		// Called directly because ValidateWithContext rejects an unknown
		// provider first, making this branch unreachable through NewKeyring.
		// It still has to be right: validation and dispatch read the same
		// provider list, and if they ever drift this is what catches it —
		// silently falling through to AES would encrypt under an algorithm
		// nobody configured.
		_, err := newCipher(&Config{Provider: "salsa20"}, keyOne, newOptions(nil))
		test.ErrorIs(t, err, perrors.ErrUnknownProvider)
	})
}
