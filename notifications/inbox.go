package notifications

import (
	"context"

	"github.com/primandproper/platform-go/v14/notifications/internal/notificationsdb"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// The SQLStore's Inbox: what somebody was told, and whether they have read it.
var _ Inbox = (*SQLStore)(nil)

// CreateNotification files one notification through the caller's transaction and
// answers with the row it wrote.
//
// The read-back is a second round trip on a write path, and it is worth it:
// created_at is database-owned — see notifications/internal/queries — so the
// insert does not carry it, and a value assembled here would say 0001-01-01 for
// a row written a moment ago. A service that serializes what it just created
// straight into a response would render that as a date rather than as an
// absence. It is the whole row rather than the stamp alone, which costs the same
// round trip and answers with what the database holds instead of with what the
// caller assembled plus a timestamp. It runs on tx, so what it reads back is the
// row this transaction just wrote rather than one a commit has made visible.
//
// The caller's Notification is never written to. Everything this settles — the
// id, the scope, the stamp — is on the value it returns, which is the module's
// one spelling of what a write did; see [Inbox].
func (s *SQLStore) CreateNotification(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	notification *Notification,
) (*Notification, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "creating notification")
	}

	if notification == nil {
		return nil, op.Error(ErrNilNotification, "creating notification")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "creating notification")
	}

	// A copy, so the id this mints and the scope it settles land on the row that
	// is returned rather than on a value the caller still holds.
	filed := *notification

	if err := adoptScope(scope, &filed.Scope); err != nil {
		return nil, op.Error(err, "creating notification")
	}

	op.Set(principalKey, filed.Principal)

	if err := validNotification(&filed); err != nil {
		return nil, op.Error(err, "creating notification")
	}

	if filed.ID == "" {
		filed.ID = identifiers.New()
	}

	op.Set(notificationIDKey, filed.ID)

	if err := s.q.CreateNotification(ctx, tx,
		createNotificationParams(scope, &filed)); err != nil {
		return nil, op.Error(err, "creating notification")
	}

	// The ordinary keyed read reaches it: a row this transaction just inserted
	// is not archived, so the statement that excludes archived rows finds it.
	row, err := s.q.GetNotification(ctx, tx, notificationsdb.GetNotificationParams{
		ID:        filed.ID,
		Scope:     scope,
		Principal: filed.Principal,
	})
	if err != nil {
		return nil, op.Error(notFound(err, ErrNotificationNotFound),
			"reading back the filed notification")
	}

	return notificationFromRow(&row), nil
}

// GetNotification reads one of the principal's live notifications.
func (s *SQLStore) GetNotification(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	principal, notificationID string,
) (*Notification, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
		observability.WithValue(notificationIDKey, notificationID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading notification %q", notificationID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading notification %q", notificationID)
	}

	if principal == "" {
		return nil, op.Error(ErrEmptyPrincipal, "reading notification %q", notificationID)
	}

	row, err := s.q.GetNotification(ctx, q, notificationsdb.GetNotificationParams{
		ID:        notificationID,
		Scope:     scope,
		Principal: principal,
	})
	if err != nil {
		return nil, op.Error(notFound(err, ErrNotificationNotFound), "reading notification %q", notificationID)
	}

	return notificationFromRow(&row), nil
}

// ListNotifications pages the principal's inbox, in the direction the filter
// names.
func (s *SQLStore) ListNotifications(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	principal string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Notification], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing notifications")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing notifications")
	}

	if principal == "" {
		return nil, op.Error(ErrEmptyPrincipal, "listing notifications")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]notificationsdb.ListNotificationsRow, error) {
			return s.q.ListNotifications(ctx, q,
				listNotificationsParams(scope, principal, filter))
		},
		func() ([]notificationsdb.ListNotificationsDescendingRow, error) {
			return s.q.ListNotificationsDescending(ctx, q,
				notificationsdb.ListNotificationsDescendingParams(
					listNotificationsParams(scope, principal, filter)))
		},
		func(r notificationsdb.ListNotificationsDescendingRow) notificationsdb.ListNotificationsRow {
			return notificationsdb.ListNotificationsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing notifications")
	}

	rows := make([]pageRow[Notification], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, notificationPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return drainNotifications(rows, filter), nil
}

// ListUnreadNotifications is ListNotifications restricted to what the principal
// has not read.
//
// The count a caller wants beside the page — the badge number — is the result's
// filtered count, which the statement carries on its rows: it is of everything
// unread rather than of the page, so a client asking for one notification learns
// how many there are.
func (s *SQLStore) ListUnreadNotifications(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	principal string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Notification], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing unread notifications")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing unread notifications")
	}

	if principal == "" {
		return nil, op.Error(ErrEmptyPrincipal, "listing unread notifications")
	}

	filter = pageFilter(filter)

	params := func() notificationsdb.ListUnreadNotificationsParams {
		return notificationsdb.ListUnreadNotificationsParams(
			listNotificationsParams(scope, principal, filter))
	}

	listRows, err := sortedRows(filter,
		func() ([]notificationsdb.ListUnreadNotificationsRow, error) {
			return s.q.ListUnreadNotifications(ctx, q, params())
		},
		func() ([]notificationsdb.ListUnreadNotificationsDescendingRow, error) {
			return s.q.ListUnreadNotificationsDescending(ctx, q,
				notificationsdb.ListUnreadNotificationsDescendingParams(params()))
		},
		func(r notificationsdb.ListUnreadNotificationsDescendingRow) notificationsdb.ListUnreadNotificationsRow {
			return notificationsdb.ListUnreadNotificationsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing unread notifications")
	}

	rows := make([]pageRow[Notification], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, unreadPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return drainNotifications(rows, filter), nil
}

// drainNotifications is the one place a page of notifications becomes a result.
//
// The cursor is the id, because both statements order by it. A cursor naming a
// position in an order the query does not use is a page that skips rows and
// repeats others, with nothing reporting an error — so the two lists share this
// rather than each naming the field they page by.
func drainNotifications(
	rows []pageRow[Notification],
	filter *filtering.QueryFilter,
) *filtering.QueryFilteredResult[Notification] {
	return filtering.Drain(rows, pageValue, pageCounts,
		func(n *Notification) string { return n.ID }, filter)
}

// MarkNotificationRead stamps one notification as read, now, through the
// caller's transaction, and answers with the row as the statement left it.
//
// The statement guards on the stamp being absent, so a second mark matches
// nothing and the time the principal first read it survives. That leaves zero
// rows meaning two things — already read, or not in the inbox at all — which are
// different answers, and the read-back settles them both: a row that comes back
// is in this principal's inbox and carries whichever stamp it has, and a row
// that does not is ErrNotificationNotFound. The affected-row count is discarded
// because it distinguishes nothing the row does not; both of its outcomes are
// the success this method promises, and the stamp on the row is what says which
// one happened.
//
// It runs on tx: the notification this transaction just filed is one it can
// find, and the stamp it reads is the one this transaction just wrote.
func (s *SQLStore) MarkNotificationRead(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	principal, notificationID string,
) (*Notification, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
		observability.WithValue(notificationIDKey, notificationID),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "marking notification %q read", notificationID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "marking notification %q read", notificationID)
	}

	if principal == "" {
		return nil, op.Error(ErrEmptyPrincipal, "marking notification %q read", notificationID)
	}

	readAt := s.now()

	if _, err := s.q.MarkNotificationRead(ctx, tx,
		notificationsdb.MarkNotificationReadParams{
			ReadAt:    &readAt,
			ID:        notificationID,
			Scope:     scope,
			Principal: principal,
		}); err != nil {
		return nil, op.Error(err, "marking notification %q read", notificationID)
	}

	row, err := s.q.GetNotification(ctx, tx, notificationsdb.GetNotificationParams{
		ID:        notificationID,
		Scope:     scope,
		Principal: principal,
	})
	if err != nil {
		return nil, op.Error(notFound(err, ErrNotificationNotFound),
			"marking notification %q read", notificationID)
	}

	return notificationFromRow(&row), nil
}

// MarkAllNotificationsRead stamps everything the principal has not read through
// the caller's transaction, and reports how many that was.
func (s *SQLStore) MarkAllNotificationsRead(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	principal string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
	)
	defer op.End()

	if tx == nil {
		return 0, op.Error(ErrNilExecutor, "marking every notification read")
	}

	if err := scope.Validate(); err != nil {
		return 0, op.Error(err, "marking every notification read")
	}

	if principal == "" {
		return 0, op.Error(ErrEmptyPrincipal, "marking every notification read")
	}

	readAt := s.now()

	count, err := s.q.MarkAllNotificationsRead(ctx, tx,
		notificationsdb.MarkAllNotificationsReadParams{
			ReadAt:    &readAt,
			Scope:     scope,
			Principal: principal,
		})
	if err != nil {
		return 0, op.Error(err, "marking every notification read")
	}

	op.SpanOnly(countKey, count)

	return count, nil
}

// ArchiveNotification dismisses one notification through the caller's
// transaction and answers with the row it archived.
//
// Zero rows is ErrNotificationNotFound rather than a quiet success, and the
// reading is exact: the statement excludes archived rows, so a notification that
// has already been dismissed is not in the inbox, which is what this method
// addresses.
//
// The read-back cannot be GetNotification, for the same reason: that statement
// is written not to see archived rows, which is what this one just made this
// row. GetArchivedNotification is its complement — same projection, keyed the
// same way, asserting archived_at IS NOT NULL — so a guard that matched nothing
// cannot be read back as a success, and the row it returns is the one this
// transaction hid.
//
// It is the guard that decides the answer, not the read. A write that moved
// nothing is ErrNotificationNotFound before the read runs, so a notification
// somebody else archived is never reported as this call's — and an empty
// read-back after a guard that matched is left unmapped rather than folded into
// that sentinel, because the statement holds the row until commit and there is
// no state in which it is honestly absent: answering a broken invariant with
// that sentinel would report it to a consumer as a 404.
func (s *SQLStore) ArchiveNotification(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	principal, notificationID string,
) (*Notification, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
		observability.WithValue(notificationIDKey, notificationID),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "archiving notification %q", notificationID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "archiving notification %q", notificationID)
	}

	if principal == "" {
		return nil, op.Error(ErrEmptyPrincipal, "archiving notification %q", notificationID)
	}

	count, err := s.q.ArchiveNotification(ctx, tx,
		notificationsdb.ArchiveNotificationParams{
			ID:        notificationID,
			Scope:     scope,
			Principal: principal,
		})
	if guardErr := guardCount(count, err, ErrNotificationNotFound,
		"archiving the notification"); guardErr != nil {
		return nil, op.Error(guardErr, "archiving notification %q", notificationID)
	}

	archived, err := s.q.GetArchivedNotification(ctx, tx,
		notificationsdb.GetArchivedNotificationParams{
			ID:        notificationID,
			Scope:     scope,
			Principal: principal,
		})
	if err != nil {
		return nil, op.Error(err, "reading back the archived notification")
	}

	return archivedNotificationFromRow(&archived), nil
}

// adoptScope settles which tenant a write is for, and writes the answer back
// onto the entity.
//
// The scope the call named is the one the statement binds, so an entity that
// names a different one is refused rather than corrected — see
// [ErrScopeMismatch]. One that names none adopts the argument; tenancy.Scope
// tells the zero value apart from Global(), so "unset" here is genuinely unset
// rather than the global scope spelled shortly.
//
// It takes a pointer to the field rather than the entity, because the inbox and
// the registry hold two different types that agree on exactly this one thing.
func adoptScope(scope tenancy.Scope, carried *tenancy.Scope) error {
	if *carried != (tenancy.Scope{}) && *carried != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"entity names %q, the write names %q", *carried, scope)
	}

	*carried = scope

	return nil
}

// validNotification is what the inbox requires of a row before it stores one.
//
// Three checks, and each refuses a row that would be unreachable rather than
// merely odd: a notification addressed to nobody is one no list can find, one
// with no topic is one no client can decide what to do with, and one with no
// title is one a client renders as a blank line.
//
// The scope is not among them. It is checked on the argument before the entity
// is consulted at all — see adoptScope — because it is the write's rather than
// the row's.
func validNotification(n *Notification) error {
	if n.Principal == "" {
		return ErrEmptyPrincipal
	}

	if n.Topic == "" {
		return ErrEmptyTopic
	}

	if n.Title == "" {
		return platformerrors.Wrap(platformerrors.ErrEmptyInputProvided, "empty notification title")
	}

	return nil
}
