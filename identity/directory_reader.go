package identity

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/querygen"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's DirectoryReader: users, accounts, and the memberships between
// them, read and never written.
var _ DirectoryReader = (*SQLStore)(nil)

// GetUser reads one of the scope's live users.
func (s *SQLStore) GetUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "reading identity user %q", userID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading identity user %q", userID)
	}

	user, err := s.readUser(ctx, q, scope, userID)
	if err != nil {
		return nil, op.Error(err, "reading identity user %q", userID)
	}

	return user, nil
}

// readUser is the read by id, through whatever executor the caller is holding.
//
// It excludes archived users, which the statement it used to run did not. That
// is querygen's single-row read rather than a decision taken here: reading one
// row by id is not a filtered list, and a caller who wants an archived user
// back wants a different query rather than a flag on this one. It is a
// consumer-visible change and it is in the release notes.
func (s *SQLStore) readUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) (*User, error) {
	row, err := s.q.GetUser(ctx, q, identitydb.GetUserParams{ID: userID, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrUserNotFound)
	}

	user := userFromRow(&row)

	if err = s.attachServiceRoles(ctx, q, []*User{user}); err != nil {
		return nil, err
	}

	return user, nil
}

// readArchivedUser is the read an archival answers with: the user the write it
// just made hid, on the transaction that hid it.
//
// It is not on Store and no consumer reaches it. readUser cannot serve — every
// single-row statement over this table filters archived_at IS NULL, which is
// what makes an archived user absent from the directory — so the one row that
// describes what an archival did is the one row the ordinary read cannot see.
// The statement carries that predicate's complement, so a read that comes back
// is proof the archival landed rather than proof a row exists.
//
// The service roles come with it, from the same batched read every other user
// read uses. A role grant is not archived with its owner — the role tables carry
// no archived column, because a grant is reached through the parent whose own
// statements are all keyed on one — so the roles the archived user held are
// still readable, and a caller handed a User with an empty ServiceRoles it never
// lost would be reading a stamp as a revocation.
func (s *SQLStore) readArchivedUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) (*User, error) {
	row, err := s.q.GetArchivedUser(ctx, q, identitydb.GetArchivedUserParams{ID: userID, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrUserNotFound)
	}

	user := userFromArchivedRow(&row)

	if err = s.attachServiceRoles(ctx, q, []*User{user}); err != nil {
		return nil, err
	}

	return user, nil
}

// ListUsers pages the scope's directory, in the direction the filter names.
//
// The direction is a choice between two generated statements rather than an
// argument either of them binds — see sortedRows — so what this method does
// with filter.SortBy is pick the one whose ORDER BY and cursor comparison agree
// with it.
func (s *SQLStore) ListUsers(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[User], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "listing identity users")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing identity users")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]identitydb.ListUsersRow, error) {
			return s.q.ListUsers(ctx, q, listUsersParams(scope, filter))
		},
		func() ([]identitydb.ListUsersDescendingRow, error) {
			return s.q.ListUsersDescending(ctx, q,
				identitydb.ListUsersDescendingParams(listUsersParams(scope, filter)))
		},
		func(r identitydb.ListUsersDescendingRow) identitydb.ListUsersRow {
			return identitydb.ListUsersRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing identity users")
	}

	rows := make([]pageRow[User], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, userPageRow(&listRows[i]))
	}

	if err = s.hydrateUsers(ctx, q, pageValues(rows)); err != nil {
		return nil, op.Error(err, "listing identity users")
	}

	op.SpanOnly(countKey, len(rows))

	// The cursor is the id, because the statement orders by it. A cursor naming
	// a position in an order the query does not use is a page that skips rows
	// and repeats others, with nothing reporting an error.
	return filtering.Drain(rows, pageValue, pageCounts,
		func(u *User) string { return u.ID }, filter), nil
}

// ListUsersByIDs reads a batch of the scope's users in one query.
//
// Archived users are among them, and that is the read's purpose rather than an
// oversight: a caller hydrating references is naming users that other rows
// already point at, and hiding a soft-deleted one turns "created by a departed
// colleague" into "created by nobody". The statement says so by being rendered
// from a column list without archived_at in it, while still projecting the
// column — see identity/internal/queries.
func (s *SQLStore) ListUsersByIDs(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userIDs []string,
) ([]*User, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "reading identity users by ID")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading identity users by ID")
	}

	// An empty batch is an empty answer without a query: the statement the
	// corpus carries has no rendering of an empty set, and sending one anyway
	// is a round trip whose answer was known before it left — see
	// querygen.Generator.SetReadQuery, which documents the contract this keeps.
	if len(userIDs) == 0 {
		return []*User{}, nil
	}

	rows, err := s.q.ListUsersByIDs(ctx, q, identitydb.ListUsersByIDsParams{
		Scope: scope,
		IDs:   userIDs,
	})
	if err != nil {
		return nil, op.Error(err, "reading identity users by ID")
	}

	users := make([]*User, 0, len(rows))
	for i := range rows {
		users = append(users, userFromBatchRow(&rows[i]))
	}

	if err = s.hydrateUsers(ctx, q, users); err != nil {
		return nil, op.Error(err, "reading identity user service roles")
	}

	op.SpanOnly(countKey, len(users))

	return users, nil
}

// SearchUsersByUsername pages the scope's users whose username begins with
// prefix.
//
// The prefix is a literal somebody typed, and what the statement binds is
// querygen.PrefixPattern's rendering of it — the wildcards escaped, a trailing
// one appended. Without that a typed % or _ is a wildcard rather than a
// character, and the directory comes back for a prefix of "%", which reads as a
// working search returning too much rather than as a bug.
//
// The page and the count are two statements rather than one carrying the other,
// unlike the rendered lists here whose counts ride along on their rows. The
// count answers how many usernames the prefix matched, which does not move as
// the caller pages, while the page is cut by a cursor over the column it is
// ordered by. Both come from one call in identity/internal/queries — see
// querygen.Generator.PrefixSearchQueries.
//
// The filter's direction is honored here too, and what it names is this read's
// own order rather than creation order: the page is ordered by the username, so
// the descending half walks the alphabet backwards. That is the only reading
// available to a read whose cursor is not an id, and answering it in the
// direction nobody asked for would be the same silent wrong order a list would
// have. The count is one statement either way, since a count does not depend on
// the order its rows would have arrived in.
func (s *SQLStore) SearchUsersByUsername(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	prefix string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[User], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "searching identity users")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "searching identity users")
	}

	filter = pageFilter(filter)

	// Escaped once and bound twice, so the page and the number describing it
	// cannot come to search for different things.
	pattern := querygen.PrefixPattern(prefix)

	searchRows, err := sortedRows(filter,
		func() ([]identitydb.SearchUsersByUsernameRow, error) {
			return s.q.SearchUsersByUsername(ctx, q, searchUsersParams(scope, pattern, filter))
		},
		func() ([]identitydb.SearchUsersByUsernameDescendingRow, error) {
			return s.q.SearchUsersByUsernameDescending(ctx, q,
				identitydb.SearchUsersByUsernameDescendingParams(searchUsersParams(scope, pattern, filter)))
		},
		func(r identitydb.SearchUsersByUsernameDescendingRow) identitydb.SearchUsersByUsernameRow {
			return identitydb.SearchUsersByUsernameRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "searching identity users")
	}

	users := make([]*User, 0, len(searchRows))
	for i := range searchRows {
		users = append(users, userFromSearchRow(&searchRows[i]))
	}

	if err = s.hydrateUsers(ctx, q, users); err != nil {
		return nil, op.Error(err, "searching identity user service roles")
	}

	count, err := s.q.CountSearchUsersByUsername(ctx, q, countSearchUsersParams(scope, pattern))
	if err != nil {
		return nil, op.Error(err, "counting identity users")
	}

	op.SpanOnly(countKey, len(users))

	// The cursor is the username, because the statement orders by it. The
	// counts are their own statement rather than riding on the rows, so an
	// empty page still reports them — the ambiguity filtering.Drain exists to
	// avoid does not arise here.
	return filtering.NewQueryFilteredResult(
		users, uint64(len(users)), countOf(count.Count),
		func(u *User) string { return u.Username },
		filter,
	), nil
}

// hydrateUsers attaches a page's service roles and redacts every user in it.
//
// Both halves are here rather than at each call site because both are rules
// that can be got wrong twice, and the redaction is the one that matters: a
// page read is where a password hash escapes in bulk, and the one list method
// that forgot would look identical to the ones that did not.
//
// It redacts through the pointer rather than returning a new slice, because its
// two callers hold the users differently — one has a plain slice, the other has
// them inside the rows carrying the page's counts — and a rule that returned a
// second slice would leave the caller with the counts holding the unredacted
// copies.
func (s *SQLStore) hydrateUsers(ctx context.Context, q database.SQLQueryExecutor, users []*User) error {
	if err := s.attachServiceRoles(ctx, q, users); err != nil {
		return err
	}

	for _, user := range users {
		*user = *user.Redacted()
	}

	return nil
}

// GetAccount reads one of the scope's live accounts.
func (s *SQLStore) GetAccount(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	accountID string,
) (*Account, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "reading identity account %q", accountID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading identity account %q", accountID)
	}

	account, err := s.readAccount(ctx, q, scope, accountID)
	if err != nil {
		return nil, op.Error(err, "reading identity account %q", accountID)
	}

	return account, nil
}

// readAccount is the read by id, through whatever executor the caller is
// holding.
//
// Like readUser it excludes archived rows where the statement it replaced did
// not — see there.
func (s *SQLStore) readAccount(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	accountID string,
) (*Account, error) {
	row, err := s.q.GetAccount(ctx, q, identitydb.GetAccountParams{ID: accountID, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrAccountNotFound)
	}

	return accountFromRow(&row), nil
}

// readArchivedAccount is readArchivedUser for the other noun, and exists for the
// same reason: the account an archival hid is reachable through no read a
// consumer has, and the statement's archived_at IS NOT NULL is what makes the
// row that comes back proof the archival landed.
func (s *SQLStore) readArchivedAccount(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	accountID string,
) (*Account, error) {
	row, err := s.q.GetArchivedAccount(ctx, q, identitydb.GetArchivedAccountParams{ID: accountID, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrAccountNotFound)
	}

	return accountFromArchivedRow(&row), nil
}

// ListAccounts pages the scope's accounts, in the direction the filter names.
func (s *SQLStore) ListAccounts(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Account], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "listing identity accounts")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing identity accounts")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]identitydb.ListAccountsRow, error) {
			return s.q.ListAccounts(ctx, q, listAccountsParams(scope, filter))
		},
		func() ([]identitydb.ListAccountsDescendingRow, error) {
			return s.q.ListAccountsDescending(ctx, q,
				identitydb.ListAccountsDescendingParams(listAccountsParams(scope, filter)))
		},
		func(r identitydb.ListAccountsDescendingRow) identitydb.ListAccountsRow {
			return identitydb.ListAccountsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing identity accounts")
	}

	rows := make([]pageRow[Account], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, accountPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(a *Account) string { return a.ID }, filter), nil
}

// ListAccountsForUser pages the accounts a user is a live member of, in the
// direction the filter names.
func (s *SQLStore) ListAccountsForUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Account], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "listing identity accounts for user")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing identity accounts for user")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]identitydb.ListAccountsForUserRow, error) {
			return s.q.ListAccountsForUser(ctx, q, listAccountsForUserParams(scope, userID, filter))
		},
		func() ([]identitydb.ListAccountsForUserDescendingRow, error) {
			return s.q.ListAccountsForUserDescending(ctx, q,
				identitydb.ListAccountsForUserDescendingParams(listAccountsForUserParams(scope, userID, filter)))
		},
		func(r identitydb.ListAccountsForUserDescendingRow) identitydb.ListAccountsForUserRow {
			return identitydb.ListAccountsForUserRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing identity accounts for user")
	}

	rows := make([]pageRow[Account], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, accountPageRowForUser(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(a *Account) string { return a.ID }, filter), nil
}

// GetMembership reads the live membership between a user and an account.
func (s *SQLStore) GetMembership(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID,
	accountID string,
) (*Membership, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "reading identity membership")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading identity membership")
	}

	membership, err := s.readMembership(ctx, q, scope, userID, accountID)
	if err != nil {
		return nil, op.Error(err, "reading identity membership")
	}

	if err = s.attachMembershipRoles(ctx, q, []*Membership{membership}); err != nil {
		return nil, op.Error(err, "reading identity membership roles")
	}

	return membership, nil
}

// ListMembershipsForUser returns every live membership a user holds, default
// account first.
func (s *SQLStore) ListMembershipsForUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) ([]*Membership, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "listing identity memberships")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing identity memberships")
	}

	memberships, err := s.readMembershipsForUser(ctx, q, scope, userID)
	if err != nil {
		return nil, op.Error(err, "listing identity memberships")
	}

	op.SpanOnly(countKey, len(memberships))

	return memberships, nil
}

// ListAccountMembers pages an account's roster, in the direction the filter
// names.
//
// The roster is a page of memberships with the member attached, so the
// direction reverses the memberships — the cursor walks their ids, and the
// user's columns arrive beside whichever page that produces.
func (s *SQLStore) ListAccountMembers(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	accountID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[MembershipWithUser], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "listing identity account members")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing identity account members")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]identitydb.ListAccountMembersRow, error) {
			return s.q.ListAccountMembers(ctx, q, listAccountMembersParams(scope, accountID, filter))
		},
		func() ([]identitydb.ListAccountMembersDescendingRow, error) {
			return s.q.ListAccountMembersDescending(ctx, q,
				identitydb.ListAccountMembersDescendingParams(listAccountMembersParams(scope, accountID, filter)))
		},
		func(r identitydb.ListAccountMembersDescendingRow) identitydb.ListAccountMembersRow {
			return identitydb.ListAccountMembersRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing identity account members")
	}

	rows := make([]pageRow[MembershipWithUser], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, memberPageRow(&listRows[i]))
	}

	members := pageValues(rows)

	memberships := make([]*Membership, 0, len(members))
	for _, member := range members {
		memberships = append(memberships, &member.Membership)
	}

	if err = s.attachMembershipRoles(ctx, q, memberships); err != nil {
		return nil, op.Error(err, "listing identity account member roles")
	}

	op.SpanOnly(countKey, len(rows))

	// The cursor is the membership id, because the statement orders by it — the
	// roster is a page of memberships with the member attached, not a page of
	// users.
	return filtering.Drain(rows, pageValue, pageCounts,
		func(m *MembershipWithUser) string { return m.ID }, filter), nil
}
