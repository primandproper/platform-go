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
// It is one interface rather than a function type per operation so that a
// consumer's audit layer is one type. Embed [NoopHooks] and override what
// matters; a method added here later then does not break the embedder.
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
	// transaction, with the row as it stood before the revision and as it now
	// stands.
	//
	// Both, because a companion that records a revision records what changed,
	// and only the hook can say what the row was: once the statement has run,
	// the old name and redirect URIs are gone. The before row is one keyed read
	// on the operation's transaction, made only when the hooks are anything
	// but [NoopHooks], so a Service handed those pays nothing for it.
	AfterUpdateClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Client) error

	// AfterArchiveClient runs once a registration has been withdrawn, inside its
	// transaction.
	//
	// It is handed the row as the archive left it — ArchivedAt set, everything
	// else as it stood — because that is what an audit entry needs to say what
	// was withdrawn and when: a caller reading the id back later has a row whose
	// name and redirect URIs are the only record of what the credential was for.
	AfterArchiveClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, client *Client) error
}

// NoopHooks does nothing. It is what a caller passes [NewService], by name, when
// it commits nothing alongside these writes — a seed import, a bootstrap tool, a
// test — and it is the type to embed in a [Hooks] that overrides some:
//
//	type auditHooks struct {
//		oauth2clients.NoopHooks
//
//		audit audit.Recorder
//	}
//
// Embedding it rather than implementing all three is what makes a method added
// to [Hooks] later additive: an embedder gains a no-op rather than a compile
// failure. A consumer who implements the interface outright — which the
// generated HooksMock in the mock subpackage invites, since it implements every
// method — is the consumer the next method breaks. That can
// be the point: a consumer that records every write may prefer a new one to
// fail to compile until somebody decides what it records.
type NoopHooks struct{}

var _ Hooks = NoopHooks{}

// isNoop reports whether hooks are exactly NoopHooks, by value or by pointer.
func isNoop(hooks Hooks) bool {
	switch hooks.(type) {
	case NoopHooks, *NoopHooks:
		return true
	default:
		return false
	}
}

// AfterCreateClient implements [Hooks].
func (NoopHooks) AfterCreateClient(context.Context, database.Tx, tenancy.Scope, *Client) error {
	return nil
}

// AfterUpdateClient implements [Hooks].
func (NoopHooks) AfterUpdateClient(context.Context, database.Tx, tenancy.Scope, *Client, *Client) error {
	return nil
}

// AfterArchiveClient implements [Hooks].
func (NoopHooks) AfterArchiveClient(context.Context, database.Tx, tenancy.Scope, *Client) error {
	return nil
}
