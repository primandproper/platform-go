package paseto

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/authentication/tokens"
	"github.com/primandproper/platform-go/identifiers"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/o1egl/paseto/v2"
)

const (
	name = "paseto_signer"
)

type (
	signer struct {
		o11y       observability.Observer
		issuer     string
		audience   string
		signingKey []byte
	}
)

func NewPASETOSigner(logger logging.Logger, tracerProvider tracing.TracerProvider, issuer, audience string, signingKey []byte) (tokens.Issuer, error) {
	s := &signer{
		issuer:     issuer,
		audience:   audience,
		signingKey: signingKey,
		o11y:       observability.NewObserver(name, logger, tracerProvider),
	}

	return s, nil
}

// IssueToken issues a new PASETO token. The issuer owns the standard claims
// (exp, nbf, iat, aud, iss, sub, jti); callers supply any application-specific
// claims via extraClaims. Passing a reserved-claim key in extraClaims returns
// ErrReservedClaim.
func (s *signer) IssueToken(ctx context.Context, subject string, expiry time.Duration, extraClaims map[string]any) (tokenStr, jti string, err error) {
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
		return "", "", fmt.Errorf("signing token with key length %d: %w", len(s.signingKey), err)
	}

	return tokenStr, jti, nil
}

// ParseToken parses and decrypts a PASETO token and returns its claims.
func (s *signer) ParseToken(ctx context.Context, providedToken string) (tokens.Claims, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	parsed, err := s.decryptToken(op, providedToken)
	if err != nil {
		return nil, err
	}

	return pasetoClaims(parsed), nil
}

func (s *signer) decryptToken(op observability.Operation, providedToken string) (map[string]any, error) {
	var (
		parsedToken map[string]any
		footer      string
	)
	if err := paseto.NewV2().Decrypt(providedToken, s.signingKey, &parsedToken, &footer); err != nil {
		return nil, op.Error(err, "parsing PASETO token")
	}

	return parsedToken, nil
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
	switch v := c["exp"].(type) {
	case time.Time:
		return v.UTC()
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC()
		}
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
