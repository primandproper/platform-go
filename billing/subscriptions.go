package billing

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/billing/internal/billingdb"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's SubscriptionStore: the recurring half, written mostly from a
// payment provider's events.
var _ SubscriptionStore = (*SQLStore)(nil)

// CreateSubscription opens an agreement in the scope, through the caller's
// transaction.
//
// Every statement runs on tx — including the product check the write is gated
// on, so a product created through CreateProduct earlier in the same transaction
// is one a subscription can be opened against. The attribution read on the
// losing path runs there too; see [Store.CreateSubscription].
func (s *SQLStore) CreateSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subscription *Subscription,
) (*Subscription, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "creating subscription")
	}

	if subscription == nil {
		return nil, op.Error(ErrNilSubscription, "creating subscription")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "creating subscription")
	}

	created := *subscription
	created.Scope = scope
	created.CurrentPeriodStart = created.CurrentPeriodStart.UTC()
	created.CurrentPeriodEnd = created.CurrentPeriodEnd.UTC()

	if err := created.validate(); err != nil {
		return nil, op.Error(err, "creating subscription")
	}

	if created.ID == "" {
		created.ID = identifiers.New()
	}

	op.Set(subscriptionKey, created.ID)
	op.Set(accountKey, created.BelongsToAccount)

	if err := s.insertSubscription(ctx, tx, scope, &created); err != nil {
		return nil, op.Error(err, "creating subscription")
	}

	return &created, nil
}

// insertSubscription is the statements the create runs: the product check, the
// insert-ignore, the attribution of a loss, and the read-back of the creation
// time onto created.
func (s *SQLStore) insertSubscription(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	created *Subscription,
) error {
	if err := s.requireProduct(ctx, q, scope, created.ProductID); err != nil {
		return err
	}

	count, err := s.q.CreateSubscription(ctx, q, createSubscriptionParams(created, scope))
	if err != nil {
		return platformerrors.Wrap(err, "creating subscription")
	}

	if count == 0 {
		return s.refuseSubscriptionCreate(ctx, q, scope, created)
	}

	row, err := s.q.GetSubscriptionCreatedAt(ctx, q,
		billingdb.GetSubscriptionCreatedAtParams{ID: created.ID})
	if err != nil {
		return platformerrors.Wrap(err, "reading back the subscription's creation time")
	}

	created.CreatedAt = row.CreatedAt.UTC()

	return nil
}

// GetSubscription reads one of the scope's live subscriptions by id, on the
// caller's executor.
func (s *SQLStore) GetSubscription(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subscriptionID string,
) (*Subscription, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subscriptionKey, subscriptionID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading subscription %q", subscriptionID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading subscription %q", subscriptionID)
	}

	if err := requireID(subscriptionID); err != nil {
		return nil, op.Error(err, "reading subscription %q", subscriptionID)
	}

	subscription, err := s.readSubscription(ctx, q, scope, subscriptionID)
	if err != nil {
		return nil, op.Error(err, "reading subscription %q", subscriptionID)
	}

	return subscription, nil
}

// GetSubscriptionByExternalID reads one live subscription by the payment
// provider's identifier for it.
//
// The statement behind it sees archived rows, because it is also the collision
// check the writes run. This is the caller that decides an archived hit is not an
// answer.
func (s *SQLStore) GetSubscriptionByExternalID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	externalSubscriptionID string,
) (*Subscription, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading subscription by external id")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading subscription by external id")
	}

	subscription, err := s.readSubscriptionByExternalID(ctx, q, scope, externalSubscriptionID)
	if err != nil {
		return nil, op.Error(err, "reading subscription by external id")
	}

	if subscription.ArchivedAt != nil {
		return nil, op.Error(ErrSubscriptionNotFound, "reading subscription by external id")
	}

	op.Set(subscriptionKey, subscription.ID)

	return subscription, nil
}

// ListSubscriptions pages every subscription in the scope.
func (s *SQLStore) ListSubscriptions(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Subscription], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing subscriptions")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing subscriptions")
	}

	filter = pageFilter(filter)

	subscriptionRows, err := sortedRows(filter,
		func() ([]billingdb.ListSubscriptionsRow, error) {
			return s.q.ListSubscriptions(ctx, q, listSubscriptionsParams(scope, filter))
		},
		func() ([]billingdb.ListSubscriptionsDescendingRow, error) {
			return s.q.ListSubscriptionsDescending(ctx, q,
				billingdb.ListSubscriptionsDescendingParams(listSubscriptionsParams(scope, filter)))
		},
		func(r billingdb.ListSubscriptionsDescendingRow) billingdb.ListSubscriptionsRow {
			return billingdb.ListSubscriptionsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing subscriptions")
	}

	return s.drainSubscriptions(op, subscriptionRows, filter), nil
}

// ListSubscriptionsForAccount pages one account's subscriptions.
func (s *SQLStore) ListSubscriptionsForAccount(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	accountID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Subscription], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountKey, accountID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing subscriptions for account %q", accountID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing subscriptions for account %q", accountID)
	}

	if err := requireAccount(accountID); err != nil {
		return nil, op.Error(err, "listing subscriptions for account")
	}

	filter = pageFilter(filter)

	subscriptionRows, err := sortedRows(filter,
		func() ([]billingdb.ListSubscriptionsForAccountRow, error) {
			return s.q.ListSubscriptionsForAccount(ctx, q,
				listSubscriptionsForAccountParams(scope, accountID, filter))
		},
		func() ([]billingdb.ListSubscriptionsForAccountDescendingRow, error) {
			return s.q.ListSubscriptionsForAccountDescending(ctx, q,
				billingdb.ListSubscriptionsForAccountDescendingParams(
					listSubscriptionsForAccountParams(scope, accountID, filter)))
		},
		func(r billingdb.ListSubscriptionsForAccountDescendingRow) billingdb.ListSubscriptionsForAccountRow {
			return billingdb.ListSubscriptionsForAccountRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing subscriptions for account %q", accountID)
	}

	shaped := make([]billingdb.ListSubscriptionsRow, 0, len(subscriptionRows))
	for i := range subscriptionRows {
		shaped = append(shaped, billingdb.ListSubscriptionsRow(subscriptionRows[i]))
	}

	return s.drainSubscriptions(op, shaped, filter), nil
}

// ListCurrentSubscriptions pages the account's subscriptions whose paid period
// covers the store's clock.
//
// The horizon is bound rather than read off the server — see
// queries.CurrentAsOfArg. That is what puts this read and [Subscription.CurrentAt]
// on the same clock: a test that moves the store's clock past a period's end sees
// the subscription leave this page, which it would not if the statement read
// CURRENT_TIMESTAMP.
func (s *SQLStore) ListCurrentSubscriptions(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	accountID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Subscription], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountKey, accountID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing current subscriptions for account %q", accountID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing current subscriptions for account %q", accountID)
	}

	if err := requireAccount(accountID); err != nil {
		return nil, op.Error(err, "listing current subscriptions for account")
	}

	filter = pageFilter(filter)
	asOf := s.clock.Now().UTC()

	subscriptionRows, err := sortedRows(filter,
		func() ([]billingdb.ListCurrentSubscriptionsRow, error) {
			return s.q.ListCurrentSubscriptions(ctx, q,
				listCurrentSubscriptionsParams(scope, accountID, asOf, filter))
		},
		func() ([]billingdb.ListCurrentSubscriptionsDescendingRow, error) {
			return s.q.ListCurrentSubscriptionsDescending(ctx, q,
				billingdb.ListCurrentSubscriptionsDescendingParams(
					listCurrentSubscriptionsParams(scope, accountID, asOf, filter)))
		},
		func(r billingdb.ListCurrentSubscriptionsDescendingRow) billingdb.ListCurrentSubscriptionsRow {
			return billingdb.ListCurrentSubscriptionsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing current subscriptions for account %q", accountID)
	}

	shaped := make([]billingdb.ListSubscriptionsRow, 0, len(subscriptionRows))
	for i := range subscriptionRows {
		shaped = append(shaped, billingdb.ListSubscriptionsRow(subscriptionRows[i]))
	}

	return s.drainSubscriptions(op, shaped, filter), nil
}

// UpdateSubscription rewrites everything a provider's own subscription can move,
// through the caller's transaction, and answers with the row as the statement
// left it.
//
// The collision check against the provider-side id runs on tx, so a subscription
// written earlier in the same transaction is one this edit is checked against.
//
// The read-back is [SQLStore.readSubscription]'s statement rather than one of
// its own: a sync write does not take the row out of the table, so the ordinary
// keyed read reaches it on the transaction that wrote it. What it adds is the
// columns this write does not assign — the account the agreement belongs to,
// which is deliberately immutable, and the stamps the database owns. See
// [Store.UpdateSubscription].
func (s *SQLStore) UpdateSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subscription *Subscription,
) (*Subscription, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "updating subscription")
	}

	if subscription == nil {
		return nil, op.Error(ErrNilSubscription, "updating subscription")
	}

	op.Set(subscriptionKey, subscription.ID)

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "updating subscription %q", subscription.ID)
	}

	if err := requireID(subscription.ID); err != nil {
		return nil, op.Error(err, "updating subscription %q", subscription.ID)
	}

	updated := *subscription
	updated.CurrentPeriodStart = updated.CurrentPeriodStart.UTC()
	updated.CurrentPeriodEnd = updated.CurrentPeriodEnd.UTC()

	if err := updated.validate(); err != nil {
		return nil, op.Error(err, "updating subscription %q", subscription.ID)
	}

	if err := s.ensureSubscriptionExternalIDFree(
		ctx, tx, scope, updated.ExternalSubscriptionID, updated.ID,
	); err != nil {
		return nil, op.Error(err, "updating subscription %q", updated.ID)
	}

	count, err := s.q.UpdateSubscription(ctx, tx, updateSubscriptionParams(&updated, scope))
	if err = guardCount(count, err, ErrSubscriptionNotFound, "updating subscription"); err != nil {
		return nil, op.Error(err, "updating subscription %q", updated.ID)
	}

	stored, err := s.readSubscription(ctx, tx, scope, updated.ID)
	if err != nil {
		return nil, op.Error(err, "reading back the updated subscription %q", updated.ID)
	}

	return stored, nil
}

// SetSubscriptionStatus moves the standing and nothing else, through the
// caller's transaction.
//
// The guard is in the statement rather than in a read before it, so a
// redelivered event is answered by the affected-row count: the row already holds
// the status, nothing is written, and the caller is told ErrStatusUnchanged.
// Distinguishing that from a missing subscription takes one read, which is made
// only on the losing path and therefore never on the hot one — and on tx, which
// is what keeps a redelivery arriving in the same transaction as the row it
// addresses from being attributed against a snapshot that cannot see it. See
// [Store.SetSubscriptionStatus].
func (s *SQLStore) SetSubscriptionStatus(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subscriptionID string,
	status capitalism.SubscriptionStatus,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subscriptionKey, subscriptionID),
		observability.WithValue(statusKey, string(status)),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "setting subscription %q status", subscriptionID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "setting subscription %q status", subscriptionID)
	}

	if err := requireID(subscriptionID); err != nil {
		return op.Error(err, "setting subscription %q status", subscriptionID)
	}

	if !status.Known() {
		return op.Error(platformerrors.Wrapf(ErrInvalidStatus, "%q", status),
			"setting subscription %q status", subscriptionID)
	}

	count, err := s.q.SetSubscriptionStatus(ctx, tx, billingdb.SetSubscriptionStatusParams{
		Status: string(status),
		ID:     subscriptionID,
		Scope:  scope,
	})
	if err != nil {
		return op.Error(platformerrors.Wrap(err, "setting subscription status"),
			"setting subscription %q status", subscriptionID)
	}

	if count == 0 {
		return op.Error(s.refuseStatusWrite(ctx, tx, scope, subscriptionID),
			"setting subscription %q status", subscriptionID)
	}

	return nil
}

// ArchiveSubscription retires one of the scope's subscriptions administratively,
// through the caller's transaction, and answers with the row it retired.
//
// The read-back is GetArchivedSubscription rather than the keyed read, for the
// reason [SQLStore.ArchiveProduct] gives: a retired subscription is absent from
// every read by id this package has, and the ledger rows still pointing at it
// are what somebody reconciling them has to read it for.
func (s *SQLStore) ArchiveSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subscriptionID string,
) (*Subscription, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subscriptionKey, subscriptionID),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "archiving subscription %q", subscriptionID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "archiving subscription %q", subscriptionID)
	}

	count, err := s.q.ArchiveSubscription(ctx, tx,
		billingdb.ArchiveSubscriptionParams{ID: subscriptionID, Scope: scope})
	if err = guardCount(count, err, ErrSubscriptionNotFound, "archiving subscription"); err != nil {
		return nil, op.Error(err, "archiving subscription %q", subscriptionID)
	}

	row, err := s.q.GetArchivedSubscription(ctx, tx,
		billingdb.GetArchivedSubscriptionParams{ID: subscriptionID, Scope: scope})
	if err != nil {
		return nil, op.Error(platformerrors.Wrap(err, "reading back the archived subscription"),
			"archiving subscription %q", subscriptionID)
	}

	return subscriptionFromArchivedRow(&row), nil
}

// drainSubscriptions turns one list statement's rows into the paged result.
func (s *SQLStore) drainSubscriptions(
	op observability.Operation,
	rows []billingdb.ListSubscriptionsRow,
	filter *filtering.QueryFilter,
) *filtering.QueryFilteredResult[Subscription] {
	page := make([]pageRow[Subscription], 0, len(rows))
	for i := range rows {
		page = append(page, subscriptionPageRow(&rows[i]))
	}

	op.SpanOnly(countKey, len(page))

	return drainPage(page, func(sub *Subscription) string { return sub.ID }, filter)
}

// refuseStatusWrite reports why a status write touched nothing, having lost its
// guard.
//
// The guard cannot say which of the two happened — a row that is not there and a
// row already holding the status both report zero — and the difference matters:
// a redelivery is fine and a write against a subscription nobody has is not. So
// the read is made here, on the losing path only.
//
// It reads through the executor the write ran on, which is the caller's: a
// caller whose transaction wrote the subscription and then addressed it would
// otherwise be told no such subscription exists by a snapshot that cannot see
// the row.
func (s *SQLStore) refuseStatusWrite(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subscriptionID string,
) error {
	if _, err := s.readSubscription(ctx, q, scope, subscriptionID); err != nil {
		return err
	}

	return platformerrors.Wrapf(ErrStatusUnchanged, "subscription %q", subscriptionID)
}

// readSubscription is the read by id, through whatever executor the caller is
// holding.
func (s *SQLStore) readSubscription(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subscriptionID string,
) (*Subscription, error) {
	row, err := s.q.GetSubscription(ctx, q,
		billingdb.GetSubscriptionParams{ID: subscriptionID, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrSubscriptionNotFound)
	}

	return subscriptionFromRow(&row), nil
}

// readSubscriptionByExternalID is the read keyed on a provider's identifier. It
// sees archived rows; its callers decide what one means.
func (s *SQLStore) readSubscriptionByExternalID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	externalSubscriptionID string,
) (*Subscription, error) {
	if externalSubscriptionID == "" {
		return nil, ErrEmptyExternalID
	}

	row, err := s.q.GetSubscriptionByExternalID(ctx, q, billingdb.GetSubscriptionByExternalIDParams{
		Scope:                  scope,
		ExternalSubscriptionID: nullable(externalSubscriptionID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSubscriptionNotFound
		}

		return nil, platformerrors.Wrap(err, "reading subscription by external id")
	}

	return subscriptionFromRow((*billingdb.GetSubscriptionRow)(&row)), nil
}

// refuseSubscriptionCreate says which identifier the create lost to. See
// refuseCreate.
func (s *SQLStore) refuseSubscriptionCreate(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	created *Subscription,
) error {
	return refuseCreate(created.ExternalSubscriptionID, created.ID, func() error {
		_, err := s.readSubscriptionByExternalID(ctx, q, scope, created.ExternalSubscriptionID)

		return err
	}, ErrSubscriptionNotFound, ErrSubscriptionExists, nil)
}

// ensureSubscriptionExternalIDFree reports whether a provider-side subscription
// id is available to an update in this scope, excluding the row it belongs to
// already.
//
// An empty identifier is always free: it is stored as NULL, and NULL repeats —
// which is what makes a subscription granted by hand storable at all.
//
// Only the update asks. The create decides the same question inside its
// insert-ignore, where there is no window between deciding and writing — see
// ensureProductExternalIDFree for why the update neither has that spelling nor
// needs it.
func (s *SQLStore) ensureSubscriptionExternalIDFree(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	externalSubscriptionID, exceptID string,
) error {
	if externalSubscriptionID == "" {
		return nil
	}

	existing, err := s.readSubscriptionByExternalID(ctx, q, scope, externalSubscriptionID)

	switch {
	case errors.Is(err, ErrSubscriptionNotFound):
		return nil
	case err != nil:
		return err
	case existing.ID == exceptID:
		return nil
	default:
		return platformerrors.Wrapf(ErrSubscriptionExists, "external subscription id %q", externalSubscriptionID)
	}
}
