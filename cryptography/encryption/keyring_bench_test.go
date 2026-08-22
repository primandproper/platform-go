package encryption_test

import (
	"bytes"
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/aes"

	"github.com/shoenig/test/must"
)

// External test package on purpose: the keyring is only worth measuring over a
// real cipher, and aes imports encryption, so an in-package benchmark would be
// an import cycle.

var byteSink []byte

func newBenchRing(b *testing.B, ids ...encryption.KeyID) *encryption.Keyring {
	b.Helper()

	material := []byte("0123456789abcdef0123456789abcdef")

	ringKeys := make([]encryption.RingKey, 0, len(ids))

	for _, id := range ids {
		cipher, err := aes.NewCipher(material)
		must.NoError(b, err)

		ringKeys = append(ringKeys, encryption.RingKey{ID: id, Cipher: cipher})
	}

	ring, err := encryption.NewKeyring(ids[0], ringKeys)
	must.NoError(b, err)

	return ring
}

func BenchmarkKeyring(b *testing.B) {
	ctx := b.Context()
	plaintext := bytes.Repeat([]byte("x"), 256)
	aad := []byte("row-0123456789abcdef")

	// The delta against the aes Cipher benchmarks is the ring's whole cost:
	// framing the header, copying it into the associated data, and one map
	// lookup on the way back.
	b.Run("Encrypt", func(b *testing.B) {
		ring := newBenchRing(b, "k1")

		for b.Loop() {
			byteSink, _ = ring.Encrypt(ctx, plaintext, aad)
		}
	})

	b.Run("Decrypt", func(b *testing.B) {
		ring := newBenchRing(b, "k1")

		ciphertext, err := ring.Encrypt(ctx, plaintext, aad)
		must.NoError(b, err)

		for b.Loop() {
			byteSink, _ = ring.Decrypt(ctx, ciphertext, aad)
		}
	})

	// A ring mid-rotation holds several keys. Decryption is a map lookup
	// either way, so this should be flat — worth pinning, because "does
	// keeping retired keys around cost reads anything" decides whether
	// operators feel pressure to retire keys early, which is the one
	// irreversible mistake available here.
	b.Run("DecryptRetiredKeyInEightKeyRing", func(b *testing.B) {
		writer := newBenchRing(b, "k1")

		ciphertext, err := writer.Encrypt(ctx, plaintext, aad)
		must.NoError(b, err)

		rotated := newBenchRing(b, "k8", "k7", "k6", "k5", "k4", "k3", "k2", "k1")

		for b.Loop() {
			byteSink, _ = rotated.Decrypt(ctx, ciphertext, aad)
		}
	})
}
