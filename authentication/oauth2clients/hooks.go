package oauth2clients

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer runs inside each operation's transaction.
//
// It is the seam an audit entry, a data change event or a search stamp goes
// through, and it is why [Service]'s operations own their transaction while the
// store's writes take one: a registration and the record of who minted it are
// one fact, and a hook that fails rolls the whole operation back — no row, and
// no credential returned.
//
// # What a hook is handed
//
// A *Client, never an [IssuedClient]. The plaintext secret exists on exactly
// one value in this package, it is returned to the caller that asked for it,
// and it does not reach here — because the natural thing to do with a creation
// hook is write what happened somewhere durable, and a secret in an audit table
// is a secret in a backup.
//
// The Client is the stored row, so the digest is on it. That is not the same
// hazard: it is what the table already holds.
//
// # What belongs in one
//
// Writes that must land with the registration. Not an email, not an HTTP call,
// not anything slow: this runs with the transaction open, and a hook that
// blocks holds a row lock while it does. Publish to an outbox and let something
// else deliver it.
type Hooks interface {
	// AfterCreateClient runs once the registration is written, inside its
	// transaction.
	AfterCreateClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, client *Client) error

	// AfterUpdateClient runs once a registration has been revised, inside its
	// transaction, with the row as it now stands.
	AfterUpdateClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, client *Client) error

	// AfterArchiveClient runs once a registration has been withdrawn, inside its
	// transaction.
	//
	// It is handed the row as it stood before the archive, because that is what
	// an audit entry needs to say what was withdrawn — a caller reading the id
	// back later has a row whose name and redirect URIs are the only record of
	// what the credential was for.
	AfterArchiveClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, client *Client) error
}

// NoopHooks does nothing, and is what a [Service] built without [WithHooks]
// runs. A consumer with nothing to commit alongside a registration configures
// nothing.
type NoopHooks struct{}

var _ Hooks = NoopHooks{}

// AfterCreateClient implements [Hooks].
func (NoopHooks) AfterCreateClient(context.Context, database.Tx, tenancy.Scope, *Client) error {
	return nil
}

// AfterUpdateClient implements [Hooks].
func (NoopHooks) AfterUpdateClient(context.Context, database.Tx, tenancy.Scope, *Client) error {
	return nil
}

// AfterArchiveClient implements [Hooks].
func (NoopHooks) AfterArchiveClient(context.Context, database.Tx, tenancy.Scope, *Client) error {
	return nil
}
