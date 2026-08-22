package requestsigning

import (
	"context"
	"net/http"
	"strconv"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// V1Signer mints v1 signatures over a keyring it re-reads per request. It is
// exported, and returned by NewSigner, so a caller can depend on the scheme it
// built rather than on the Signer seam.
type V1Signer struct {
	keys KeySource
	cfg  *config
}

var _ Signer = (*V1Signer)(nil)

// NewSigner builds the v1 Signer: it stamps SignatureHeader and
// TimestampHeader over the request body, under every key the source holds.
//
// The keyring is resolved per call rather than captured, so a rotation in the
// secret store reaches the wire without a restart — see NewSecretKeySource for
// what makes that affordable.
//
// Reads WithClock; WithTolerance and WithVerificationTime belong to the
// verifying side and are ignored here.
func NewSigner(keys KeySource, opts ...Option) (*V1Signer, error) {
	if keys == nil {
		return nil, ErrNilKeySource
	}

	return &V1Signer{keys: keys, cfg: newConfig(opts)}, nil
}

// Scheme returns SchemeV1.
func (s *V1Signer) Scheme() string { return SchemeV1 }

// SignRequest stamps the signature and timestamp headers over req's body.
//
// The timestamp header carries the same value that is inside the signature. It
// is set separately so a receiver can reject a stale request before spending an
// HMAC on it; a receiver must still treat the signature as authoritative, since
// only the copy inside the signed material is covered by the MAC.
func (s *V1Signer) SignRequest(ctx context.Context, req *http.Request) error {
	body, err := RequestBody(req)
	if err != nil {
		return platformerrors.Wrap(err, "reading the request body to sign it")
	}

	keyring, err := s.keys.Keyring(ctx)
	if err != nil {
		return platformerrors.Wrap(err, "resolving the signing keyring")
	}

	at := s.cfg.Now().UTC()

	signature, err := Sign(keyring, body, at)
	if err != nil {
		return err
	}

	req.Header.Set(SignatureHeader, signature)
	req.Header.Set(TimestampHeader, strconv.FormatInt(at.Unix(), 10))

	return nil
}
