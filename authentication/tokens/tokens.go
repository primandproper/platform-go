package tokens

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/errors"
)

// ErrReservedClaim indicates that a caller passed a JWT registered-claim key in extraClaims.
// Reserved claim keys (iss, sub, aud, exp, nbf, iat, jti) are owned by the issuer and cannot
// be overridden by callers.
var ErrReservedClaim = platformerrors.New("reserved claim key in extraClaims")

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
