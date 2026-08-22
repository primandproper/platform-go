package encryptioncfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterEncryptorDecryptor(T *testing.T) {
	T.Parallel()

	T.Run("resolves a working keyring", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, encryption.Keyset{"k1": keyOne})

		RegisterEncryptorDecryptor(i)

		ed, err := do.Invoke[encryption.EncryptorDecryptor](i)
		must.NoError(t, err)

		ciphertext, err := ed.Encrypt(t.Context(), []byte("secret"), nil)
		must.NoError(t, err)

		plaintext, err := ed.Decrypt(t.Context(), ciphertext, nil)
		must.NoError(t, err)

		test.Eq(t, []byte("secret"), plaintext)
	})

	T.Run("wires up with no observability registered", func(t *testing.T) {
		t.Parallel()

		// A container that registers no pillars still has to build. Absent
		// observability is a configuration, not a failure.
		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, encryption.Keyset{"k1": keyOne})

		RegisterEncryptorDecryptor(i)

		_, err := do.Invoke[encryption.EncryptorDecryptor](i)
		test.NoError(t, err)
	})

	T.Run("carries every key in the registered keyset", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, encryption.Keyset{"k1": keyOne, "k2": keyTwo})

		RegisterEncryptorDecryptor(i)

		ed, err := do.Invoke[encryption.EncryptorDecryptor](i)
		must.NoError(t, err)

		ring, ok := ed.(*encryption.Keyring)
		must.True(t, ok)

		// A keyset trimmed to the current key would resolve fine and then fail
		// on the first row written before the last rotation.
		test.SliceLen(t, 2, ring.KeyIDs())
	})

	T.Run("surfaces a keyset that does not contain the current key", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, encryption.Keyset{"k2": keyTwo})

		RegisterEncryptorDecryptor(i)

		_, err := do.Invoke[encryption.EncryptorDecryptor](i)
		test.ErrorIs(t, err, encryption.ErrNoCurrentKey)
	})

	T.Run("registers under the interface rather than the concrete type", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, encryption.Keyset{"k1": keyOne})

		RegisterEncryptorDecryptor(i)

		// Consumers invoke the interface; nothing should have to know a
		// keyring is what satisfies it.
		_, err := do.Invoke[encryption.EncryptorDecryptor](i)
		test.NoError(t, err)
	})
}
