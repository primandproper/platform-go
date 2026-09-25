/*
Package grants stores the tokens a third party granted this deployment: one
standing credential per subject per provider, sealed at rest, refreshed by
compare-and-set, and revocable from either side.

# Which direction this is

authentication/oauth2clients and authentication/oauth2serverstore are this
deployment acting as the authorization server — administering the clients that
call it and the tokens it issued them. This package is the other direction:
this deployment acting as the client, holding a grant somebody gave it to reach
their account at a provider. A studio owner who pushes a schedule into their
Google Calendar consented once; what that consent produced — an access token, a
refresh token, and the account they reach — lives here, and is refreshed from
here for as long as the owner leaves it connected.

It is a domain package rather than a primitive because it owns a table and has
a privacy obligation. The protocol it depends on is golang.org/x/oauth2's, and
[Exchanger] is the one method of it this package needs.

# Why a consumer should not write this table itself

A refresh token is a standing credential to somebody else's account. Stored by
hand it is stored in plaintext by whoever forgets, refreshed by a loop that
races itself, and never deleted when the person leaves. The three halves of this
package are those three failures, each closed:

  - Both tokens are ciphertext at rest, sealed through primitives-go's
    cryptography/encryption with the row's key — scope, subject, provider — and
    the column's name as associated data, so a ciphertext moved into another row
    or the other column fails to open. The store never holds a plaintext it did
    not just decrypt for a caller.
  - A refresh is a compare-and-set. [Store.Refreshed] writes only if the row
    still holds the access token the caller refreshed from, so two replicas
    refreshing one grant cannot both win, and the loser — told
    [ErrStaleRefresh] — never stores a refresh token the provider has since
    rotated away.
  - authentication/grants/privacy exports the fact of each grant, never its
    tokens, and erases every row a subject holds in the erasure's transaction.

# The shape of a refresh

	grant, err := store.Get(ctx, client.Reader(), scope, studioID, "google")
	// ...
	tokens, err := grants.Refresh(ctx, googleOAuthConfig, grant)
	switch {
	case errors.Is(err, grants.ErrProviderRevoked):
		err = client.WithTransaction(ctx, func(tx database.Tx) error {
			_, revokeErr := store.Revoke(ctx, tx, scope, grant.ID, grants.RevokedByProvider)
			return revokeErr
		})
	case err == nil:
		err = client.WithTransaction(ctx, func(tx database.Tx) error {
			_, storeErr := store.Refreshed(ctx, tx, scope, grant.ID, grant.AccessToken, tokens)
			return storeErr
		})
		// errors.Is(err, grants.ErrStaleRefresh): somebody else won; read again.
	}

The provider is called between two statements rather than inside one
transaction, deliberately: a transaction held open across somebody else's
server is a lock held for as long as they take to answer, and the
compare-and-set is what makes the gap safe.

# One grant per subject per provider

A new consent replaces the old grant rather than sitting beside it — see
[Store.Put]. The subject is the consumer's to define, as it is everywhere in
this module: a tenant for a calendar push, a user for a "sign in with" door,
whatever the consumer's product means by who consented.

# Revocation, both ways

[Store.Revoke] records a revocation and empties both token columns in the same
statement, so a revoked row is a record that an account was connected and no
longer a credential. [RevokedByConsumer] is the subject disconnecting;
[RevokedByProvider] is a refresh the provider refused with invalid_grant, and
recording it is what stops a worker retrying a grant that will never work.
Telling the provider is the consumer's call, made before or after — this
package makes no request of its own.

# The table is yours to create

authentication/grants/migrations renders the DDL for a dialect and prefix.
Nothing here creates a table on its own.

# Where the SQL comes from

Every statement this package executes is generated. The table's facts are
spelled once, in internal/queries; `make generate` renders them through
database/querygen into canonical .sql files; `make sqlc_compile` checks them
against the DDL on all three dialects; sqlc-gen-unison emits internal/grantsdb
from the same files, and that is what the store executes.
*/
package grants

//go:generate go run ./internal/queriesgen
