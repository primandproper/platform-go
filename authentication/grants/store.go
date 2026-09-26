package grants

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is the third-party grant table: one standing credential per subject per
// provider, sealed at rest, refreshed by compare-and-set.
//
// Every write takes a database.Tx and every read takes the wider
// database.SQLQueryExecutor, which is the module's store convention. A consent
// is rarely the only row a consumer writes — the audit entry saying an account
// was connected, the flag that turns a sync on — and a store that opened its
// own transaction would be one whose companions landed in a second one. A
// caller with nothing to join opens one with Client.WithTransaction.
//
// Every method is scoped and there is no unscoped variant. An application with
// a single tenant passes tenancy.Global() everywhere.
//
// # What this store does not do
//
// It does not speak OAuth2. Exchanging an authorization code, refreshing a token
// and calling a provider's revocation endpoint are golang.org/x/oauth2's and the
// consumer's; [Refresh] is the one piece of protocol here, and it is a function
// over an [Exchanger] rather than a method, so no statement ever runs while a
// provider is being called.
type Store interface {
	// Put stores the grant a consent produced, through the caller's
	// transaction, replacing whatever grant the subject already held at that
	// provider — live or revoked — and answers with the row it wrote.
	//
	// A re-consent replaces rather than updates. The new grant is a new
	// credential with a new id, so a refresh still in flight against the old
	// one is refused rather than writing the old grant's refreshed tokens over
	// the new consent. Which refusal depends on where the re-consent lands: one
	// that commits before Refreshed reads the grant leaves no row under the old
	// id, and the answer is ErrGrantNotFound; one that lands between that read
	// and Refreshed's write has moved the same row to a new id, the
	// compare-and-set matches nothing, and the answer is ErrStaleRefresh.
	// Neither stores the stale tokens.
	//
	// The consent is not modified. A nil tx is an error wrapping
	// ErrNilExecutor.
	Put(ctx context.Context, tx database.Tx, scope tenancy.Scope, consent *Consent) (*Grant, error)

	// Get reads the live grant one subject gave one provider, with both tokens
	// opened for the caller.
	//
	// A revoked grant, one in another scope and one that never existed all read
	// as ErrGrantNotFound. A nil q is an error wrapping ErrNilExecutor.
	Get(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subject, provider string) (*Grant, error)

	// Refreshed stores the tokens a refresh returned, through the caller's
	// transaction, provided the grant still holds expectedAccessToken — the
	// access token the caller refreshed from — and answers with the row it
	// left.
	//
	// It is a compare-and-set, and that is the point of it. Two replicas
	// refreshing one grant both read the same access token and both call the
	// provider; the first to write wins, and the second is ErrStaleRefresh,
	// which asks it to read the grant again rather than store its own answer
	// over the winner's. A provider that rotates refresh tokens is handled by
	// the same write: the winner's rotated token is the one stored, and the
	// loser's — which the provider may already have invalidated — never is.
	//
	// A tokens value with no RefreshToken leaves the stored one standing, which
	// is what a provider that does not rotate means by omitting it. A grant
	// that is revoked, in another scope or absent is ErrGrantNotFound when this
	// call reads it; one revoked or replaced by a consent after that read and
	// before the write is ErrStaleRefresh, because the write's predicate is
	// what refuses it. A nil tx is an error wrapping ErrNilExecutor.
	Refreshed(ctx context.Context, tx database.Tx, scope tenancy.Scope, id, expectedAccessToken string, tokens *Tokens) (*Grant, error)

	// Revoke marks a live grant revoked, through the caller's transaction,
	// empties both token columns, and answers with the row it left: RevokedAt
	// from the database's clock, the reason recorded, and no tokens.
	//
	// It revokes here and nowhere else. A consumer that also wants the
	// provider to forget the grant reads it with Get for the token to send,
	// calls the provider's revocation endpoint itself outside any transaction —
	// for the reason Refresh runs outside one — and then revokes here. A
	// refresh the provider refused is recorded the same way, with
	// RevokedByProvider, so that a worker retrying refreshes stops retrying a
	// grant that will never work.
	//
	// A grant already revoked, in another scope or absent is
	// ErrGrantNotFound. A nil tx is an error wrapping ErrNilExecutor.
	Revoke(ctx context.Context, tx database.Tx, scope tenancy.Scope, id string, reason RevocationReason) (*Grant, error)

	// ListAllForSubject reads every grant one subject holds in the scope,
	// revoked ones included, oldest first, and opens no token: every Grant it
	// returns has an empty AccessToken and RefreshToken, because the statement
	// behind it never selects either column.
	//
	// It is the privacy export's read — authentication/grants/privacy — and
	// unpaged because a subject holds at most one grant per provider. A nil q
	// is an error wrapping ErrNilExecutor.
	ListAllForSubject(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subject string) ([]*Grant, error)

	// DeleteForSubject destroys every grant one subject holds in the scope,
	// revoked ones included, through the caller's transaction, and answers with
	// how many rows went.
	//
	// It is the erasure. A subject who holds none deletes nothing and is not an
	// error, which is what makes it safe to run for every subject an erasure
	// names. It does not tell any provider: a consumer that wants the grants
	// revoked at the source calls the revocation endpoints before erasing,
	// while it still has the tokens to send. A nil tx is an error wrapping
	// ErrNilExecutor.
	DeleteForSubject(ctx context.Context, tx database.Tx, scope tenancy.Scope, subject string) (int64, error)
}
