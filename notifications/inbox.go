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
// reads back the creation time the database assigned.
//
// The read-back is a second round trip on a write path, and it is worth it:
// created_at is database-owned — see notifications/internal/queries — so the
// insert does not carry it, and the alternative is a value whose CreatedAt says
// 0001-01-01 for a row written a moment ago. A service that serializes what it
// just created straight into a response would render that as a date rather than
// as an absence. It runs on tx, so what it reads back is the row this
// transaction just wrote rather than one a commit has made visible.
func (s *SQLStore) CreateNotification(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	notification *Notification,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "creating notification")
	}

	if notification == nil {
		return op.Error(ErrNilNotification, "creating notification")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "creating notification")
	}

	if err := adoptScope(scope, &notification.Scope); err != nil {
		return op.Error(err, "creating notification")
	}

	op.Set(principalKey, notification.Principal)

	if err := validNotification(notification); err != nil {
		return op.Error(err, "creating notification")
	}

	if notification.ID == "" {
		notification.ID = identifiers.New()
	}

	op.Set(notificationIDKey, notification.ID)

	if err := s.q.CreateNotification(ctx, tx,
		createNotificationParams(scope, notification)); err != nil {
		return op.Error(err, "creating notification")
	}

	created, err := s.q.GetNotificationCreatedAt(ctx, tx,
		notificationsdb.GetNotificationCreatedAtParams{
			ID:        notification.ID,
			Scope:     scope,
			Principal: notification.Principal,
		})
	if err != nil {
		return op.Error(err, "reading back the notification's creation time")
	}

	notification.CreatedAt = created.CreatedAt.UTC()

	return nil
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
// caller's transaction.
//
// The statement guards on the stamp being absent, so a second mark matches
// nothing and the time the principal first read it survives. That leaves zero
// rows meaning two things — already read, or not in the inbox at all — and they
// are different answers, so the miss is disambiguated with a read rather than
// collapsed. It costs a round trip only on the path that already did nothing,
// and it runs on tx: the notification this transaction just filed is one it can
// find.
func (s *SQLStore) MarkNotificationRead(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	principal, notificationID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
		observability.WithValue(notificationIDKey, notificationID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "marking notification %q read", notificationID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "marking notification %q read", notificationID)
	}

	if principal == "" {
		return op.Error(ErrEmptyPrincipal, "marking notification %q read", notificationID)
	}

	readAt := s.now()

	count, err := s.q.MarkNotificationRead(ctx, tx,
		notificationsdb.MarkNotificationReadParams{
			ReadAt:    &readAt,
			ID:        notificationID,
			Scope:     scope,
			Principal: principal,
		})
	if err != nil {
		return op.Error(err, "marking notification %q read", notificationID)
	}

	if count > 0 {
		return nil
	}

	// Nothing was written. Either it was already read — in which case this is
	// the success the caller asked for — or the notification is not in this
	// principal's inbox, which is the error they need.
	if _, err = s.GetNotification(ctx, tx, scope, principal, notificationID); err != nil {
		return op.Error(err, "marking notification %q read", notificationID)
	}

	return nil
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
// transaction.
//
// Zero rows is ErrNotificationNotFound rather than a quiet success, and the
// reading is exact: the statement excludes archived rows, so a notification that
// has already been dismissed is not in the inbox, which is what this method
// addresses.
func (s *SQLStore) ArchiveNotification(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	principal, notificationID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
		observability.WithValue(notificationIDKey, notificationID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "archiving notification %q", notificationID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving notification %q", notificationID)
	}

	if principal == "" {
		return op.Error(ErrEmptyPrincipal, "archiving notification %q", notificationID)
	}

	count, err := s.q.ArchiveNotification(ctx, tx,
		notificationsdb.ArchiveNotificationParams{
			ID:        notificationID,
			Scope:     scope,
			Principal: principal,
		})

	return op.Error(
		guardCount(count, err, ErrNotificationNotFound, "archiving the notification"),
		"archiving notification %q", notificationID)
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
