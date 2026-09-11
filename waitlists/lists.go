package waitlists

import (
	"context"

	"github.com/primandproper/platform-go/v14/waitlists/internal/waitlistsdb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's ListStore: the catalog, written by whoever administers a
// deployment and read by everything else.
var _ ListStore = (*SQLStore)(nil)

// CreateList opens a waitlist in the scope's catalog, through the caller's
// transaction — so the list commits with whatever the caller records about
// opening it. See [Store].
//
// The read-back of the creation time runs on tx as well. The column is the
// database's — see waitlists/internal/queries — so the alternative is a caller
// whose struct says 0001-01-01 for a row written a moment ago, and reading it
// anywhere but here would be reading a row this transaction has not committed.
func (s *SQLStore) CreateList(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	list *List,
) (*List, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "creating waitlist")
	}

	if list == nil {
		return nil, op.Error(ErrNilList, "creating waitlist")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "creating waitlist")
	}

	if err := matchScope(scope, list.Scope, "waitlist"); err != nil {
		return nil, op.Error(err, "creating waitlist")
	}

	if err := list.validate(); err != nil {
		return nil, op.Error(err, "creating waitlist %q", list.Name)
	}

	created := *list
	created.Scope = scope
	created.ClosesAt = created.ClosesAt.UTC()

	if created.ID == "" {
		created.ID = identifiers.New()
	}

	op.Set(listKey, created.ID)

	if err := s.q.CreateList(ctx, tx, createListParams(&created, scope)); err != nil {
		return nil, op.Error(err, "creating waitlist %q", created.ID)
	}

	row, err := s.q.GetListCreatedAt(ctx, tx, waitlistsdb.GetListCreatedAtParams{ID: created.ID})
	if err != nil {
		return nil, op.Error(err, "reading back the waitlist's creation time")
	}

	created.CreatedAt = row.CreatedAt.UTC()

	// Nothing has edited or archived a list that was written a moment ago,
	// and a caller who filled either in is a caller describing a row that does
	// not exist yet.
	created.LastUpdatedAt = nil
	created.ArchivedAt = nil

	return &created, nil
}

// GetList reads one of the scope's live lists by id, on the caller's executor.
func (s *SQLStore) GetList(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	listID string,
) (*List, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading waitlist %q", listID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading waitlist %q", listID)
	}

	list, err := s.readList(ctx, q, scope, listID)
	if err != nil {
		return nil, op.Error(err, "reading waitlist %q", listID)
	}

	return list, nil
}

// ListLists pages the scope's catalog, in the direction the filter names.
//
// The direction is a choice between two generated statements rather than an
// argument either of them binds — see sortedRows — so what this method does with
// filter.SortBy is pick the one whose ORDER BY and cursor comparison agree with
// it.
func (s *SQLStore) ListLists(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[List], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing waitlists")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing waitlists")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]waitlistsdb.ListListsRow, error) {
			return s.q.ListLists(ctx, q, listListsParams(scope, filter))
		},
		func() ([]waitlistsdb.ListListsDescendingRow, error) {
			return s.q.ListListsDescending(ctx, q,
				waitlistsdb.ListListsDescendingParams(listListsParams(scope, filter)))
		},
		func(r waitlistsdb.ListListsDescendingRow) waitlistsdb.ListListsRow {
			return waitlistsdb.ListListsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing waitlists")
	}

	rows := make([]pageRow[List], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, listPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	// The cursor is the id, because the statement orders by it. A cursor naming
	// a position in an order the query does not use is a page that skips rows
	// and repeats others, with nothing reporting an error.
	return filtering.Drain(rows, pageValue, pageCounts,
		func(l *List) string { return l.ID }, filter), nil
}

// ListOpenLists pages the lists still taking signups.
//
// The horizon is the store's clock rather than the server's, bound as an
// argument — see queries.OpenAsOfArg. That is what puts this read and
// List.OpenAt on the same clock: a test that moves the store's clock past a
// list's closing time sees the list leave this page, which it would not if the
// statement read CURRENT_TIMESTAMP.
func (s *SQLStore) ListOpenLists(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[List], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing open waitlists")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing open waitlists")
	}

	filter = pageFilter(filter)
	asOf := s.clock.Now().UTC()

	listRows, err := sortedRows(filter,
		func() ([]waitlistsdb.ListOpenListsRow, error) {
			return s.q.ListOpenLists(ctx, q, listOpenListsParams(scope, asOf, filter))
		},
		func() ([]waitlistsdb.ListOpenListsDescendingRow, error) {
			return s.q.ListOpenListsDescending(ctx, q,
				waitlistsdb.ListOpenListsDescendingParams(listOpenListsParams(scope, asOf, filter)))
		},
		func(r waitlistsdb.ListOpenListsDescendingRow) waitlistsdb.ListOpenListsRow {
			return waitlistsdb.ListOpenListsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing open waitlists")
	}

	rows := make([]pageRow[List], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, openListPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(l *List) string { return l.ID }, filter), nil
}

// UpdateList rewrites a list's name, description and closing time, through the
// caller's transaction. See [Store].
func (s *SQLStore) UpdateList(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	list *List,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "updating waitlist")
	}

	if list == nil {
		return op.Error(ErrNilList, "updating waitlist")
	}

	op.Set(listKey, list.ID)

	if err := scope.Validate(); err != nil {
		return op.Error(err, "updating waitlist")
	}

	if err := matchScope(scope, list.Scope, "waitlist"); err != nil {
		return op.Error(err, "updating waitlist")
	}

	if err := requireID(list.ID); err != nil {
		return op.Error(err, "updating waitlist")
	}

	if err := list.validate(); err != nil {
		return op.Error(err, "updating waitlist %q", list.ID)
	}

	count, err := s.q.UpdateList(ctx, tx, updateListParams(list, scope))

	return op.Error(guardCount(count, err, ErrListNotFound, "updating waitlist"), "updating waitlist")
}

// ArchiveList retires one of the scope's lists, through the caller's
// transaction. See [Store].
func (s *SQLStore) ArchiveList(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	listID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "archiving waitlist %q", listID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving waitlist %q", listID)
	}

	count, err := s.q.ArchiveList(ctx, tx, waitlistsdb.ArchiveListParams{ID: listID, Scope: scope})

	return op.Error(guardCount(count, err, ErrListNotFound, "archiving waitlist"),
		"archiving waitlist %q", listID)
}

// readList is the read by id, through whatever executor the caller is holding.
//
// It is the read Join makes inside the caller's transaction as well as the one
// GetList makes on whatever executor it was handed, which is what puts the "is
// this list open" decision on the same row the signup is written against.
func (s *SQLStore) readList(
	ctx context.Context,
	q waitlistsdb.DBTX,
	scope tenancy.Scope,
	listID string,
) (*List, error) {
	if err := requireID(listID); err != nil {
		return nil, err
	}

	row, err := s.q.GetList(ctx, q, waitlistsdb.GetListParams{ID: listID, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrListNotFound)
	}

	return listFromRow(&row), nil
}
