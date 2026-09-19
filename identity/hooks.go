package identity

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
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

	// AfterInvite is called with the invitation that was issued, as the write
	// wrote it: the row read back on this transaction, with the ID it minted
	// and the creation time the database stamped, rather than the value the
	// caller assembled.
	//
	// It is the one invitation a hook sees carrying its Token. The column holds
	// only a digest, so the secret is the one the caller minted, put back onto
	// this read-back — which is also not redacted, unlike the invitations the
	// other three hooks receive. A hook that queues the link for mailing takes
	// the token from here, and one that records the invitation must not record
	// the token with it.
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

	// AfterArchiveUser is called with the user as the archival left them,
	// redacted, and every membership the archival ended.
	//
	// The user is the row Store.ArchiveUser answered with, read through the one
	// statement that can see an archived row, so what a consumer records is the
	// subject as the write left them — archived_at included — rather than as
	// they stood a statement earlier.
	//
	// The memberships are pre-state and could not be otherwise: they are
	// archived with the user, and a consumer removing the subject from the
	// rosters it keeps of its own needs the accounts they were on. This is the
	// last call that can name them.
	AfterArchiveUser(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
		endedMemberships []*Membership,
	) error

	// AfterUpdateUserAccountStatus is called with the user under their new
	// status and the status they held before it.
	//
	// This is where a consumer revokes what a suspended user is still holding,
	// and it is the only place that can: identity holds no handle on a session or
	// on a refresh token and should not — each lives in a package with a store of
	// its own, and reaching across for one would be exactly the dependency a hook
	// exists to avoid. The two calls are
	// signin/refreshtokens.SQLStore.RevokeForSubject, which takes this tx and says
	// on itself that "disable this account" and the sign-out that goes with it are
	// one fact, and sessions.Store[T].RevokeAll over a sessions.Holder naming the
	// user, which cannot — a sessions store may be backed by Redis rather than by
	// this database, so that one runs outside the transaction and a delete it made
	// is not rolled back by returning an error here.
	//
	// It is not what makes the ban effective. Store.GetPrincipal refuses a user
	// whose status does not admit sign-in, so the next authenticated request on
	// any surface is already refused whether or not a consumer implements this —
	// what a revocation here buys is tidiness rather than safety. A live session
	// row for somebody who can no longer use it is a row on their own security
	// page, a row a "sign out everywhere" reports having ended, and a row the
	// sweeper carries until its absolute deadline.
	//
	// Which way to be wrong is the consumer's to choose, and the hook lets them
	// choose it. Returning an error refuses the status write, so a ban that could
	// not be made tidy does not land at all; swallowing it and writing an outbox
	// row on this tx keeps the ban and retries the revocation. What is not
	// available is both, for the session half: a revocation that has happened has
	// happened, and a commit that fails after it leaves somebody signed out of a
	// suspension the directory never recorded.
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

	// The credential hooks, one per write in [CredentialStore] — the half of
	// the directory the authentication engines write to.
	//
	// None of them sees a credential. The user each is handed is redacted, so
	// the hash, the TOTP secret and the verification token are gone before a
	// hook could record one, and that is the point rather than a limitation: a
	// hook is by definition something that writes elsewhere, and a live
	// second-factor secret or an unredeemed verification link written anywhere
	// twice is a credential with two holders. What is recordable is that the
	// credential moved, whose it was, and when — and the columns carrying those
	// three all survive redaction.
	//
	// Three of the seven take a second argument, and in each case it is the
	// value of a column the write cleared on its way past rather than as its
	// purpose: the forced-change flag UpdateUserPassword releases, and the
	// verification stamp the two issuing writes drop. A hook is told what it
	// could not have read for itself a statement later, and nothing else.

	// AfterUpdateUserPassword is called with the user whose password changed, as
	// the write left them and redacted, and whether a change had been forced on
	// them before it.
	//
	// Neither hash is here, the new one because it is a credential and the old
	// one because it is a credential somebody may still be trying. What the row
	// carries is PasswordLastChangedAt, stamped by this write, which is the fact
	// a record of a rotation is written from.
	//
	// previouslyRequiredChange is the flag the write released. It is an argument
	// because the write clears it, so a hook reading the row afterwards finds
	// false either way — and "the user completed a change an operator compelled"
	// is not the entry "the user rotated a password they still knew".
	AfterUpdateUserPassword(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
		previouslyRequiredChange bool,
	) error

	// AfterSetUserRequiresPasswordChange is called with the user an operator
	// forced or released a password change on, read back after the write and
	// redacted.
	//
	// No before-image, unlike the two role setters. The row carries
	// RequiresPasswordChange and this write assigned it, so what the hook reads
	// is what the operator chose; a bool's previous value would let an entry say
	// the requirement was already in force, which is not what the requirement
	// being imposed is a record of.
	AfterSetUserRequiresPasswordChange(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *User) error

	// AfterUpdateUserTwoFactorSecret is called with the user who was issued a
	// new second-factor secret, redacted, and the moment the secret it replaced
	// had been proven.
	//
	// The new secret is unproven — the store will not be told otherwise — so
	// between this and AfterMarkUserTwoFactorSecretVerified the user holds no
	// second factor at all.
	//
	// previousSecretVerifiedAt is the proof the write dropped, and is nil for a
	// user who never demonstrated possession of the secret they held. It is what
	// separates a consumer alerting on "a proven second factor was replaced"
	// from one watching an enrollment get restarted, which are the same row and
	// very different events.
	AfterUpdateUserTwoFactorSecret(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
		previousSecretVerifiedAt *time.Time,
	) error

	// AfterMarkUserTwoFactorSecretVerified is called with the user who proved
	// possession of the secret they hold, redacted.
	//
	// It is the row the write answered with, so TwoFactorSecretVerifiedAt holds
	// the moment being recorded rather than whatever it held a statement
	// earlier. A replayed verification never reaches here at all: the write
	// matches nothing and the operation fails with ErrUserNotFound.
	AfterMarkUserTwoFactorSecretVerified(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
	) error

	// AfterSetUserEmailAddressVerificationToken is called with the user a
	// verification link was minted for, read back after the write and redacted,
	// and the moment their address had last been proven.
	//
	// The token is not here, and the mail is not sent from here either — see
	// what this interface says a held-open transaction costs. What belongs in
	// the hook is the outbox row the mail is sent from, and the token belongs to
	// whoever minted it, which is the caller.
	//
	// previousAddressVerifiedAt is the proof the write dropped, because a row
	// may not say both "proven" and "a link is outstanding". It is nil for the
	// ordinary case, a link minted for an address nobody has proven yet, and set
	// for the one worth alerting on: an address that was proven and now is not.
	AfterSetUserEmailAddressVerificationToken(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
		previousAddressVerifiedAt *time.Time,
	) error

	// AfterMarkUserEmailAddressVerified is called with the user whose address is
	// now proven, read back after the write and redacted.
	//
	// The address on that row is the fact worth recording: the column this write
	// stamps says when something was proven and never which address it was. Two
	// clicks on one link reach here once, since the second finds the token
	// already burned.
	AfterMarkUserEmailAddressVerified(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
	) error

	// AfterMarkUserEmailAddressUnverified is called with the user whose address
	// stopped being proven, as the write answered with them, redacted.
	//
	// No previous stamp, unlike the two writes that drop a proof on the way to
	// doing something else: withdrawing it is what this write is for, and what a
	// record of that needs is the address the proof was withdrawn from rather
	// than the moment it was made. That address is on the row, which is the
	// reason the store answers with one.
	AfterMarkUserEmailAddressUnverified(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		user *User,
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
// Embedding rather than implementing all twenty-two is what keeps a method added
// to Hooks later from breaking every consumer — a new operation arrives as a
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

// AfterUpdateUserAccountStatus does nothing, so a ban leaves whatever sessions
// and refresh-token families the user held live until their own deadlines. The
// ban itself is still effective on the next request — Store.GetPrincipal refuses
// it — and the interface's doc names the two calls that clear the rows.
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

// AfterUpdateUserPassword does nothing.
func (NoopHooks) AfterUpdateUserPassword(
	context.Context, database.Tx, tenancy.Scope, *User, bool,
) error {
	return nil
}

// AfterSetUserRequiresPasswordChange does nothing.
func (NoopHooks) AfterSetUserRequiresPasswordChange(
	context.Context, database.Tx, tenancy.Scope, *User,
) error {
	return nil
}

// AfterUpdateUserTwoFactorSecret does nothing.
func (NoopHooks) AfterUpdateUserTwoFactorSecret(
	context.Context, database.Tx, tenancy.Scope, *User, *time.Time,
) error {
	return nil
}

// AfterMarkUserTwoFactorSecretVerified does nothing.
func (NoopHooks) AfterMarkUserTwoFactorSecretVerified(
	context.Context, database.Tx, tenancy.Scope, *User,
) error {
	return nil
}

// AfterSetUserEmailAddressVerificationToken does nothing.
func (NoopHooks) AfterSetUserEmailAddressVerificationToken(
	context.Context, database.Tx, tenancy.Scope, *User, *time.Time,
) error {
	return nil
}

// AfterMarkUserEmailAddressVerified does nothing.
func (NoopHooks) AfterMarkUserEmailAddressVerified(
	context.Context, database.Tx, tenancy.Scope, *User,
) error {
	return nil
}

// AfterMarkUserEmailAddressUnverified does nothing.
func (NoopHooks) AfterMarkUserEmailAddressUnverified(
	context.Context, database.Tx, tenancy.Scope, *User,
) error {
	return nil
}
