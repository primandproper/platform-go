package requestsigning

import (
	"context"
	"net/http"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// V1Verifier checks v1 signatures against a keyring it re-reads per request. It
// is exported, and returned by NewVerifier, so a caller can depend on the scheme
// it built rather than on the Verifier seam.
type V1Verifier struct {
	keys KeySource
	cfg  *config
}

var _ Verifier = (*V1Verifier)(nil)

// NewVerifier builds the v1 Verifier: it checks the SignatureHeader value
// against the body under every key the source holds.
//
// The keyring is resolved per call, so a receiver picks up the far side's key
// rotation without a restart — and, more usefully, can carry both keys through
// a window of its own.
//
// Reads WithClock, WithTolerance, and WithVerificationTime.
func NewVerifier(keys KeySource, opts ...Option) (*V1Verifier, error) {
	if keys == nil {
		return nil, ErrNilKeySource
	}

	return &V1Verifier{keys: keys, cfg: newConfig(opts)}, nil
}

// Scheme returns SchemeV1.
func (v *V1Verifier) Scheme() string { return SchemeV1 }

// VerifyRequest checks req's SignatureHeader against its body.
//
// TimestampHeader is not consulted. It is a courtesy copy for a receiver that
// wants to shed a stale request before reading a body at all; the value this
// checks is the one inside the signed material, which is the only one an
// attacker cannot edit.
func (v *V1Verifier) VerifyRequest(ctx context.Context, req *http.Request) error {
	if req == nil {
		return platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil request")
	}

	signature := req.Header.Get(SignatureHeader)
	if signature == "" {
		// The same error a wrong signature gets. An unsigned request and a
		// badly signed one are both "did not prove it holds the key", and
		// separating them tells a prober which header this endpoint reads.
		//
		// Checked before the body is read, so an unsigned request costs nothing
		// proportional to what it carried.
		return platformerrors.Wrapf(ErrInvalidSignature, "no %s header", SignatureHeader)
	}

	body, err := RequestBody(req)
	if err != nil {
		return platformerrors.Wrap(err, "reading the request body to verify it")
	}

	keyring, err := v.keys.Keyring(ctx)
	if err != nil {
		return platformerrors.Wrap(err, "resolving the verification keyring")
	}

	// Through the same code Verify runs, against a configuration resolved once
	// at construction. The object and the function cannot drift on what a
	// tolerance or a clock means, because there is only one of each.
	return verify(keyring, body, signature, v.cfg)
}
