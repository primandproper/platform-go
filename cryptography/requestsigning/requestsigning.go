package requestsigning

import (
	"context"
	"io"
	"net/http"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

const (
	// SignatureHeader carries the signature(s) over the request body.
	SignatureHeader = "X-Platform-Signature"

	// TimestampHeader carries the signing timestamp, as Unix seconds. It is the
	// same value that appears inside the signature; it is exposed separately so
	// a receiver can reject a stale request before doing any HMAC work.
	TimestampHeader = "X-Platform-Timestamp"

	// SchemeV1 is the only scheme this package mints. It is the literal prefix
	// bound into the signed bytes, so changing it is a wire break, not a rename.
	SchemeV1 = "v1"

	// DefaultTolerance is how far a signature's timestamp may sit from the
	// verifier's clock before verification rejects it.
	//
	// Five minutes is the customary figure, and it is a compromise between two
	// real failures: too tight and ordinary clock skew between sender and
	// receiver rejects good requests, too loose and a captured request stays
	// replayable for as long as the window lasts.
	DefaultTolerance = 5 * time.Minute
)

var (
	// ErrInvalidSignature indicates a signature header that is missing,
	// malformed, carries no recognized scheme, or does not match the body under
	// any key the verifier holds. The cases are deliberately one error: telling
	// a caller which of them applied tells an attacker how close a forgery came.
	ErrInvalidSignature = platformerrors.New("invalid request signature")

	// ErrStaleSignature indicates a signature whose timestamp is outside the
	// tolerance. It is distinct from ErrInvalidSignature because it is the one
	// verification failure with a benign cause an operator can act on — clock
	// skew — and it says nothing about the key.
	ErrStaleSignature = platformerrors.New("request signature timestamp outside tolerance")

	// ErrNoSigningKey indicates a keyring with no current key. Unsigned
	// requests are not something this package will mint: a receiver that cannot
	// authenticate a payload cannot safely act on it.
	ErrNoSigningKey = platformerrors.New("no current signing key")

	// ErrNoVerificationKey indicates a verification attempted against a keyring
	// holding no keys at all.
	//
	// It is deliberately not ErrInvalidSignature. A verifier with no keys
	// rejects everything, which looks from the outside exactly like a fleet of
	// callers that all got their signing wrong; naming it separately is what
	// lets the server report its own misconfiguration as a fault of its own
	// rather than as a verdict about the caller.
	ErrNoVerificationKey = platformerrors.New("no verification key")

	// ErrNilKeySource indicates a constructor called without a KeySource. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilKeySource = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil signing key source")
)

// Keyring carries the HMAC keys a signature is minted and checked under.
//
// It is a pair rather than a single value so that rotation is not an outage. A
// request is signed under Current and, while Previous is set, again under
// Previous; both signatures travel in the same header. A receiver therefore
// accepts requests throughout the window in which either side is switching
// keys, and the operator clears Previous once everyone has moved.
//
// A single shared secret makes that impossible: rolling it breaks every
// counterparty at the same instant, so in practice it never gets rolled.
type Keyring struct {
	// Current is the key new signatures are minted under. Required to sign.
	Current []byte `json:"-"`
	// Previous is an outgoing key still emitted alongside Current during a
	// rotation window. Empty outside one.
	Previous []byte `json:"-"`
}

// Keys reports the keyring's non-empty keys, in the order a signature emits
// them.
func (k Keyring) Keys() [][]byte {
	keys := make([][]byte, 0, 2)

	for _, key := range [][]byte{k.Current, k.Previous} {
		if len(key) > 0 {
			keys = append(keys, key)
		}
	}

	return keys
}

type (
	// Signer stamps a request with whatever proves its body was produced by
	// someone holding the key.
	//
	// It takes the request rather than a header bag and a []byte, and that is
	// what keeps it honest: the bytes it signs are read from the same request
	// its caller is about to send, so the two cannot be different bytes. A seam
	// that accepted the body separately would let a caller sign one payload and
	// transmit another, and nothing in the type would notice.
	Signer interface {
		// Scheme names the wire format this signer mints. It is a label, for
		// spans and log lines; nothing dispatches on it.
		Scheme() string

		// SignRequest reads req's body through RequestBody and writes the proof
		// into req.Header. It writes headers rather than returning one value
		// because a scheme may carry more than one — v1 sets a timestamp beside
		// its signature, so a receiver can shed a stale request before hashing
		// anything.
		//
		// req must be a request the caller is willing to have read, and should
		// carry a GetBody so the read is repeatable — see RequestBody. It is
		// called once per attempt rather than once per logical request, so the
		// timestamp it stamps is always fresh: a retry that fires after a long
		// backoff must not arrive already stale.
		SignRequest(ctx context.Context, req *http.Request) error
	}

	// Verifier checks that a request was signed by a holder of a key it trusts,
	// and is the inbound half of Signer.
	//
	// NewVerifier is only its v1 implementation. A scheme this package did not
	// design — a proof in another header, in another format — satisfies these
	// same two methods, so a service checking somebody else's signature runs the
	// same middleware over the same seam rather than a second verification stack
	// beside it. Locating the proof is the scheme's own business, which is why
	// this takes the whole request and not a header value somebody else picked
	// out of it.
	//
	// Code that holds bytes rather than a request — a queue consumer reading a
	// message payload, a test — wants the Verify function instead. That is the
	// transport-agnostic seam; this one is deliberately HTTP-shaped.
	Verifier interface {
		// Scheme names the wire format this verifier reads. It is a label, for
		// spans and log lines; nothing dispatches on it.
		Scheme() string

		// VerifyRequest checks req and returns nil only if its body was signed
		// under a key this verifier holds. A request carrying no proof at all is
		// ErrInvalidSignature: unsigned and badly signed are both "did not prove
		// it holds the key".
		//
		// The body is read through RequestBody, so it must be the exact bytes
		// received and the read must not have been bounded somewhere the
		// signature was not. requestsigning/http's middleware caps it before
		// handing the request over, which is where that bound belongs.
		VerifyRequest(ctx context.Context, req *http.Request) error
	}
)

// RequestBody reads a request's body without consuming it, when it can.
//
// GetBody is preferred and Body is the fallback, which is net/http's own way of
// saying whether a body is replayable. A signer or verifier calling this on a
// request that carries GetBody — which is what both callers in this module hand
// it — leaves that request as readable as it found it. On one that does not, the
// body is consumed, and whoever built it that way owns the consequence.
//
// It is exported because a scheme this package did not write needs to read the
// body exactly the way the built-in one does. A verifier that read it some other
// way would be checking a signature over bytes the handler never sees.
func RequestBody(req *http.Request) ([]byte, error) {
	if req == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil request")
	}

	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, platformerrors.Wrap(err, "rewinding the request body")
		}

		defer func() { _ = body.Close() }() //nolint:errcheck // a rewound in-memory body has nothing to fail at

		return io.ReadAll(body)
	}

	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}

	return io.ReadAll(req.Body)
}
