package tokens

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v6/errors"
)

// ErrReservedClaim indicates that a caller passed a JWT registered-claim key in extraClaims.
// Reserved claim keys (iss, sub, aud, exp, nbf, iat, jti) are owned by the issuer and cannot
// be overridden by callers.
var ErrReservedClaim = platformerrors.New("reserved claim key in extraClaims")

// Sentinel errors returned by ParseToken implementations when a token decodes
// and authenticates but fails claim validation. Backends should return these
// (or library equivalents that wrap the same conditions) so callers can branch
// on the reason consistently regardless of which Issuer is configured.
var (
	// ErrTokenExpired indicates the token's "exp" claim is in the past.
	ErrTokenExpired = platformerrors.New("token is expired")
	// ErrTokenNotYetValid indicates the token's "nbf" claim is in the future.
	ErrTokenNotYetValid = platformerrors.New("token is not yet valid")
	// ErrInvalidAudience indicates the token's "aud" claim does not match the issuer's audience.
	ErrInvalidAudience = platformerrors.New("token audience is not valid")
	// ErrInvalidIssuer indicates the token's "iss" claim does not match the issuer.
	ErrInvalidIssuer = platformerrors.New("token issuer is not valid")
)

// ReservedClaimKeys is the set of JWT registered claim names (RFC 7519) the issuer owns.
// Callers MUST NOT include these in extraClaims passed to IssueToken.
var ReservedClaimKeys = map[string]struct{}{
	"iss": {},
	"sub": {},
	"aud": {},
	"exp": {},
	"nbf": {},
	"iat": {},
	"jti": {},
}

// Claims is the parsed claim set from a token. Implementations expose
// issuer-owned registered claims via typed accessors (Subject, JTI,
// ExpiresAt) and any application-specific claims via Get / GetString.
//
// Callers that need claims not surfaced by the typed accessors look them
// up by name, e.g. claims.GetString("account_id").
type Claims interface {
	// Subject returns the "sub" claim.
	Subject() string
	// JTI returns the "jti" claim.
	JTI() string
	// ExpiresAt returns the "exp" claim as a UTC time. Zero if unset.
	ExpiresAt() time.Time
	// Get returns the raw value for key and whether it was present.
	Get(key string) (any, bool)
	// GetString returns the string value for key and whether it was
	// present AND a string. Missing keys and non-string values both
	// return ("", false).
	GetString(key string) (string, bool)
}

// Issuer issues and parses authentication tokens. Implementations own the
// standard registered claims (sub, jti, iat, nbf, exp, aud, iss); callers
// supply any application-specific claims via extraClaims and read them
// back through the Claims returned by ParseToken.
type Issuer interface {
	IssueToken(ctx context.Context, subject string, expiry time.Duration, extraClaims map[string]any) (tokenStr, jti string, err error)
	ParseToken(ctx context.Context, token string) (Claims, error)
}
