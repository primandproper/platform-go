package passwordreset

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer hangs off the store's writes: one method per write,
// each called inside the transaction that write ran in, once its statements have
// landed.
//
// The transaction is the point, as it is for identity.Hooks. A reset token
// write's ordinary companion — an audit entry saying a link was issued for this
// account, or spent — is the same fact as the row, and is the one fact the row
// cannot keep: the sweeper deletes it at its expiry, and "was a link issued
// before the takeover, and was it used?" is asked months after that. So a hook
// receives the database.Tx and writes on it, and returning an error fails the
// write: the store answers with that error and nothing else, and the caller's
// transaction rolls back with it. A reset the log has no record of does not
// happen.
//
// That is the whole of what a hook adds over a caller writing the companion
// beside each store call itself, and it is worth having because the alternative
// is a consumer wrapping the Store to record two of its methods and calling
// through on the rest.
//
// What it costs is what identity.Hooks says it costs: a hook runs with a write
// transaction open, and on Consume that transaction is usually also the one
// changing somebody's password. Work that is slow, talks to a network, or can
// fail for reasons the write should survive belongs behind an outbox row the
// hook enqueues, not in the hook.
//
// Every method is "After", and none is a veto. Whether somebody may have a link
// or spend one is decided before or by the store; a hook returning an error is
// an abort of a decision already taken.
//
// # What a hook is handed
//
// A *Token, never an Issuance. The secret exists on exactly one value in this
// package, Issue returns it to the caller that asked for it, and it does not
// reach here — because the natural thing to do with an issuance hook is write
// what happened somewhere durable, and a reset link in an audit table is a reset
// link in every backup of it. oauth2clients.Hooks draws the same line for the
// same reason.
//
// Nor is the digest on it. A Token carries no field for one, so a hook holds
// the row's id, its principal, its scope and its deadlines, and nothing it could
// exchange for a password change.
//
// The rows a hook is handed are the rows the write answers its caller with, and
// they are the same values: a hook that modifies one modifies what the caller
// receives. Do not.
//
// Consume's hook is handed the one row it changed, after the change, because
// the change is the method's name: a token Consume answers with was unredeemed a
// moment ago, and is now redeemed at the RedeemedAt it carries. There is no
// before row to hand over that the hook could not write down itself.
//
// The two bulk writes are handed a principal and a count rather than rows. A
// revocation's rows are links nobody will follow, and an erasure's are the very
// data it exists to remove.
//
// Sweep calls no hook. It deletes on nobody's behalf, in no caller's
// transaction, and what it removes is rows already dead by their own deadline.
//
// It is one interface rather than a function type per write so that a
// consumer's recording layer is one type. Embed NoopHooks and override the
// methods that matter; an operation added here later then arrives as a no-op
// rather than a compile failure.
type Hooks interface {
	// AfterIssue is called with the token Issue stored, carrying the id it was
	// minted under and the deadline it was given. The secret is not on it; see
	// the Hooks documentation.
	AfterIssue(ctx context.Context, tx database.Tx, scope tenancy.Scope, token *Token) error

	// AfterConsume is called with the token Consume spent, carrying the
	// RedeemedAt the redemption stamped. It is called only for the caller that
	// won the token: a refusal — not found, expired, already redeemed — calls
	// nothing.
	AfterConsume(ctx context.Context, tx database.Tx, scope tenancy.Scope, token *Token) error

	// AfterRevokeForUser is called with the principal RevokeForUser revoked for
	// and how many unredeemed tokens that destroyed. It is called when the count
	// is zero, too: a completed reset revokes whether or not anything was
	// outstanding, and a consumer recording revocations records that one.
	AfterRevokeForUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string, revoked int64) error

	// AfterDeleteForUser is called with the principal an erasure deleted every
	// token for and how many that was, zero included: an erasure that found
	// nothing to erase still ran.
	AfterDeleteForUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string, deleted int64) error
}

// NoopHooks does nothing. It is what a caller passes NewSQLStore, by name, when
// it commits nothing alongside these writes — a seed import, a bootstrap tool, a
// test — and it is the type to embed in a Hooks that overrides some:
//
//	type recordingHooks struct {
//		passwordreset.NoopHooks
//
//		audit audit.Recorder
//	}
//
// Embedding it rather than implementing every method is what makes a method
// added to Hooks later additive: an embedder gains a no-op rather than a compile
// failure. A consumer implementing the interface outright — which the generated
// HooksMock invites — is the consumer the next method breaks. That can
// be the point: a consumer that records every write may prefer a new one to
// fail to compile until somebody decides what it records.
type NoopHooks struct{}

var _ Hooks = NoopHooks{}

// AfterIssue implements Hooks.
func (NoopHooks) AfterIssue(context.Context, database.Tx, tenancy.Scope, *Token) error {
	return nil
}

// AfterConsume implements Hooks.
func (NoopHooks) AfterConsume(context.Context, database.Tx, tenancy.Scope, *Token) error {
	return nil
}

// AfterRevokeForUser implements Hooks.
func (NoopHooks) AfterRevokeForUser(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

// AfterDeleteForUser implements Hooks.
func (NoopHooks) AfterDeleteForUser(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}
