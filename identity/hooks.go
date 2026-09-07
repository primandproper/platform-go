package identity

import (
	"context"

	"github.com/primandproper/platform-go/v14/database"
	"github.com/primandproper/platform-go/v14/tenancy"
)

// Hooks is what a consumer hangs off the Service's operations: one method per
// operation, each called inside the transaction that operation ran in.
//
// The transaction is the whole point. Every application that adopts this
// package has companions for an identity write — an audit entry, a data change
// event, a search index stamp, an outbox row — and those companions are the
// same fact as the row. A hook that ran after the commit would be a fact that
// can be missing from a database that says the registration happened; a hook
// that opened a transaction of its own would be that fact landing in a second
// one, which is the shape the module's store convention exists to rule out.
// So a hook receives the database.Tx and writes on it, and returning an error
// rolls the operation back — the row and its provenance commit together or
// neither does.
//
// What that costs is worth stating plainly, because it is not free: a hook runs
// with a write transaction held open. Work that is slow, that talks to a
// network, or that can fail for reasons the operation should survive does not
// belong here — it belongs behind an outbox row this hook writes. Sending the
// welcome email from AfterRegister makes the registration fail when the mail
// provider is down.
//
// Every method is "After": the operation's own writes have already run when it
// is called, and nothing here is a veto on policy. Whether a registration is
// allowed at all, whether an invitation may be sent, who may transfer an
// account — those are decisions a consumer makes before calling the Service,
// not inside it. A hook returning an error is an abort of a decision already
// taken, which is what makes it the right place for "record this" and the wrong
// place for "allow this".
//
// It is one interface rather than one function type per operation so that a
// consumer's audit layer is one type implementing what it needs. Embed
// NoopHooks and override the methods that matter; the rest then stay no-ops,
// and an operation added here later does not break the embedder.
type Hooks interface {
	// AfterRegister is called with the user, the account, and the owner
	// membership a registration minted, all three already written.
	AfterRegister(ctx context.Context, tx database.Tx, scope tenancy.Scope, registration *Registration) error

	// AfterInvite is called with the invitation that was issued — the caller's
	// own value, with its ID and CreatedAt filled in by the write.
	//
	// It is therefore the one invitation a hook sees carrying its Token, since
	// the caller minted it: the invitations the other three hooks receive were
	// read back by the Service and are redacted. A hook that queues the link
	// for mailing takes the token from here, and one that records the
	// invitation must not record the token with it.
	AfterInvite(ctx context.Context, tx database.Tx, scope tenancy.Scope, invitation *Invitation) error

	// AfterAcceptInvitation is called with the answered invitation and the
	// membership accepting it produced.
	AfterAcceptInvitation(ctx context.Context, tx database.Tx, scope tenancy.Scope, acceptance *Acceptance) error

	// AfterRejectInvitation is called with the invitation the recipient
	// declined, read back after the status write.
	AfterRejectInvitation(ctx context.Context, tx database.Tx, scope tenancy.Scope, invitation *Invitation) error

	// AfterCancelInvitation is called with the invitation the sender withdrew,
	// read back after the status write.
	AfterCancelInvitation(ctx context.Context, tx database.Tx, scope tenancy.Scope, invitation *Invitation) error

	// AfterTransferAccountOwnership is called with the account under its new
	// owner and the ID of the one it had before.
	//
	// The previous owner is an argument rather than something the hook could
	// read for itself, because by the time it runs the column holds the new
	// one. An audit entry for a transfer that cannot say who it came from is
	// not an audit entry for a transfer.
	AfterTransferAccountOwnership(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		account *Account,
		previousOwnerUserID string,
	) error

	// AfterSetDefaultAccount is called with the membership that is now the
	// user's default and the account ID that was, which is empty when the user
	// had none.
	AfterSetDefaultAccount(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		membership *Membership,
		previousAccountID string,
	) error

	// AfterArchiveUser is called with the user as they were read immediately
	// before the archival, and every membership the archival ended.
	//
	// Both are pre-state deliberately, and neither could be otherwise: an
	// archived user is not returned by any read here, and the memberships are
	// archived with them. A consumer removing the subject from the rosters it
	// keeps of its own needs the accounts they were on, and this is the last
	// call that can name them.
	AfterArchiveUser(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
		endedMemberships []*Membership,
	) error

	// AfterUpdateUserAccountStatus is called with the user under their new
	// status and the status they held before it.
	AfterUpdateUserAccountStatus(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
		previousStatus AccountStatus,
	) error

	// AfterSetUserServiceRoles is called with the user holding their new
	// service roles and the set they held before.
	//
	// This is the one operation in the module that grants or withdraws operator
	// access, so the before and after are both here: a record saying somebody
	// now holds a role is not the same record as one saying they gained it.
	AfterSetUserServiceRoles(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
		previousRoles []string,
	) error

	// AfterUpdateProfile is called with the user as they stand after the save
	// and the fields that actually moved, named rather than valued.
	//
	// The names and not the old values, because a profile save is the one write
	// here whose before-image is a privacy question of its own: an audit trail
	// that records what somebody's email address used to be is a second copy of
	// a personal detail, kept somewhere the erasure path does not reach. A
	// consumer that genuinely needs the old value reads it in the hook, on the
	// transaction, before this returns.
	AfterUpdateProfile(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
		changed []string,
	) error

	// AfterUpdateAccount is called with the account as it stands after the save
	// and the fields that moved.
	AfterUpdateAccount(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		account *Account,
		changed []string,
	) error

	// AfterRecordAgreement is called with the user and the documents they just
	// accepted, all stamped with one clock read.
	AfterRecordAgreement(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
		agreements []Agreement,
	) error

	// AfterSetMembershipRoles is called with the membership as it stands and
	// the roles it held before.
	//
	// Both sets, for the reason AfterSetUserServiceRoles gives: a record saying
	// somebody holds a role is not what an investigation wants. The one it
	// wants says they gained it, and when.
	AfterSetMembershipRoles(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		membership *Membership,
		previousRoles []string,
	) error

	// AfterRemoveMembership is called with the membership that was ended, as it
	// stood before it was, and the account the user's default moved to.
	//
	// The membership is read before the write because it cannot be read after:
	// an ended membership is returned by no read here, and a consumer keeping a
	// roster or a per-account projection needs to know which one to strike. The
	// destination is empty when the removal did not move a default — the
	// membership was not the user's landing account, or it was and there was
	// no other live one to land on.
	AfterRemoveMembership(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		membership *Membership,
		newDefaultAccountID string,
	) error
}

var _ Hooks = NoopHooks{}

// NoopHooks implements Hooks and does nothing.
//
// It is the Service's default, so a consumer with nothing to commit alongside
// an identity write configures nothing. It is also what a consumer with one or
// two hooks embeds:
//
//	type auditHooks struct {
//		identity.NoopHooks
//
//		audit audit.Recorder
//	}
//
//	func (h *auditHooks) AfterRegister(
//		ctx context.Context, tx database.Tx, scope tenancy.Scope, r *identity.Registration,
//	) error {
//		return h.audit.Record(ctx, tx, scope, "user.registered", r.User.ID)
//	}
//
// Embedding rather than implementing all fifteen is what keeps a method added to
// Hooks later from breaking every consumer — a new operation arrives as a
// no-op they can then choose to override.
type NoopHooks struct{}

// AfterRegister does nothing.
func (NoopHooks) AfterRegister(context.Context, database.Tx, tenancy.Scope, *Registration) error {
	return nil
}

// AfterInvite does nothing.
func (NoopHooks) AfterInvite(context.Context, database.Tx, tenancy.Scope, *Invitation) error {
	return nil
}

// AfterAcceptInvitation does nothing.
func (NoopHooks) AfterAcceptInvitation(context.Context, database.Tx, tenancy.Scope, *Acceptance) error {
	return nil
}

// AfterRejectInvitation does nothing.
func (NoopHooks) AfterRejectInvitation(context.Context, database.Tx, tenancy.Scope, *Invitation) error {
	return nil
}

// AfterCancelInvitation does nothing.
func (NoopHooks) AfterCancelInvitation(context.Context, database.Tx, tenancy.Scope, *Invitation) error {
	return nil
}

// AfterTransferAccountOwnership does nothing.
func (NoopHooks) AfterTransferAccountOwnership(
	context.Context, database.Tx, tenancy.Scope, *Account, string,
) error {
	return nil
}

// AfterSetDefaultAccount does nothing.
func (NoopHooks) AfterSetDefaultAccount(
	context.Context, database.Tx, tenancy.Scope, *Membership, string,
) error {
	return nil
}

// AfterArchiveUser does nothing.
func (NoopHooks) AfterArchiveUser(
	context.Context, database.Tx, tenancy.Scope, *User, []*Membership,
) error {
	return nil
}

// AfterUpdateUserAccountStatus does nothing.
func (NoopHooks) AfterUpdateUserAccountStatus(
	context.Context, database.Tx, tenancy.Scope, *User, AccountStatus,
) error {
	return nil
}

// AfterSetUserServiceRoles does nothing.
func (NoopHooks) AfterSetUserServiceRoles(
	context.Context, database.Tx, tenancy.Scope, *User, []string,
) error {
	return nil
}

// AfterUpdateProfile does nothing.
func (NoopHooks) AfterUpdateProfile(
	context.Context, database.Tx, tenancy.Scope, *User, []string,
) error {
	return nil
}

// AfterUpdateAccount does nothing.
func (NoopHooks) AfterUpdateAccount(
	context.Context, database.Tx, tenancy.Scope, *Account, []string,
) error {
	return nil
}

// AfterRecordAgreement does nothing.
func (NoopHooks) AfterRecordAgreement(
	context.Context, database.Tx, tenancy.Scope, *User, []Agreement,
) error {
	return nil
}

// AfterSetMembershipRoles does nothing.
func (NoopHooks) AfterSetMembershipRoles(
	context.Context, database.Tx, tenancy.Scope, *Membership, []string,
) error {
	return nil
}

// AfterRemoveMembership does nothing.
func (NoopHooks) AfterRemoveMembership(
	context.Context, database.Tx, tenancy.Scope, *Membership, string,
) error {
	return nil
}
