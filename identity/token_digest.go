package identity

import (
	"crypto/sha256"
	"encoding/hex"
)

// tokenDigest renders what this schema's two token columns hold: the
// hex-encoded SHA-256 of the secret a link carries.
//
// Both columns hold a digest rather than the token for the reason
// authentication/passwordreset's does. A verification link proves an address
// and an invitation link joins somebody else's account, so each is a bearer
// credential, and a database copy — a backup, a replica, a support engineer's
// query — hands out every outstanding one of them if the column holds the raw
// value. The verification column is also indexed, which would put a secret in
// an index; this package already refuses to index the invitation token for that
// reason, and the users table had the column it was arguing against.
//
// The digest is not salted and does not need to be: what it digests is a
// minter's randomness rather than something a person chose, so there is no
// dictionary to run against it.
//
// The empty string digests to the empty string, deliberately. It is not a
// token: it is how "no outstanding link" is stored, what the verification
// index's partial clause tests for, and what a burnt link is cleared to. A
// digest of it would give every user in the directory a link nobody minted, on
// a value every row shares.
//
// SHA-256 is fixed rather than an option, unlike passwordreset's hasher. No
// caller ever sees one of these — the column is store-internal and no migration
// is owed — so the only thing a choice would buy is the chance to invalidate
// every outstanding link by changing it. An option can be added later without
// breaking anybody; removing one cannot.
func tokenDigest(token string) string {
	if token == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}
