package jwt

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/tokens"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"

	"github.com/golang-jwt/jwt/v5"
)

const (
	name = "jwt_signer"
)

var _ tokens.Issuer = (*Signer)(nil)

type (
	// Signer is the JWT tokens.Issuer implementation. It is exported, and returned
	// by NewSigner, so a caller who has chosen JWT can depend on that choice rather
	// than on the interface every token format shares.
	Signer struct {
		o11y       observability.Observer
		issuer     string
		audience   string
		signingKey []byte
	}
)

// NewSigner builds a JWT-backed tokens.Issuer.
//
// An empty signingKey is rejected rather than accepted: HS256 will happily mint
// and verify tokens under a zero-length HMAC key, so anyone who knows the
// algorithm can forge one.
func NewSigner(issuer, audience string, signingKey []byte, opts ...Option) (*Signer, error) {
	if len(signingKey) == 0 {
		return nil, platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "JWT signing key")
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

// IssueToken issues a new JSON web token. The issuer owns the standard claims
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
func (s *Signer) ParseToken(ctx context.Context, tokenString string) (tokens.Claims, error) {
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

func (s *Signer) parseToken(tokenString string) (*jwt.Token, error) {
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
		return nil, mapParseError(err)
	}

	return parsedToken, nil
}

// mapParseError translates golang-jwt's sentinels to the tokens package's.
//
// The Issuer interface promises provider-independent errors: a refresh flow
// that branches on tokens.ErrTokenExpired must keep working when a deployment
// switches from JWT to PASETO, which the design calls a safe change. Without
// this the JWT signer returned jwt.ErrTokenExpired and the PASETO signer
// returned tokens.ErrTokenExpired, so that branch silently stopped matching.
//
// The original error is wrapped, not discarded, so the provider's own detail is
// still there for anyone who wants it.
func mapParseError(err error) error {
	for from, to := range map[error]error{
		jwt.ErrTokenExpired:         tokens.ErrTokenExpired,
		jwt.ErrTokenNotValidYet:     tokens.ErrTokenNotYetValid,
		jwt.ErrTokenInvalidAudience: tokens.ErrInvalidAudience,
		jwt.ErrTokenInvalidIssuer:   tokens.ErrInvalidIssuer,
	} {
		if errors.Is(err, from) {
			return platformerrors.Join(to, err)
		}
	}

	return err
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
