package passkeys

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer runs inside the transaction of each [Service]
// operation that writes.
//
// It is the seam an audit entry or an outbox event goes through. A passkey
// added to somebody's account and the record that one was added are one fact,
// so a hook that fails fails the operation, and the operation's write rolls
// back with it: no row, and nothing recorded about a row.
//
// It is one interface rather than a function type per operation so that a
// consumer's audit layer is one type. Embed [NoopHooks] and override what
// matters; a method added here later then does not break the embedder.
//
// # What belongs in one
//
// Writes that must land with the operation. Not an email, not an HTTP call,
// not anything slow: this runs with the transaction open. Publish to an outbox
// and let something else deliver it.
type Hooks interface {
	// AfterRegisterPasskey runs once a registration's credential is written,
	// on the transaction [Service.FinishRegistration] was handed, with the row
	// the write answered with.
	AfterRegisterPasskey(ctx context.Context, tx database.Tx, scope tenancy.Scope, credential *Credential) error

	// AfterArchivePasskey runs once a passkey is revoked, on the transaction
	// [Service.ArchiveCredential] was handed, with the row as the archive left
	// it — ArchivedAt set, and every descriptive field still there for an audit
	// entry to say which passkey it was.
	AfterArchivePasskey(ctx context.Context, tx database.Tx, scope tenancy.Scope, credential *Credential) error

	// AfterFailedPasskeyLogin runs once a login ceremony has been refused, on a
	// transaction [Service.FinishLogin] or [Service.FinishDiscoverableLogin]
	// opens for it — a refused login has no write of its own to join.
	//
	// It is where a lockout counter or a security alert goes. Its error is
	// returned in place of the refusal rather than beside it: a failed login
	// whose record could not be written is the deployment being unwell, and a
	// lockout that silently stopped counting is the failure worth surfacing.
	AfterFailedPasskeyLogin(ctx context.Context, tx database.Tx, scope tenancy.Scope, attempt *FailedLogin) error
}

// FailedLogin is what [Hooks.AfterFailedPasskeyLogin] is told about a refused
// login.
type FailedLogin struct {
	// Cause is why the ceremony was refused. It wraps ErrLoginFailed, and past
	// that is whatever refused it: a signature that did not verify, a
	// challenge nobody issued, ErrSignCountRegressed.
	Cause error
	// UserID is the account the attempt was made against, when one is known:
	// the owner of the username a named login gave, or of the handle a
	// discoverable assertion carried. It is empty when the attempt named
	// nobody this deployment has.
	UserID string
	// CredentialID is the authenticator's credential ID the response carried,
	// or nil when the response could not be parsed far enough to say.
	CredentialID []byte
}

// NoopHooks does nothing, and is what a [Service] built without [WithHooks]
// runs. It is also the type to embed in a [Hooks] that overrides some:
//
//	type auditHooks struct {
//		passkeys.NoopHooks
//
//		audit audit.Recorder
//	}
//
// Embedding it rather than implementing all three is what makes a method added
// to [Hooks] later additive: an embedder gains a no-op rather than a compile
// failure.
type NoopHooks struct{}

var _ Hooks = NoopHooks{}

// AfterRegisterPasskey implements [Hooks].
func (NoopHooks) AfterRegisterPasskey(context.Context, database.Tx, tenancy.Scope, *Credential) error {
	return nil
}

// AfterArchivePasskey implements [Hooks].
func (NoopHooks) AfterArchivePasskey(context.Context, database.Tx, tenancy.Scope, *Credential) error {
	return nil
}

// AfterFailedPasskeyLogin implements [Hooks].
func (NoopHooks) AfterFailedPasskeyLogin(context.Context, database.Tx, tenancy.Scope, *FailedLogin) error {
	return nil
}
