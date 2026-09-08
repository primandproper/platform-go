package billing

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/billing/internal/billingdb"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// The SQLStore's PurchaseStore: the one-time half.
var _ PurchaseStore = (*SQLStore)(nil)

// CreatePurchase records a sale in the scope, outstanding, through the caller's
// transaction.
//
// Every statement runs on tx — including the product check the write is gated
// on, so a product created through CreateProduct earlier in the same transaction
// is one a sale can be recorded against. The attribution read on the losing path
// runs there too; see [Store.CreatePurchase].
func (s *SQLStore) CreatePurchase(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	purchase *Purchase,
) (*Purchase, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "creating purchase")
	}

	if purchase == nil {
		return nil, op.Error(ErrNilPurchase, "creating purchase")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "creating purchase")
	}

	created := *purchase
	created.Scope = scope
	created.CompletedAt = utcPtr(created.CompletedAt)
	created.normalize()

	if err := created.validate(); err != nil {
		return nil, op.Error(err, "creating purchase")
	}

	if created.ID == "" {
		created.ID = identifiers.New()
	}

	op.Set(purchaseKey, created.ID)
	op.Set(accountKey, created.BelongsToAccount)

	if err := s.insertPurchase(ctx, tx, scope, &created); err != nil {
		return nil, op.Error(err, "creating purchase")
	}

	return &created, nil
}

// insertPurchase is the statements the create runs: the product check, the
// insert-ignore, the attribution of a loss, and the read-back of the creation
// time onto created.
func (s *SQLStore) insertPurchase(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	created *Purchase,
) error {
	if err := s.requireProduct(ctx, q, scope, created.ProductID); err != nil {
		return err
	}

	count, err := s.q.CreatePurchase(ctx, q, createPurchaseParams(created, scope))
	if err != nil {
		return platformerrors.Wrap(err, "creating purchase")
	}

	if count == 0 {
		return s.refusePurchaseCreate(ctx, q, scope, created)
	}

	row, err := s.q.GetPurchaseCreatedAt(ctx, q, billingdb.GetPurchaseCreatedAtParams{ID: created.ID})
	if err != nil {
		return platformerrors.Wrap(err, "reading back the purchase's creation time")
	}

	created.CreatedAt = row.CreatedAt.UTC()

	return nil
}

// GetPurchase reads one of the scope's live purchases by id, on the caller's
// executor.
func (s *SQLStore) GetPurchase(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	purchaseID string,
) (*Purchase, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(purchaseKey, purchaseID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading purchase %q", purchaseID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading purchase %q", purchaseID)
	}

	if err := requireID(purchaseID); err != nil {
		return nil, op.Error(err, "reading purchase %q", purchaseID)
	}

	row, err := s.q.GetPurchase(ctx, q,
		billingdb.GetPurchaseParams{ID: purchaseID, Scope: scope})
	if err != nil {
		return nil, op.Error(notFound(err, ErrPurchaseNotFound), "reading purchase %q", purchaseID)
	}

	return purchaseFromRow(&row), nil
}

// GetPurchaseByExternalID reads one live purchase by the payment provider's
// identifier for the payment. The statement behind it sees archived rows,
// because it is also the collision check the write runs.
func (s *SQLStore) GetPurchaseByExternalID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	externalTransactionID string,
) (*Purchase, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading purchase by external id")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading purchase by external id")
	}

	purchase, err := s.readPurchaseByExternalID(ctx, q, scope, externalTransactionID)
	if err != nil {
		return nil, op.Error(err, "reading purchase by external id")
	}

	if purchase.ArchivedAt != nil {
		return nil, op.Error(ErrPurchaseNotFound, "reading purchase by external id")
	}

	op.Set(purchaseKey, purchase.ID)

	return purchase, nil
}

// ListPurchases pages every purchase in the scope.
func (s *SQLStore) ListPurchases(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Purchase], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing purchases")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing purchases")
	}

	filter = pageFilter(filter)

	purchaseRows, err := sortedRows(filter,
		func() ([]billingdb.ListPurchasesRow, error) {
			return s.q.ListPurchases(ctx, q, listPurchasesParams(scope, filter))
		},
		func() ([]billingdb.ListPurchasesDescendingRow, error) {
			return s.q.ListPurchasesDescending(ctx, q,
				billingdb.ListPurchasesDescendingParams(listPurchasesParams(scope, filter)))
		},
		func(r billingdb.ListPurchasesDescendingRow) billingdb.ListPurchasesRow {
			return billingdb.ListPurchasesRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing purchases")
	}

	return s.drainPurchases(op, purchaseRows, filter), nil
}

// ListPurchasesForAccount pages one account's purchases.
func (s *SQLStore) ListPurchasesForAccount(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	accountID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Purchase], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountKey, accountID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing purchases for account %q", accountID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing purchases for account %q", accountID)
	}

	if err := requireAccount(accountID); err != nil {
		return nil, op.Error(err, "listing purchases for account")
	}

	filter = pageFilter(filter)

	purchaseRows, err := sortedRows(filter,
		func() ([]billingdb.ListPurchasesForAccountRow, error) {
			return s.q.ListPurchasesForAccount(ctx, q,
				listPurchasesForAccountParams(scope, accountID, filter))
		},
		func() ([]billingdb.ListPurchasesForAccountDescendingRow, error) {
			return s.q.ListPurchasesForAccountDescending(ctx, q,
				billingdb.ListPurchasesForAccountDescendingParams(
					listPurchasesForAccountParams(scope, accountID, filter)))
		},
		func(r billingdb.ListPurchasesForAccountDescendingRow) billingdb.ListPurchasesForAccountRow {
			return billingdb.ListPurchasesForAccountRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing purchases for account %q", accountID)
	}

	shaped := make([]billingdb.ListPurchasesRow, 0, len(purchaseRows))
	for i := range purchaseRows {
		shaped = append(shaped, billingdb.ListPurchasesRow(purchaseRows[i]))
	}

	return s.drainPurchases(op, shaped, filter), nil
}

// CompletePurchase stamps the moment the money arrived, through the caller's
// transaction.
//
// The guard is completed_at IS NULL, in the statement, so a purchase completes
// exactly once however many times the provider delivers the event. Telling a
// replay apart from a missing purchase takes one read, made only on the losing
// path — and on tx, so a sale created and settled in one transaction is answered
// by the row that transaction wrote. See [Store.CompletePurchase].
func (s *SQLStore) CompletePurchase(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	purchaseID string,
	at time.Time,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(purchaseKey, purchaseID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "completing purchase %q", purchaseID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "completing purchase %q", purchaseID)
	}

	if err := requireID(purchaseID); err != nil {
		return op.Error(err, "completing purchase %q", purchaseID)
	}

	// The zero time is the completion with no provider behind it — a comped
	// order, a migration — and falls back to the store's clock. Anything else is
	// the provider's own settlement time, which is what revenue is recognized
	// against and is frequently older than this process's idea of now.
	stamped := at.UTC()
	if at.IsZero() {
		stamped = s.clock.Now().UTC()
	}

	count, err := s.q.CompletePurchase(ctx, tx, billingdb.CompletePurchaseParams{
		CompletedAt: &stamped,
		ID:          purchaseID,
		Scope:       scope,
	})
	if err != nil {
		return op.Error(platformerrors.Wrap(err, "completing purchase"),
			"completing purchase %q", purchaseID)
	}

	if count == 0 {
		return op.Error(s.refuseCompletion(ctx, tx, scope, purchaseID), "completing purchase %q", purchaseID)
	}

	return nil
}

// ArchivePurchase retires one of the scope's purchases administratively, through
// the caller's transaction. See [Store.ArchivePurchase].
func (s *SQLStore) ArchivePurchase(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	purchaseID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(purchaseKey, purchaseID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "archiving purchase %q", purchaseID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving purchase %q", purchaseID)
	}

	count, err := s.q.ArchivePurchase(ctx, tx,
		billingdb.ArchivePurchaseParams{ID: purchaseID, Scope: scope})
	if err = guardCount(count, err, ErrPurchaseNotFound, "archiving purchase"); err != nil {
		return op.Error(err, "archiving purchase %q", purchaseID)
	}

	return nil
}

// drainPurchases turns one list statement's rows into the paged result.
func (s *SQLStore) drainPurchases(
	op observability.Operation,
	rows []billingdb.ListPurchasesRow,
	filter *filtering.QueryFilter,
) *filtering.QueryFilteredResult[Purchase] {
	page := make([]pageRow[Purchase], 0, len(rows))
	for i := range rows {
		page = append(page, purchasePageRow(&rows[i]))
	}

	op.SpanOnly(countKey, len(page))

	return drainPage(page, func(p *Purchase) string { return p.ID }, filter)
}

// refuseCompletion reports why a completion touched nothing, having lost its
// guard: a purchase that is not there, or one whose money already arrived.
//
// It reads through the executor the write ran on, for the reason
// refuseStatusWrite does.
func (s *SQLStore) refuseCompletion(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	purchaseID string,
) error {
	if _, err := s.q.GetPurchase(ctx, q,
		billingdb.GetPurchaseParams{ID: purchaseID, Scope: scope}); err != nil {
		return notFound(err, ErrPurchaseNotFound)
	}

	// The row is there and live, so the guard is what lost — which for this
	// statement means the money had already arrived. The read exists to tell that
	// apart from a purchase nobody has; it is not a second guard, and it does not
	// re-examine completed_at.
	return platformerrors.Wrapf(ErrAlreadyCompleted, "purchase %q", purchaseID)
}

// readPurchaseByExternalID is the read keyed on a provider's identifier. It sees
// archived rows; its callers decide what one means.
func (s *SQLStore) readPurchaseByExternalID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	externalTransactionID string,
) (*Purchase, error) {
	if externalTransactionID == "" {
		return nil, ErrEmptyExternalID
	}

	row, err := s.q.GetPurchaseByExternalID(ctx, q, billingdb.GetPurchaseByExternalIDParams{
		Scope:                 scope,
		ExternalTransactionID: nullable(externalTransactionID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPurchaseNotFound
		}

		return nil, platformerrors.Wrap(err, "reading purchase by external id")
	}

	return purchaseFromRow((*billingdb.GetPurchaseRow)(&row)), nil
}

// refusePurchaseCreate says which identifier the create lost to.
//
// There is no update counterpart, as there is for products and subscriptions: a
// purchase's provider id is written with the row and there is no statement able
// to change it, so the insert-ignore is the only place the question is ever
// asked. See refuseCreate.
func (s *SQLStore) refusePurchaseCreate(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	created *Purchase,
) error {
	return refuseCreate(created.ExternalTransactionID, created.ID, func() error {
		_, err := s.readPurchaseByExternalID(ctx, q, scope, created.ExternalTransactionID)

		return err
	}, ErrPurchaseNotFound, ErrPurchaseExists, nil)
}
