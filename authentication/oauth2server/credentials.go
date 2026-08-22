package oauth2server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/random"
)

// CredentialByteLength is how many bytes of entropy every credential this
// package mints carries: authorization codes, access tokens, refresh tokens,
// client identifiers, and client secrets.
//
// 256 bits, base64url-encoded to 43 characters. The same number for all five
// because there is no credential here whose disclosure is survivable, and a
// per-credential length would be five numbers to justify instead of one.
const CredentialByteLength = 32

// mintCredential returns a fresh credential and the digest to store beside it.
//
// The pair is returned together, and the raw value is returned first, because
// that is the only ordering in which forgetting to store the digest is a
// compile error rather than a store full of credentials at rest.
//
// The generator is package-level rather than injected, for the reason
// sessions.NewID gives: there is exactly one correct source of randomness for a
// bearer credential, and an option to replace it would be an option to weaken
// it.
func mintCredential(ctx context.Context) (value, digest string, err error) {
	value, err = random.GenerateBase64EncodedString(ctx, CredentialByteLength)
	if err != nil {
		return "", "", platformerrors.Wrap(err, "minting oauth2 credential")
	}

	return value, Hash(value), nil
}

// Hash returns the hex-encoded SHA-256 digest of a credential. It is what every
// Store method keys on, and what a Client's SecretHash holds.
//
// SHA-256 with no salt and no work factor, deliberately. Every value passed
// here is 256 bits from crypto/rand, so there is no dictionary to attack, no
// two users choosing the same value, and nothing a slow hash would buy — while
// what it would cost is a KDF on the path of every resource-server request.
// Passwords are the opposite case in every one of those respects, and go
// through authentication/argon2.
//
// It is exported because a resource server holding an opaque token has to
// reach the same digest to look it up, and re-deriving "hex of sha256" at that
// call site is exactly the kind of second copy that can drift.
func Hash(credential string) string {
	sum := sha256.Sum256([]byte(credential))

	return hex.EncodeToString(sum[:])
}

// equalHash compares two digests without a timing signal.
//
// Both sides are hex digests of the same fixed length, so this is not
// protecting a length. It is protecting the prefix: a byte-by-byte compare on
// a client secret's digest leaks how much of a guess was right, one request at
// a time.
func equalHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// VerifyPKCE reports whether verifier is the S256 pre-image of challenge.
//
// S256 only. RFC 7636 also defines "plain", which puts the verifier in the
// authorization request — the request PKCE exists to protect — so supporting
// it would mean supporting the attack. There is no option to enable it.
//
// The comparison is constant-time for the same reason equalHash is, and an
// empty challenge or verifier is a failure rather than a match: a code that
// somehow reached the store with no challenge must not be redeemable by
// sending no verifier.
func VerifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}

	return equalHash(S256Challenge(verifier), challenge)
}

// S256Challenge renders the RFC 7636 S256 challenge for a verifier:
// base64url-encoded, unpadded, as the spec requires — a padded challenge is a
// different string and will not match one a compliant client computed.
func S256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// PKCE verifier length bounds, from RFC 7636 §4.1. They are checked at
// /authorize rather than only at /token so that a client whose verifier is too
// short finds out before a human has typed a password.
const (
	MinCodeVerifierLength = 43
	MaxCodeVerifierLength = 128
)

// validCodeVerifier reports whether v is a syntactically valid RFC 7636 code
// verifier: 43..128 characters from the unreserved set.
func validCodeVerifier(v string) bool {
	if len(v) < MinCodeVerifierLength || len(v) > MaxCodeVerifierLength {
		return false
	}

	for i := range len(v) {
		switch c := v[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-', c == '.', c == '_', c == '~':
		default:
			return false
		}
	}

	return true
}
