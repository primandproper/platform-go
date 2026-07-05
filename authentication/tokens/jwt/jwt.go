package jwt

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v4/authentication/tokens"
	"github.com/primandproper/platform-go/v4/identifiers"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/golang-jwt/jwt/v5"
)

const (
	name = "jwt_signer"
)

type (
	signer struct {
		o11y       observability.Observer
		issuer     string
		audience   string
		signingKey []byte
	}
)

func NewJWTSigner(logger logging.Logger, tracerProvider tracing.TracerProvider, issuer, audience string, signingKey []byte) (tokens.Issuer, error) {
	s := &signer{
		issuer:     issuer,
		audience:   audience,
		signingKey: signingKey,
		o11y:       observability.NewObserver(name, logger, tracerProvider),
	}

	return s, nil
}

// IssueToken issues a new JSON web token. The issuer owns the standard claims
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

	claims := jwt.MapClaims{
		"exp": jwt.NewNumericDate(time.Now().Add(expiry).UTC()),           /* expiration time */
		"nbf": jwt.NewNumericDate(time.Now().Add(-1 * time.Minute).UTC()), /* not before */
		"iat": jwt.NewNumericDate(time.Now().UTC()),                       /* issued at */
		"aud": s.audience,                                                 /* audience, i.e. server address */
		"iss": s.issuer,                                                   /* issuer */
		"sub": subject,                                                    /* subject */
		"jti": jti,                                                        /* JWT ID */
	}
	for k, v := range extraClaims {
		if _, reserved := tokens.ReservedClaimKeys[k]; reserved {
			return "", "", fmt.Errorf("%w: %q", tokens.ErrReservedClaim, k)
		}
		claims[k] = v
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenStr, err = token.SignedString(s.signingKey)
	if err != nil {
		return "", "", err
	}

	return tokenStr, jti, nil
}

// ParseToken parses and verifies a JWT and returns its claims.
func (s *signer) ParseToken(ctx context.Context, tokenString string) (tokens.Claims, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	parsedToken, err := s.parseToken(tokenString)
	if err != nil {
		return nil, op.Error(err, "parsing JWT")
	}

	mapClaims, ok := parsedToken.Claims.(jwt.MapClaims)
	if !ok {
		return nil, op.Error(fmt.Errorf("unexpected JWT claims type %T", parsedToken.Claims), "asserting JWT claims type")
	}

	return jwtClaims{inner: mapClaims}, nil
}

func (s *signer) parseToken(tokenString string) (*jwt.Token, error) {
	parsedToken, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.signingKey, nil
	},
		jwt.WithAudience(s.audience),
		jwt.WithIssuer(s.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, err
	}

	return parsedToken, nil
}

// jwtClaims adapts jwt.MapClaims to tokens.Claims.
type jwtClaims struct {
	inner jwt.MapClaims
}

func (c jwtClaims) Subject() string {
	sub, err := c.inner.GetSubject()
	if err != nil {
		return ""
	}
	return sub
}

func (c jwtClaims) JTI() string {
	if s, ok := c.inner["jti"].(string); ok {
		return s
	}
	return ""
}

func (c jwtClaims) ExpiresAt() time.Time {
	exp, err := c.inner.GetExpirationTime()
	if err != nil || exp == nil {
		return time.Time{}
	}
	return exp.UTC()
}

func (c jwtClaims) Get(key string) (any, bool) {
	v, ok := c.inner[key]
	return v, ok
}

func (c jwtClaims) GetString(key string) (string, bool) {
	v, ok := c.inner[key].(string)
	return v, ok
}
