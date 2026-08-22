package oauth2server_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"

	"github.com/shoenig/test"
)

func TestHash(T *testing.T) {
	T.Parallel()

	T.Run("is the hex SHA-256 of the credential", func(t *testing.T) {
		t.Parallel()

		// Asserted against the algorithm rather than against a recorded string:
		// what matters is that a resource server deriving the same digest by
		// hand reaches the same value, and a recorded constant would still pass
		// if this switched to SHA-512.
		sum := sha256.Sum256([]byte("token"))
		test.EqOp(t, hex.EncodeToString(sum[:]), oauth2server.Hash("token"))
	})

	T.Run("is stable and distinguishing", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, oauth2server.Hash("a"), oauth2server.Hash("a"))
		test.NotEq(t, oauth2server.Hash("a"), oauth2server.Hash("b"))

		// The empty string hashes to something, which is why every Store checks
		// for an empty identifier before hashing rather than after: a digest of
		// "" is a perfectly good lookup key for a row nobody meant to write.
		test.NotEq(t, "", oauth2server.Hash(""))
	})
}

func TestS256Challenge(T *testing.T) {
	T.Parallel()

	T.Run("is unpadded base64url of the digest", func(t *testing.T) {
		t.Parallel()

		verifier := strings.Repeat("a", oauth2server.MinCodeVerifierLength)
		sum := sha256.Sum256([]byte(verifier))

		// Unpadded, as RFC 7636 requires. A padded challenge is a different
		// string and will not match one a compliant client computed.
		test.EqOp(t, base64.RawURLEncoding.EncodeToString(sum[:]), oauth2server.S256Challenge(verifier))
		test.StrNotContains(t, oauth2server.S256Challenge(verifier), "=")
	})
}

func TestVerifyPKCE(T *testing.T) {
	T.Parallel()

	T.Run("matches a verifier against its own challenge", func(t *testing.T) {
		t.Parallel()

		verifier := strings.Repeat("v", oauth2server.MinCodeVerifierLength)
		test.True(t, oauth2server.VerifyPKCE(verifier, oauth2server.S256Challenge(verifier)))
	})

	T.Run("refuses another verifier's challenge", func(t *testing.T) {
		t.Parallel()

		verifier := strings.Repeat("v", oauth2server.MinCodeVerifierLength)
		other := strings.Repeat("w", oauth2server.MinCodeVerifierLength)

		test.False(t, oauth2server.VerifyPKCE(verifier, oauth2server.S256Challenge(other)))
	})

	T.Run("refuses the plain method by construction", func(t *testing.T) {
		t.Parallel()

		verifier := strings.Repeat("v", oauth2server.MinCodeVerifierLength)

		// Under RFC 7636's "plain", the challenge is the verifier. There is no
		// option that enables it, and this is what that means in practice.
		test.False(t, oauth2server.VerifyPKCE(verifier, verifier))
	})

	T.Run("an empty side never matches", func(t *testing.T) {
		t.Parallel()

		// A code that somehow reached a store with no challenge must not be
		// redeemable by sending no verifier.
		test.False(t, oauth2server.VerifyPKCE("", ""))
		test.False(t, oauth2server.VerifyPKCE("verifier", ""))
		test.False(t, oauth2server.VerifyPKCE("", "challenge"))
	})
}
