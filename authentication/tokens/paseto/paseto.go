package paseto

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/tokens"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"

	"github.com/o1egl/paseto/v2"
)

const (
	name = "paseto_signer"

	// signingKeyLength is what v2.local's XChaCha20-Poly1305 takes, and is fixed
	// by the algorithm rather than chosen here.
	signingKeyLength = 32
)

var _ tokens.Issuer = (*Signer)(nil)

type (
	// Signer is the PASETO tokens.Issuer implementation. It is exported, and
	// returned by NewSigner, so a caller who has chosen PASETO can depend on that
	// choice rather than on the interface every token format shares.
	Signer struct {
		o11y       observability.Observer
		issuer     string
		audience   string
		signingKey []byte
	}
)

// NewSigner builds a PASETO-backed tokens.Issuer.
//
// An empty signingKey is rejected: a zero-length key is not a secret, and the
// tokens minted under one are forgeable by anyone who notices.
//
// Any other length is rejected too, because v2.local seals tokens with
// XChaCha20-Poly1305 and that cipher takes exactly 32 bytes. A 16- or 64-byte
// key built a Signer that looked configured and failed at the first IssueToken,
// which moves a startup mistake into the first request that needed a token.
func NewSigner(issuer, audience string, signingKey []byte, opts ...Option) (*Signer, error) {
	if len(signingKey) == 0 {
		return nil, platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "PASETO signing key")
	}

	if len(signingKey) != signingKeyLength {
		return nil, platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue,
			"PASETO signing key is %d bytes, must be %d", len(signingKey), signingKeyLength)
	}

	o := newOptions(opts)

	s := &Signer{
		issuer:     issuer,
		audience:   audience,
		signingKey: signingKey,
		o11y:       observability.NewObserver(name, o.logger, o.tracerProvider),
	}

	return s, nil
}

// IssueToken issues a new PASETO token. The issuer owns the standard claims
// (exp, nbf, iat, aud, iss, sub, jti); callers supply any application-specific
// claims via extraClaims. Passing a reserved-claim key in extraClaims returns
// ErrReservedClaim.
func (s *Signer) IssueToken(ctx context.Context, subject string, expiry time.Duration, extraClaims map[string]any) (tokenStr, jti string, err error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if expiry <= 0 {
		expiry = time.Minute * 10
	}

	jti = identifiers.New()

	op.Set(keys.UserIDKey, subject).
		Set("token.issuer", s.issuer).
		Set("token.audience", s.audience).
		Set("token.jti", jti).
		Set("token.ttl", expiry.String())

	payload := map[string]any{
		"aud": s.audience,
		"iss": s.issuer,
		"jti": jti,
		"sub": subject,
		"iat": time.Now().UTC(),
		"exp": time.Now().Add(expiry).UTC(),
		"nbf": time.Now().Add(-1 * time.Minute).UTC(),
	}
	for k, v := range extraClaims {
		if _, reserved := tokens.ReservedClaimKeys[k]; reserved {
			return "", "", fmt.Errorf("%w: %q", tokens.ErrReservedClaim, k)
		}
		payload[k] = v
	}

	tokenStr, err = paseto.NewV2().Encrypt(s.signingKey, payload, "footer")
	if err != nil {
		return "", "", fmt.Errorf("signing token: %w", err)
	}

	return tokenStr, jti, nil
}

// ParseToken parses and decrypts a PASETO token and returns its claims.
func (s *Signer) ParseToken(ctx context.Context, providedToken string) (tokens.Claims, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	parsed, err := s.decryptToken(op, providedToken)
	if err != nil {
		return nil, err
	}

	return pasetoClaims(parsed), nil
}

func (s *Signer) decryptToken(op observability.Operation, providedToken string) (map[string]any, error) {
	var (
		parsedToken map[string]any
		footer      string
	)
	if err := paseto.NewV2().Decrypt(providedToken, s.signingKey, &parsedToken, &footer); err != nil {
		return nil, op.Error(err, "parsing PASETO token")
	}

	// Decrypting into a map performs authenticated decryption only — it does
	// NOT validate registered claims. Validate them explicitly so an expired,
	// not-yet-valid, or wrong-audience token is rejected, matching the JWT
	// backend's behavior.
	now := time.Now().UTC()

	exp, hasExp := claimTime(parsedToken, "exp")
	if !hasExp {
		return nil, op.Error(tokens.ErrTokenExpired, "validating PASETO token: missing exp claim")
	}
	if now.After(exp) {
		return nil, op.Error(tokens.ErrTokenExpired, "validating PASETO token")
	}

	if nbf, ok := claimTime(parsedToken, "nbf"); ok && now.Before(nbf) {
		return nil, op.Error(tokens.ErrTokenNotYetValid, "validating PASETO token")
	}

	if aud, ok := parsedToken["aud"].(string); !ok || aud != s.audience {
		return nil, op.Error(tokens.ErrInvalidAudience, "validating PASETO token")
	}

	if iss, ok := parsedToken["iss"].(string); !ok || iss != s.issuer {
		return nil, op.Error(tokens.ErrInvalidIssuer, "validating PASETO token")
	}

	return parsedToken, nil
}

// claimTime extracts a time-valued claim, accepting both a native time.Time
// (set at mint time) and the RFC3339 string it round-trips to through the
// PASETO payload's JSON encoding.
func claimTime(claims map[string]any, key string) (time.Time, bool) {
	switch v := claims[key].(type) {
	case time.Time:
		return v.UTC(), true
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC(), true
		}
	}

	return time.Time{}, false
}

// pasetoClaims adapts a PASETO payload map to tokens.Claims.
type pasetoClaims map[string]any

func (c pasetoClaims) Subject() string {
	if s, ok := c["sub"].(string); ok {
		return s
	}
	return ""
}

func (c pasetoClaims) JTI() string {
	if s, ok := c["jti"].(string); ok {
		return s
	}
	return ""
}

func (c pasetoClaims) ExpiresAt() time.Time {
	if exp, ok := claimTime(c, "exp"); ok {
		return exp
	}
	return time.Time{}
}

func (c pasetoClaims) Get(key string) (any, bool) {
	v, ok := c[key]
	return v, ok
}

func (c pasetoClaims) GetString(key string) (string, bool) {
	v, ok := c[key].(string)
	return v, ok
}
