package identity

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's AdminWriter: the operator's half, whose exposure through an
// ordinary request handler is a privilege escalation.
var _ AdminWriter = (*SQLStore)(nil)

// UpdateUserAccountStatus moves a user between statuses.
func (s *SQLStore) UpdateUserAccountStatus(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
	status AccountStatus,
	explanation string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "updating identity account status")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "updating identity account status")
	}

	if !status.Valid() {
		return op.Error(
			platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "account status %q", status),
			"updating identity account status",
		)
	}

	count, err := s.q.UpdateUserAccountStatus(ctx, tx, identitydb.UpdateUserAccountStatusParams{
		ID:                       userID,
		Scope:                    scope,
		AccountStatus:            status.String(),
		AccountStatusExplanation: explanation,
	})
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "updating identity account status"); err != nil {
		return op.Error(err, "updating identity account status")
	}

	return nil
}

// SetUserServiceRoles replaces the roles a user holds outside any account.
//
// It replaces rather than merges, for the reason SetMembershipRoles does: a
// merging setter cannot revoke, and revocation is the operation that matters
// most on the role set that grants operator access.
func (s *SQLStore) SetUserServiceRoles(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
	roles []string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "setting identity service roles")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "setting identity service roles")
	}

	if slices.Contains(roles, "") {
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty service role name"),
			"setting identity service roles",
		)
	}

	// The existence check and the role write are the caller's one transaction.
	// Without the check, granting a role to a user ID that does not exist in this
	// scope writes rows nothing will ever read and reports success — and the
	// scope is the part that makes "does not exist" the common case rather than
	// a typo.
	if _, err := s.readUser(ctx, tx, scope, userID); err != nil {
		return op.Error(err, "setting identity service roles")
	}

	if err := s.replaceRoles(ctx, tx, s.userRoleWrites(), userID, roles); err != nil {
		return op.Error(err, "setting identity service roles")
	}

	return nil
}

// ArchiveUser soft-deletes a user and ends every membership they hold, refusing
// while they still own a live account, and answers with the user it hid.
//
// The row it hands back is the one no read here can reach afterwards. Every
// single-row statement over this table excludes archived rows, so once this
// commits the subject is absent from GetUser, from every list that does not ask
// for archived rows, and from every Principal — which makes this the last
// moment anything can describe who was removed. A consumer writing that down
// used to read the user first and describe them as they stood a statement
// earlier; what comes back here is the row as the archival left it, stamp
// included.
//
// It is read through a statement of its own, GetArchivedUser, because the
// ordinary read is precisely the one that cannot see the result — see
// readArchivedUser.
func (s *SQLStore) ArchiveUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return nil, op.Error(err, "archiving identity user")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "archiving identity user")
	}

	// The last-owner guard, which RemoveMembership has always had and this did
	// not. An owner archived out from under their accounts leaves them live and
	// answering to a user every scoped read now reports as absent, which is the
	// same ownerless account RemoveMembership refuses to create — reached
	// through a different door and discovered at the next permission check
	// rather than here. Transfer or archive the account first; both are one call
	// away, and neither can be reconstructed from the failure this otherwise
	// causes.
	owned, err := s.ownedAccountID(ctx, tx, scope, userID)
	if err != nil {
		return nil, op.Error(err, "archiving identity user")
	}

	if owned != "" {
		return nil, op.Error(platformerrors.Wrapf(ErrLastAccountOwner, "account %q", owned), "archiving identity user")
	}

	count, err := s.q.ArchiveUser(ctx, tx, identitydb.ArchiveUserParams{ID: userID, Scope: scope})
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "archiving identity user"); err != nil {
		return nil, op.Error(err, "archiving identity user")
	}

	// The memberships go in the caller's transaction with the archival. A user
	// archived with live memberships still appears on the rosters of the
	// accounts they belonged to, which is the state an application discovers
	// when a deleted colleague is still listed.
	//
	// The default flag comes off first, sparing no account, because the
	// statement that clears it reaches live rows only and every one of this
	// user's memberships is about to stop being one.
	if err = s.clearDefaultAccountsForUser(ctx, tx, scope, userID, ""); err != nil {
		return nil, op.Error(err, "archiving identity user")
	}

	if _, err = s.q.ArchiveMembershipsForUser(ctx, tx, identitydb.ArchiveMembershipsForUserParams{
		Scope:         scope,
		BelongsToUser: userID,
	}); err != nil {
		return nil, op.Error(platformerrors.Wrap(err, "archiving identity memberships"), "archiving identity user")
	}

	// After the memberships rather than between them and the row, so what comes
	// back describes a subject whose removal is complete rather than one halfway
	// through it. The memberships are not on a User, so the read is of the row
	// alone; a consumer that needs the rosters the subject just left reads them
	// before calling, which is the order the service above this one takes.
	archived, err := s.readArchivedUser(ctx, tx, scope, userID)
	if err != nil {
		return nil, op.Error(err, "archiving identity user")
	}

	return archived, nil
}

// ownedAccountID returns the id of one live account the user owns in this
// scope, or the empty string when they own none.
//
// It reads an id rather than asking whether one exists because the answer the
// caller needs is which account blocked: a refusal that says an account is in
// the way and cannot say which leaves the operator to find it, and the read
// costs the same either way.
func (s *SQLStore) ownedAccountID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) (string, error) {
	row, err := s.q.GetOwnedAccountIDForUser(ctx, q, identitydb.GetOwnedAccountIDForUserParams{
		Scope:       scope,
		OwnerUserID: userID,
	})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", platformerrors.Wrap(err, "reading the identity accounts a user owns")
	}

	return row.ID, nil
}

// EraseUser destroys the user row through the caller's transaction.
//
// Accounts the subject owned are left where they are, and this is the one place
// in this package where an ownerless account is a state a caller can reach:
// owner_user_id keeps naming an id that no longer exists anywhere, because an
// erasure cannot be refused the way ArchiveUser refuses. A right-to-be-forgotten
// transaction spans every domain and has to commit; a store that could decline
// it would make the subject's rights conditional on an account they may not even
// administer. So the guard sits on the path that has an alternative — archiving
// is refusable, and the refusal names the account — and this path documents what
// it leaves behind instead of inventing a resolution nobody asked for. Archiving
// the owned accounts here would take an account other members are still working
// in offline because one of them exercised a right; nulling the column is not
// open to it, since the column is NOT NULL and the sentinel that would fit is a
// user id no user has.
//
// What that means for a consumer wiring this into dataprivacy: resolve the
// subject's accounts before the erasure runs — transfer the ones with other
// members, archive the ones without — the same order ArchiveUser forces on the
// soft-delete path. See identity/migrations for why owner_user_id carries no
// REFERENCES clause when every other belongs-to column in this schema does.
func (s *SQLStore) EraseUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return 0, op.Error(err, "erasing identity user")
	}

	if err := scope.Validate(); err != nil {
		return 0, op.Error(err, "erasing identity user")
	}

	// The count comes from the generated :execrows statement, which reads it off
	// the driver and reports a refusal to supply one as an error. This used to
	// treat that refusal as a conservative zero — the erasure happened, only the
	// number was unavailable — and there is no seam left to do so: the Exec and
	// the RowsAffected are one call now, so a failure of either is one error.
	// Every driver this package supports reports the count for a DELETE, and an
	// erasure whose outcome is genuinely unknown is better rolled back by the
	// caller's transaction than reported as nothing destroyed.
	erased, err := s.q.EraseUser(ctx, tx, identitydb.EraseUserParams{ID: userID, Scope: scope})
	if err != nil {
		return 0, op.Error(err, "erasing identity user")
	}

	return erased, nil
}

// ArchiveAccount soft-deletes an account and ends every membership in it,
// answering with the account it hid.
//
// The row is reachable through no read here afterwards, for the reason
// ArchiveUser's is not, and it is the record of what the account was called and
// who owned it — which is what a consumer's entry, its billing reconciliation
// and its retention sweep are all written from.
func (s *SQLStore) ArchiveAccount(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	accountID string,
) (*Account, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return nil, op.Error(err, "archiving identity account")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "archiving identity account")
	}

	count, err := s.q.ArchiveAccount(ctx, tx, identitydb.ArchiveAccountParams{ID: accountID, Scope: scope})
	if err = s.guardCount(ctx, count, err, ErrAccountNotFound, "archiving identity account"); err != nil {
		return nil, op.Error(err, "archiving identity account")
	}

	// The memberships go with it, in the caller's transaction. Members left live
	// against an archived account keep it in their switcher and keep resolving
	// permissions through it.
	//
	// Who lands here has to be read before the flag comes off, since the clear
	// is what makes them unfindable. The rows are read rather than counted
	// because each stranded member's default moves to a membership of their own,
	// which is a different account per member.
	stranded, err := s.q.ListDefaultMembershipsForAccount(ctx, tx,
		identitydb.ListDefaultMembershipsForAccountParams{
			Scope:            scope,
			BelongsToAccount: accountID,
			DefaultAccount:   true,
		})
	if err != nil {
		return nil, op.Error(platformerrors.Wrap(err, "reading identity default memberships"), "archiving identity account")
	}

	// The default flag comes off first, for the reason ArchiveUser's does: the
	// clear reaches live rows only, and these are about to stop being live.
	if _, err = s.q.ClearMembershipDefaultAccountsForAccount(ctx, tx,
		identitydb.ClearMembershipDefaultAccountsForAccountParams{
			Scope:            scope,
			BelongsToAccount: accountID,
			DefaultAccount:   false,
		}); err != nil {
		return nil, op.Error(platformerrors.Wrap(err, "clearing identity default accounts"), "archiving identity account")
	}

	if _, err = s.q.ArchiveMembershipsForAccount(ctx, tx, identitydb.ArchiveMembershipsForAccountParams{
		Scope:            scope,
		BelongsToAccount: accountID,
	}); err != nil {
		return nil, op.Error(platformerrors.Wrap(err, "archiving identity memberships"), "archiving identity account")
	}

	// Each member who landed here now lands somewhere else they still belong,
	// which is the same move RemoveMembership makes for the one member it
	// removes — the account going away is that removal performed on everybody at
	// once, and a member with memberships and nowhere to land cannot build a
	// Principal. A member who belonged to nothing else keeps no default, which is
	// the honest state rather than an invented one: there is no membership left
	// to point at.
	for i := range stranded {
		if err = s.moveDefaultAccount(ctx, tx, scope, stranded[i].BelongsToUser, accountID); err != nil {
			return nil, op.Error(err, "archiving identity account")
		}
	}

	archived, err := s.readArchivedAccount(ctx, tx, scope, accountID)
	if err != nil {
		return nil, op.Error(err, "archiving identity account")
	}

	return archived, nil
}
