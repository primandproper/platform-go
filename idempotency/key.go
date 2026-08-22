package idempotency

import (
	"context"

	"github.com/primandproper/platform-go/v13/charset"
	"github.com/primandproper/platform-go/v13/identifiers"
)

// Observability keys. Package-specific ones are namespaced; nothing here maps
// onto an observability/keys constant.
const (
	keyKey           = "idempotency.key"
	fingerprintKey   = "idempotency.fingerprint"
	claimIDKey       = "idempotency.claim_id"
	recordVersionKey = "idempotency.record_version"
	replayedKey      = "idempotency.replayed"
	recordedKey      = "idempotency.recorded"
	actionKey        = "idempotency.action"
	outcomeKey       = "outcome"
)

// Outcomes reported on idempotency_requests. Every call to Do that resolves
// lands in exactly one of these, so the four together are the request total.
const (
	outcomeExecuted = "executed"
	outcomeReplayed = "replayed"
	outcomeInFlight = "in_flight"
	outcomeMismatch = "mismatch"
)

// Key identifies a logical operation, so that a retry of it can be recognized
// as the same operation rather than a new one. It is minted by the client and
// arrives over the wire.
//
// Fingerprint identifies what the operation was, and the two are distinct types
// on purpose. Do takes one of each, adjacent, and both are strings underneath:
// as bare strings a transposed pair compiles, runs, and silently disables
// mismatch detection — every request would fingerprint-match itself, so one key
// reused for two different requests would replay the first answer instead of
// being reported. That is a security control failing open with no signal, which
// makes it worth the conversions at the wire boundary.
type (
	Key         string
	Fingerprint string
)

// ValidateKey reports whether a client-supplied key is usable.
//
// A key becomes both a store key and a lock key, so it is restricted rather
// than escaped: printable ASCII with no spaces, which admits the UUIDs, xids,
// and base64url tokens clients actually send while excluding control
// characters and anything that would travel badly in a header.
//
// identifiers.Validate is deliberately not used. It accepts only xid, and the
// keys arriving here are minted by third-party clients — rejecting a
// well-formed UUID would break every caller that does the ordinary thing.
// Generating a key is the other direction; see WithNewKey.
//
// A non-positive maxLength disables the length check.
func ValidateKey(key Key, maxLength int) error {
	if key == "" {
		return ErrKeyRequired
	}

	// The length is checked before the characters, and the order is part of the
	// contract: a key that is both over-length and malformed reports the
	// length, because shortening it is the thing the client has to do first.
	// The bound also cannot move into keyCharset — it arrives per call, from
	// the manager's configuration.
	if maxLength > 0 && len(key) > maxLength {
		return ErrKeyTooLong
	}

	if !keyCharset.ContainsAll(string(key)) {
		return ErrKeyInvalid
	}

	return nil
}

// keyCharset is printable ASCII with the space excluded. Bytes, not runes: the
// check is over the wire representation, and anything outside this range is
// rejected whole rather than decoded.
//
// A Set rather than a charset.Checker because that is all this rule is — the
// emptiness and length answers above are this package's own, and each has its
// own sentinel.
var keyCharset = charset.VisibleASCII

// keyContextKey types the context value, so nothing else can collide with it.
type keyContextKey struct{}

// WithKey returns a context carrying key, for a client adapter to attach to
// outbound requests.
func WithKey(ctx context.Context, key Key) context.Context {
	return context.WithValue(ctx, keyContextKey{}, key)
}

// WithNewKey returns a context carrying a freshly minted key, and the key.
//
// Call it once per logical operation, outside any retry loop. That placement
// is the whole contract: every attempt sharing this context sends the same
// key, which is what lets the server recognize a retry. Minting inside the
// loop produces a new key per attempt and no protection at all.
//
// The generator is identifiers.New, which is fine for keys this process mints
// even though inbound keys are validated by shape rather than by xid — see
// ValidateKey.
func WithNewKey(ctx context.Context) (keyed context.Context, key Key) {
	key = Key(identifiers.New())

	return WithKey(ctx, key), key
}

// KeyFromContext returns the key carried by ctx, if any.
func KeyFromContext(ctx context.Context) (Key, bool) {
	key, ok := ctx.Value(keyContextKey{}).(Key)
	if !ok || key == "" {
		return "", false
	}

	return key, true
}
