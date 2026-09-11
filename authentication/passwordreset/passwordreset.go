package passwordreset

import (
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Token is the record of one issued reset token.
//
// The token itself is not on it, and there is no field it could go in: what is
// stored is a digest, and the secret exists once, in the Issuance that produced
// it. A Token read back from the store is therefore safe to log, to return from
// an administrative API, and to keep — which is the whole point of the split.
type Token struct {
	// CreatedAt is when the reset was asked for.
	CreatedAt time.Time `json:"createdAt"`
	// ExpiresAt is the deadline past which the token is refused. It is compared
	// against the store's clock rather than swept into truth, so a row the
	// sweeper has not reached is already dead.
	ExpiresAt time.Time `json:"expiresAt"`
	// RedeemedAt is when the token was spent, or nil while it is unspent. It is
	// what makes "this link has already been used" answerable, which is why a
	// redeemed row is left until its own expiry rather than deleted as it is
	// consumed.
	RedeemedAt *time.Time `json:"redeemedAt,omitempty"`
	// ID identifies the row. It is not the token and cannot be exchanged for
	// one: it is what a log line, a span, or an audit entry names.
	ID string `json:"id"`
	// UserID is the principal the token resets. It is opaque here — this package
	// never reads a user table, so an application whose users live outside
	// identity uses it unchanged.
	UserID string `json:"belongsToUser"`
	// Scope is whose token it is.
	Scope tenancy.Scope `json:"scope"`
}

// Live reports whether the token is still spendable as of now: unredeemed, and
// not past its deadline.
//
// It is the same comparison Verify and Consume make, exported so a caller
// rendering a token's state — an administrative view of somebody's outstanding
// links — reaches one answer rather than reimplementing the boundary. What it
// cannot tell a caller is whether a redemption will win the race against
// another one; only Consume decides that.
func (t *Token) Live(now time.Time) bool {
	return t != nil && t.RedeemedAt == nil && now.UTC().Before(t.ExpiresAt.UTC())
}

// Issuance is what Issue returns: the secret to put in the email, and the row
// that was written for it.
//
// The two are separate fields rather than a token with a Secret on it because
// they have different lifetimes. Secret is in memory for as long as it takes to
// render a link and hand it to a mailer; Token is the durable half, and it is
// the one that is safe to keep.
type Issuance struct {
	// Token is what was stored.
	Token *Token `json:"token"`

	// Secret is the raw token, and this is the only place it will ever exist.
	// The store holds a digest of it and cannot reverse one, so a secret that
	// is not sent is a reset nobody can complete.
	//
	// It is URL-safe base64 with no padding, so it goes into a link without
	// escaping. It carries no json tag other than this one because it is
	// serializable at all only for a caller that has chosen to move it — do not
	// log it, do not store it, and do not put it in a response body that is not
	// the one email.
	Secret string `json:"-"`
}
