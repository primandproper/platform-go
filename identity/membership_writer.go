package identity

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's MembershipWriter: who belongs to an account and what they may
// do there.
var _ MembershipWriter = (*SQLStore)(nil)

// SetMembershipRoles replaces the roles a user holds in an account.
func (s *SQLStore) SetMembershipRoles(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID, accountID string,
	roles []string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "setting identity membership roles")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "setting identity membership roles")
	}

	// The ownership check, the membership read and the role rewrite are the
	// caller's one transaction: the clear and the inserts that follow it are a
	// role set held in two halves until they commit, and a caller reading the
	// membership between them would find it holding none.
	if err := s.requireRolesOrOwnership(ctx, tx, scope, userID, accountID, roles); err != nil {
		return op.Error(err, "setting identity membership roles")
	}

	membership, err := s.readMembership(ctx, tx, scope, userID, accountID)
	if err != nil {
		return op.Error(err, "setting identity membership roles")
	}

	if err = s.replaceRoles(ctx, tx, s.membershipRoleWrites(), membership.ID, roles); err != nil {
		return op.Error(err, "setting identity membership roles")
	}

	return nil
}

// requireRolesOrOwnership is SetMembershipRoles's emptiness rule, which has one
// exception and needs a read to know whether it applies.
//
// A membership with no roles is ordinarily a user who belongs to an account and
// may do nothing in it — an authorization bug at runtime rather than an empty
// slice somebody passed, and removing somebody is RemoveMembership. The
// exception is the account's owner, whose standing is the ownership itself:
// they are the one member who cannot be removed and the one every check that
// resolves through owner_user_id answers for, so a role set is something they
// may hold rather than something they need.
//
// It is here because the alternative was an asymmetry with no defensible half.
// TransferAccountOwnership mints the new owner a membership carrying no roles —
// it has none to give, and giving them one would invent a role name this
// package does not define — so refusing the same state here made the setter
// unable to produce a row the transfer creates, and left the invariant true of
// whichever door a caller happened to use.
func (s *SQLStore) requireRolesOrOwnership(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, accountID string,
	roles []string,
) error {
	if len(roles) > 0 {
		return nil
	}

	account, err := s.readAccount(ctx, q, scope, accountID)
	if err != nil {
		return err
	}

	if account.OwnerUserID != userID {
		return platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "membership must carry at least one role")
	}

	return nil
}

// SetDefaultAccount marks one of a user's accounts as the one they land in.
func (s *SQLStore) SetDefaultAccount(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID, accountID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "setting identity default account")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "setting identity default account")
	}

	// Both writes are the caller's one transaction, so "one default per user"
	// cannot be left half-applied: clearing without setting leaves the user with
	// none, and setting without clearing leaves them with two.
	count, err := s.writeDefaultAccountFlag(ctx, tx, scope, userID, accountID, true)
	if err = s.guardCount(ctx, count, err, ErrMembershipNotFound, "setting identity default account"); err != nil {
		return op.Error(err, "setting identity default account")
	}

	if err = s.clearDefaultAccountsForUser(ctx, tx, scope, userID, accountID); err != nil {
		return op.Error(err, "setting identity default account")
	}

	return nil
}

// TransferAccountOwnership moves an account to a new owner.
func (s *SQLStore) TransferAccountOwnership(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	accountID, newOwnerUserID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountIDKey, accountID),
		observability.WithValue(userIDKey, newOwnerUserID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "transferring identity account ownership")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "transferring identity account ownership")
	}

	if newOwnerUserID == "" {
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "no new owner named"),
			"transferring identity account ownership",
		)
	}

	account, err := s.readAccount(ctx, tx, scope, accountID)
	if err != nil {
		return op.Error(err, "transferring identity account ownership")
	}

	if account.OwnerUserID == newOwnerUserID {
		return nil
	}

	// owner_user_id carries no scope and no foreign key, so nothing below
	// this refuses a new owner from another directory: the membership branch
	// takes the id on faith and the SET stores it. An account owned by
	// somebody its own directory cannot read is an account whose every
	// ownership-derived permission check resolves to nobody, and the roster
	// that does display them displays a stranger. The scoped read is the
	// refusal, and it is here rather than only inside the membership write
	// because a new owner who already holds a membership would skip that
	// path entirely.
	if _, err = s.readUser(ctx, tx, scope, newOwnerUserID); err != nil {
		return op.Error(err, "transferring identity account ownership")
	}

	// The new owner gets a membership if they lack one. An owner who is not
	// a member is an account whose roster does not include the person
	// responsible for it, and every roster-driven permission check then
	// refuses them. An owner who already is one keeps the roles they have.
	//
	// The minted one carries no roles, and that is the state rather than a
	// gap in it: ownership is the standing, and a role invented here would
	// be a name this package does not define being written into somebody's
	// authorization. SetMembershipRoles admits the same state for the same
	// reason — see requireRolesOrOwnership — so the owner's role set is
	// reachable from both doors and refused for everybody else at both.
	switch _, readErr := s.readMembership(ctx, tx, scope, newOwnerUserID, accountID); {
	case readErr == nil:
	case errors.Is(readErr, ErrMembershipNotFound):
		// A first membership is its holder's default, here as at the other
		// two doors that mint one — see CreateMembership and
		// AcceptInvitation. A transfer to somebody who belonged to nothing
		// otherwise leaves them with a membership and nowhere to land, and
		// the failure surfaces as ErrNoDefaultAccount at their next sign-in
		// rather than at the transfer somebody else performed on them.
		//
		// It is the recipient's first membership anywhere that decides it,
		// not their first in this account: an owner who already belongs to
		// other accounts keeps the default they chose.
		existing, existsErr := s.hasLiveMembership(ctx, tx, scope, newOwnerUserID)
		if existsErr != nil {
			return op.Error(existsErr, "transferring identity account ownership")
		}

		// The membership it answers with is discarded: what this method
		// reports is the transfer, and the row it minted on the way is
		// described by the account that now names its holder as owner.
		if _, err = s.writeMembership(ctx, tx, &Membership{
			ID:               newID(""),
			Scope:            scope,
			BelongsToUser:    newOwnerUserID,
			BelongsToAccount: accountID,
			Roles:            []string{},
			DefaultAccount:   !existing,
		}); err != nil {
			return op.Error(err, "transferring identity account ownership")
		}
	default:
		return op.Error(readErr, "transferring identity account ownership")
	}

	// The owner being moved away from is in the predicate as well as the new
	// one in the SET, so two concurrent transfers cannot both succeed and
	// leave the account owned by whichever committed last: the second
	// matches nothing and reports zero rows.
	count, err := s.q.TransferAccountOwnership(ctx, tx, identitydb.TransferAccountOwnershipParams{
		ID:                 accountID,
		Scope:              scope,
		OwnerUserID:        newOwnerUserID,
		CurrentOwnerUserID: account.OwnerUserID,
	})
	if err = s.guardCount(ctx, count, err, ErrAccountNotFound, "transferring identity account ownership"); err != nil {
		return op.Error(err, "transferring identity account ownership")
	}

	return nil
}

// RemoveMembership ends a user's membership in an account.
func (s *SQLStore) RemoveMembership(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID, accountID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "removing identity membership")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "removing identity membership")
	}

	account, err := s.readAccount(ctx, tx, scope, accountID)
	if err != nil {
		return op.Error(err, "removing identity membership")
	}

	if account.OwnerUserID == userID {
		return op.Error(
			platformerrors.Wrapf(ErrLastAccountOwner, "account %q", accountID),
			"removing identity membership",
		)
	}

	// The flag comes off before the row is archived, because the statement that
	// clears it reaches live rows only — an archived membership is nobody's
	// default, and the soft delete stamps archived_at and nothing else, here as
	// everywhere else in this schema. Its row count says nothing the archival's
	// does not say better: a membership that was not the default is a row this
	// write correctly leaves alone.
	if _, err = s.writeDefaultAccountFlag(ctx, tx, scope, userID, accountID, false); err != nil {
		return op.Error(err, "removing identity membership")
	}

	count, err := s.q.ArchiveMembership(ctx, tx, identitydb.ArchiveMembershipParams{
		Scope:            scope,
		BelongsToUser:    userID,
		BelongsToAccount: accountID,
	})
	if err = s.guardCount(ctx, count, err, ErrMembershipNotFound, "removing identity membership"); err != nil {
		return op.Error(err, "removing identity membership")
	}

	// If that was the user's default, the default moves rather than vanishing —
	// a user with memberships and nowhere to land cannot build a Principal, and
	// the failure surfaces at their next request rather than at the removal that
	// caused it.
	if err = s.moveDefaultAccount(ctx, tx, scope, userID, accountID); err != nil {
		return op.Error(err, "removing identity membership")
	}

	return nil
}

// moveDefaultAccount points a user's default at another live membership, for
// when the membership that just ended was it. A user with no memberships left
// keeps none, which is the honest state rather than an invented one.
//
// Two writes end a membership and so two call this: RemoveMembership for the
// one member it removes, and ArchiveAccount once per member the account it is
// closing was the default of. It lives here rather than beside the second
// because the first is where the rule is stated — see Store.RemoveMembership —
// and one rule written twice is one that can come to differ.
func (s *SQLStore) moveDefaultAccount(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, removedAccountID string,
) error {
	fallback, readErr := s.q.GetMembershipFallbackAccountID(ctx, q, identitydb.GetMembershipFallbackAccountIDParams{
		Scope:            scope,
		BelongsToUser:    userID,
		BelongsToAccount: removedAccountID,
	})

	switch {
	case errors.Is(readErr, sql.ErrNoRows):
		return nil
	case readErr != nil:
		return platformerrors.Wrap(readErr, "reading identity fallback account")
	}

	if _, err := s.writeDefaultAccountFlag(ctx, q, scope, userID, fallback.BelongsToAccount, true); err != nil {
		return platformerrors.Wrap(err, "moving identity default account")
	}

	return nil
}

// writeDefaultAccountFlag assigns the flag on one live membership and hands
// back the row count rather than deciding what it means.
//
// The three callers read it differently. SetDefaultAccount treats zero as the
// membership not being there, which is how a stranger's account is answered.
// The removal and the fallback do not: the first is clearing a flag that may
// already be false, and the second names a membership it read out of this same
// transaction a statement ago.
func (s *SQLStore) writeDefaultAccountFlag(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, accountID string,
	isDefault bool,
) (int64, error) {
	count, err := s.q.SetMembershipDefaultAccount(ctx, q, identitydb.SetMembershipDefaultAccountParams{
		Scope:            scope,
		BelongsToUser:    userID,
		BelongsToAccount: accountID,
		DefaultAccount:   isDefault,
	})
	if err != nil {
		return 0, platformerrors.Wrap(err, "setting identity membership default account")
	}

	return count, nil
}
