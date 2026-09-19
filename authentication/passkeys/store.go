package passkeys

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is the registered-passkey table: the rows a WebAuthn ceremony produces
// and the ones the next ceremony is verified against.
//
// # The transaction is the caller's
//
// Every write takes a database.Tx and every read takes the wider
// database.SQLQueryExecutor, which is the module's store convention rather than
// anything this package invented. No write here opens a transaction of its own,
// and that absence is the point. A registration is almost never the only row a
// consumer writes — the audit entry naming the new passkey, the notification
// telling the person one was added, the flag saying their account now has a
// second factor — and two transactions means a passkey somebody can log in with
// that nothing recorded, or a record of one that was never stored.
//
// The read takes the wider type so that one method serves both moments. A
// settings page listing somebody's passkeys holds no transaction and passes
// Client.Reader(); a login that has just written a sign count back passes the Tx
// it wrote through, and sees it. A read narrowed to Tx would force the first
// caller into a transaction it has no use for, and one narrowed to
// Client.Reader() would read a database that does not yet hold the row its caller
// just wrote.
//
// A caller with genuinely nothing to join opens one with Client.WithTransaction
// and passes the Tx it is handed.
//
// # The scope is an argument, on every method
//
// Every read is scoped and there is no unscoped variant of any of them. That is
// the point rather than a convenience: the caller who reaches for an unscoped
// read is the caller who has not thought about tenancy. An application with a
// single tenant passes tenancy.Global() everywhere and gets exactly the behavior
// it would have had without the column.
//
// [Store.CreateCredential] takes the scope as an argument even though a
// [Credential] carries one, and the argument wins. A credential naming no scope
// adopts it; one naming a different scope is ErrScopeMismatch rather than a row
// quietly filed under the field. A scope derived from a struct the caller
// assembled somewhere else is exactly the derivation the tenancy convention
// exists to rule out.
//
// # What this store does not do
//
// It does not run the protocol. The challenge, the attestation and the assertion
// are primitives-go's authentication/webauthn, and the ceremony state that spans
// two requests is authentication/webauthnsessions. This is the third table in
// that story and the only one a library cannot ship for you — until it existed,
// every consumer wrote it again.
//
// It does not know what a user is, either. A discoverable login arrives holding
// a WebAuthn user handle, and turning one into an account is the consumer's
// directory's job; [NewUserSource] is the seam that takes it as a function.
type Store interface {
	// CreateCredential stores one registered passkey through the caller's
	// transaction and answers with the row it wrote: the ID it assigned when the
	// argument carried none, the CreatedAt the database stamped, and the scope
	// the call named.
	//
	// The credential ID must be free among the scope's live rows. One already
	// enrolled is ErrCredentialRegistered; one whose row was archived is free
	// again, which is what the live-rows-only unique index is for — a person who
	// revokes a passkey and enrolls the same authenticator afresh is doing
	// something ordinary, and an index covering archived rows would refuse it
	// forever.
	//
	// The argument is not modified. Everything the write settled is on the value
	// returned, so a caller reads it from one place rather than from an argument
	// that changed under them. A nil tx is an error wrapping ErrNilExecutor.
	CreateCredential(ctx context.Context, tx database.Tx, scope tenancy.Scope, credential *Credential) (*Credential, error)

	// GetCredentialByCredentialID reads the scope's live passkey for the
	// credential ID an authenticator returned, which is the read a login runs.
	//
	// An archived credential reads as ErrCredentialNotFound, and so does one in
	// another scope. A revoked passkey verifying nothing is the whole point of
	// revoking it, and a cross-scope answer would turn this read into an oracle
	// for which credentials exist in other tenants. A nil q is an error wrapping
	// ErrNilExecutor.
	GetCredentialByCredentialID(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, credentialID []byte) (*Credential, error)

	// GetCredentialsForUser reads every live passkey one user has, oldest first.
	//
	// It is unpaged and takes no filter. An authenticator is a physical thing
	// and a person has a handful, so the set is bounded by the world rather than
	// by a cursor; and the caller is usually a ceremony, which needs all of them
	// — a login offered a page of somebody's passkeys would refuse the one they
	// are holding. A user with none reads as an empty slice rather than an
	// error. A nil q is an error wrapping ErrNilExecutor.
	GetCredentialsForUser(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, userID string) ([]*Credential, error)

	// RecordUse writes the authenticator's signature counter back, through the
	// caller's transaction, and answers with the row it left.
	//
	// It is the method this table exists for, and the one a from-scratch
	// implementation treats as best-effort. The counter's only purpose is that
	// the next assertion compares against it: a count that did not advance means
	// two authenticators are answering for one credential, which is the only
	// clone detection WebAuthn has. A count that is never written back is a
	// count every later login compares against a stale value, so the detection
	// reports nothing — forever, silently, on the one control it exists to
	// provide.
	//
	// So the error is the caller's to surface rather than to log. A login whose
	// bookkeeping failed is a login that did not happen: the alternative is a
	// deployment with working passkeys, no clone detection, and nothing that
	// says so.
	//
	// at is the instant the assertion happened, from the caller's clock, and is
	// what LastUsedAt reads back as. The row's own last_updated_at is stamped
	// from the database's — two facts, not two spellings.
	//
	// A credential that is archived, in another scope, or absent is
	// ErrCredentialNotFound: nothing was written, and the caller has to decide
	// that rather than be told the write succeeded. A nil tx is an error
	// wrapping ErrNilExecutor.
	RecordUse(ctx context.Context, tx database.Tx, scope tenancy.Scope, credentialRowID string, signCount uint32, at time.Time) (*Credential, error)

	// ArchiveCredentialForUser revokes one of a user's passkeys through the
	// caller's transaction, and answers with the row it hid, carrying the
	// ArchivedAt the database stamped.
	//
	// The owner is part of the statement rather than a check made first. A
	// read-then-archive is two statements with a window between them; here the
	// affected-row count is the authorization, so a credential belonging to
	// somebody else moves nothing and is reported as ErrCredentialNotFound —
	// the same answer a credential that does not exist gets, which is the answer
	// that does not tell a caller which of the two it was.
	//
	// It archives rather than deletes. The row is what a deployment reads to
	// answer "this person had a passkey here and revoked it on this date", which
	// is a question a security review asks and a DELETE makes unanswerable; and
	// the live-rows-only unique index means the archived row costs the person
	// nothing — the same authenticator can be enrolled again. A nil tx is an
	// error wrapping ErrNilExecutor.
	ArchiveCredentialForUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, credentialRowID, userID string) (*Credential, error)
}
