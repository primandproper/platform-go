package requestsigning

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
	hmachasher "github.com/primandproper/platform-go/v13/cryptography/hashing/hmac"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// signingPayload renders the bytes a signature actually covers:
//
//	<scheme>.<unix timestamp>.<body>
//
// The scheme and the timestamp are inside the signed material rather than
// merely alongside it, and both are load-bearing.
//
// The timestamp is what makes a captured request expire. Signing the body alone
// produces a signature valid forever, so anyone who observes one request can
// replay it against the receiver indefinitely.
//
// The scheme prefix is what makes the construction replaceable. Without it, a
// v2 that signed different material could be substituted by an attacker for a
// v1 signature over material that happens to coincide, and a receiver accepting
// both schemes during a migration would have no way to tell them apart. Binding
// the scheme into the signed bytes means a v1 signature can only ever verify as
// v1.
func signingPayload(scheme string, timestamp int64, body []byte) []byte {
	prefix := scheme + "." + strconv.FormatInt(timestamp, 10) + "."

	// Built in one allocation: this runs once per key per attempt, on bodies
	// that can be large.
	buf := make([]byte, 0, len(prefix)+len(body))
	buf = append(buf, prefix...)
	buf = append(buf, body...)

	return buf
}

// Sign renders the SignatureHeader value for body at the given time, under
// every active key in keyring.
//
// The result looks like:
//
//	v1,t=1753900000,s=<hex>,s=<hex>
//
// A second s= appears only during a rotation window, when keyring.Previous is
// set. Emitting both is what lets a receiver roll its key without coordinating
// an instant of downtime with whoever operates the sender: it accepts either
// signature while it switches, and the operator drops Previous once every
// receiver has.
//
// Verify accepts a header with any number of s= components, so widening this to
// a longer key list later is not a wire change.
func Sign(keyring Keyring, body []byte, at time.Time) (string, error) {
	if len(keyring.Current) == 0 {
		return "", ErrNoSigningKey
	}

	timestamp := at.UTC().Unix()
	payload := signingPayload(SchemeV1, timestamp, body)

	var sb strings.Builder

	sb.WriteString(SchemeV1)
	sb.WriteString(",t=")
	sb.WriteString(strconv.FormatInt(timestamp, 10))

	for _, key := range keyring.Keys() {
		sb.WriteString(",s=")
		sb.WriteString(hashing.Hex(hmachasher.NewHMACSHA256Hasher(key), payload))
	}

	return sb.String(), nil
}

// Verify checks a SignatureHeader value against body under keyring, and is what
// a receiver calls on receipt.
//
// It ships with the signer on purpose. Verification is where these schemes are
// actually got wrong: receivers compare with ==, forget the timestamp check, or
// verify a re-serialized body rather than the received bytes. Handing out the
// sender and leaving the receiver to reimplement it from prose is how that
// keeps happening.
//
// body must be the exact bytes received, read before any decoding. Decoding and
// re-encoding changes key order and whitespace, and the signature covers bytes,
// not meaning.
//
// A signature verifies if it matches under any key in keyring, so a receiver
// holding both an old and a new key accepts requests from either side of a
// rotation.
func Verify(keyring Keyring, body []byte, signature string, opts ...Option) error {
	return verify(keyring, body, signature, newConfig(opts))
}

// verify is Verify against a configuration that is already resolved.
//
// It exists so the Verifier object can hold its config from construction rather
// than rebuilding one — and, more to the point, rendering it back into options —
// on every request it checks.
func verify(keyring Keyring, body []byte, signature string, cfg *config) error {
	keys := keyring.Keys()
	if len(keys) == 0 {
		return ErrNoVerificationKey
	}

	scheme, timestamp, candidates, err := parseSignature(signature)
	if err != nil {
		return err
	}

	// Checked before any HMAC is computed; see Freshness.Check for why that
	// ordering is what keeps a replay flood cheap.
	if err = cfg.Check(time.Unix(timestamp, 0)); err != nil {
		return err
	}

	payload := signingPayload(scheme, timestamp, body)

	hashers := make([]hashing.Hasher, 0, len(keys))
	for _, key := range keys {
		hashers = append(hashers, hmachasher.NewHMACSHA256Hasher(key))
	}

	// Every key is tried against every candidate without short-circuiting; see
	// hmac.MatchesAny for why the loop does not break on a match.
	if !hmachasher.MatchesAny(hashers, payload, candidates...) {
		return ErrInvalidSignature
	}

	return nil
}

// maxSignatureComponents bounds how many components parseSignature will read,
// so an attacker cannot make a receiver allocate — or HMAC-compare against —
// an unbounded list by sending a header full of s= parts.
const maxSignatureComponents = 16

// parseSignature splits a SignatureHeader value into its scheme, timestamp, and
// candidate MACs.
//
// Unknown components are ignored rather than rejected, so a later scheme may add
// one without every existing receiver failing closed on it. An unknown *scheme*
// is rejected outright: that is a change to what the signature covers, and
// ignoring it would mean verifying v2 material under v1 rules.
func parseSignature(signature string) (scheme string, timestamp int64, candidates [][]byte, err error) {
	parts := strings.Split(signature, ",")
	if len(parts) < 2 || len(parts) > maxSignatureComponents {
		return "", 0, nil, ErrInvalidSignature
	}

	scheme = strings.TrimSpace(parts[0])
	if scheme != SchemeV1 {
		return "", 0, nil, platformerrors.Wrapf(ErrInvalidSignature, "unsupported signature scheme %q", scheme)
	}

	haveTimestamp := false

	for _, part := range parts[1:] {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			return "", 0, nil, ErrInvalidSignature
		}

		switch key {
		case "t":
			if haveTimestamp {
				// Two timestamps means two readings of what was signed, and
				// picking either one is a guess.
				return "", 0, nil, ErrInvalidSignature
			}

			if timestamp, err = strconv.ParseInt(value, 10, 64); err != nil {
				return "", 0, nil, ErrInvalidSignature
			}

			haveTimestamp = true
		case "s":
			mac, decodeErr := hex.DecodeString(value)
			if decodeErr != nil {
				return "", 0, nil, ErrInvalidSignature
			}

			candidates = append(candidates, mac)
		}
	}

	if !haveTimestamp || len(candidates) == 0 {
		return "", 0, nil, ErrInvalidSignature
	}

	return scheme, timestamp, candidates, nil
}
