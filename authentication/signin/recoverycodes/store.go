package recoverycodes

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is everything this package's table answers: the four methods the
// sign-in service calls, and the two a privacy pipeline does.
//
// The first four are signin.RecoveryCodeStore, embedded rather than restated,
// because that is the seam and a second spelling of it here would be one that
// could drift from it. The two beyond it are not on the seam because the service
// never calls them: listing a person's codes is an export's question, and
// deleting them outside a replacement is an erasure's. See the privacy package.
type Store interface {
	signin.RecoveryCodeStore

	// ListForUser answers with every code a user holds, spent and unspent, as
	// records rather than secrets — there is nothing on a RecoveryCode that
	// presents as one.
	//
	// It is unpaged, and the bound is structural: a replacement deletes the set
	// it replaces, so what one person holds is one set rather than their
	// history.
	ListForUser(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
	) ([]*RecoveryCode, error)

	// DeleteForUser removes every code a user holds, spent or not, and reports
	// how many it removed. Zero is not an error: somebody who never asked for a
	// set holds none.
	DeleteForUser(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
	) (int64, error)
}

// RecoveryCode is the record of one code in a set.
//
// The code itself is not on it, and there is no field it could go in: what is
// stored is a digest, and the code exists once, in the slice Replace returned.
// The digest is not on it either — see the store's ListForUser for why.
type RecoveryCode struct {
	_ struct{} `json:"-"`

	// IssuedAt is when the set this code belongs to was minted.
	IssuedAt time.Time `json:"issuedAt"`

	// UsedAt is when the code was spent, or nil while it is not.
	UsedAt *time.Time `json:"usedAt,omitempty"`

	// UserID is whose code this is. It is opaque to the store — this package
	// reads no user table — so an application whose users live outside identity
	// uses it unchanged.
	UserID string `json:"belongsToUser"`

	// Scope is whose directory the code was minted in.
	Scope tenancy.Scope `json:"scope"`
}
