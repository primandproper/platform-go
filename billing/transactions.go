package billing

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/billing/internal/billingdb"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// The SQLStore's TransactionStore: the ledger.
var _ TransactionStore = (*SQLStore)(nil)

// RecordTransaction writes one payment attempt to the scope's ledger, through
// the caller's transaction, so the row lands with the audit entry and the outbox
// event that describe it rather than ahead of them.
//
// The collision check is the point of this method. Payment providers redeliver;
// without it the second delivery would either insert a second row — a number
// somebody reconciles by hand — or fail against the index with an error the
// caller cannot tell from an outage, and would therefore retry forever.
//
// Every statement runs on tx: the insert-ignore, the read-back, and the
// attribution the insert makes when it loses, which asks about the subscription
// or purchase the row names. A ledger row written against a purchase created
// earlier in the same transaction is therefore attributed correctly rather than
// refused for naming a row nobody has.
//
// The instrument is incremented when the statement writes the row rather than
// when the caller commits: nothing here can observe somebody else's commit, and
// the alternative — leaving this path uncounted — would quietly remove the one
// instrument a payment integration's health is read from. A rolled back
// transaction therefore leaves a count with no row behind it. See
// [Store.RecordTransaction].
func (s *SQLStore) RecordTransaction(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	transaction *Transaction,
) (*Transaction, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "recording transaction")
	}

	if transaction == nil {
		return nil, op.Error(ErrNilTransaction, "recording transaction")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "recording transaction")
	}

	recorded := *transaction
	recorded.Scope = scope
	recorded.normalize()

	if err := recorded.validate(); err != nil {
		return nil, op.Error(err, "recording transaction")
	}

	if recorded.ID == "" {
		recorded.ID = identifiers.New()
	}

	op.Set(transactionKey, recorded.ID)
	op.Set(accountKey, recorded.BelongsToAccount)
	op.Set(statusKey, string(recorded.Status))

	if err := s.insertTransaction(ctx, tx, scope, &recorded); err != nil {
		return nil, op.Error(err, "recording transaction")
	}

	s.countTransaction(ctx, recorded.Status)

	return &recorded, nil
}

// insertTransaction is the statements the ledger write runs: the insert-ignore,
// the attribution of a loss, and the read-back of the creation time onto
// recorded.
func (s *SQLStore) insertTransaction(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	recorded *Transaction,
) error {
	count, err := s.q.CreateTransaction(ctx, q, createTransactionParams(recorded, scope))
	if err != nil {
		return platformerrors.Wrap(err, "recording transaction")
	}

	if count == 0 {
		return s.refuseTransactionCreate(ctx, q, scope, recorded)
	}

	row, err := s.q.GetTransactionCreatedAt(ctx, q,
		billingdb.GetTransactionCreatedAtParams{ID: recorded.ID})
	if err != nil {
		return platformerrors.Wrap(err, "reading back the transaction's creation time")
	}

	recorded.CreatedAt = row.CreatedAt.UTC()

	return nil
}

// GetTransaction reads one of the scope's live ledger rows by id, on the
// caller's executor.
func (s *SQLStore) GetTransaction(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	transactionID string,
) (*Transaction, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(transactionKey, transactionID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading transaction %q", transactionID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading transaction %q", transactionID)
	}

	if err := requireID(transactionID); err != nil {
		return nil, op.Error(err, "reading transaction %q", transactionID)
	}

	row, err := s.q.GetTransaction(ctx, q,
		billingdb.GetTransactionParams{ID: transactionID, Scope: scope})
	if err != nil {
		return nil, op.Error(notFound(err, ErrTransactionNotFound), "reading transaction %q", transactionID)
	}

	return transactionFromRow(&row), nil
}

// GetTransactionByExternalID reads one live ledger row by the payment provider's
// identifier for the attempt.
//
// It is how a handler tells a redelivery from a new charge before it writes, and
// the statement behind it sees archived rows because it is also the collision
// check RecordTransaction runs. This is the caller that decides an archived hit
// is not an answer.
func (s *SQLStore) GetTransactionByExternalID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	externalTransactionID string,
) (*Transaction, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading transaction by external id")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading transaction by external id")
	}

	transaction, err := s.readTransactionByExternalID(ctx, q, scope, externalTransactionID)
	if err != nil {
		return nil, op.Error(err, "reading transaction by external id")
	}

	if transaction.ArchivedAt != nil {
		return nil, op.Error(ErrTransactionNotFound, "reading transaction by external id")
	}

	op.Set(transactionKey, transaction.ID)

	return transaction, nil
}

// ListTransactions pages every ledger row in the scope.
func (s *SQLStore) ListTransactions(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Transaction], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing transactions")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing transactions")
	}

	filter = pageFilter(filter)

	transactionRows, err := sortedRows(filter,
		func() ([]billingdb.ListTransactionsRow, error) {
			return s.q.ListTransactions(ctx, q, listTransactionsParams(scope, filter))
		},
		func() ([]billingdb.ListTransactionsDescendingRow, error) {
			return s.q.ListTransactionsDescending(ctx, q,
				billingdb.ListTransactionsDescendingParams(listTransactionsParams(scope, filter)))
		},
		func(r billingdb.ListTransactionsDescendingRow) billingdb.ListTransactionsRow {
			return billingdb.ListTransactionsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing transactions")
	}

	return s.drainTransactions(op, transactionRows, filter), nil
}

// ListTransactionsForAccount pages one account's ledger.
func (s *SQLStore) ListTransactionsForAccount(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	accountID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Transaction], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountKey, accountID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing transactions for account %q", accountID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing transactions for account %q", accountID)
	}

	if err := requireAccount(accountID); err != nil {
		return nil, op.Error(err, "listing transactions for account")
	}

	filter = pageFilter(filter)

	transactionRows, err := sortedRows(filter,
		func() ([]billingdb.ListTransactionsForAccountRow, error) {
			return s.q.ListTransactionsForAccount(ctx, q,
				listTransactionsForAccountParams(scope, accountID, filter))
		},
		func() ([]billingdb.ListTransactionsForAccountDescendingRow, error) {
			return s.q.ListTransactionsForAccountDescending(ctx, q,
				billingdb.ListTransactionsForAccountDescendingParams(
					listTransactionsForAccountParams(scope, accountID, filter)))
		},
		func(r billingdb.ListTransactionsForAccountDescendingRow) billingdb.ListTransactionsForAccountRow {
			return billingdb.ListTransactionsForAccountRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing transactions for account %q", accountID)
	}

	shaped := make([]billingdb.ListTransactionsRow, 0, len(transactionRows))
	for i := range transactionRows {
		shaped = append(shaped, billingdb.ListTransactionsRow(transactionRows[i]))
	}

	return s.drainTransactions(op, shaped, filter), nil
}

// SetTransactionStatus moves an attempt's outcome, through the caller's
// transaction.
//
// It is guarded exactly as SetSubscriptionStatus is, with the guard and the
// attribution read behind it both running on tx. The counter is incremented only
// when this call is the one that moved the row — so the proportions the
// instrument reports are outcomes rather than deliveries — and on the write
// rather than on the caller's commit, for the reason
// [SQLStore.RecordTransaction] gives. See [Store.SetTransactionStatus].
func (s *SQLStore) SetTransactionStatus(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	transactionID string,
	status TransactionStatus,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(transactionKey, transactionID),
		observability.WithValue(statusKey, string(status)),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "setting transaction %q status", transactionID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "setting transaction %q status", transactionID)
	}

	if err := requireID(transactionID); err != nil {
		return op.Error(err, "setting transaction %q status", transactionID)
	}

	if !status.Valid() {
		return op.Error(platformerrors.Wrapf(ErrInvalidStatus, "%q", status),
			"setting transaction %q status", transactionID)
	}

	count, err := s.q.SetTransactionStatus(ctx, tx, billingdb.SetTransactionStatusParams{
		Status: string(status),
		ID:     transactionID,
		Scope:  scope,
	})
	if err != nil {
		return op.Error(platformerrors.Wrap(err, "setting transaction status"),
			"setting transaction %q status", transactionID)
	}

	if count == 0 {
		return op.Error(s.refuseTransactionStatusWrite(ctx, tx, scope, transactionID),
			"setting transaction %q status", transactionID)
	}

	s.countTransaction(ctx, status)

	return nil
}

// ArchiveTransaction retires one of the scope's ledger rows administratively,
// through the caller's transaction. See [Store.ArchiveTransaction].
func (s *SQLStore) ArchiveTransaction(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	transactionID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(transactionKey, transactionID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "archiving transaction %q", transactionID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving transaction %q", transactionID)
	}

	count, err := s.q.ArchiveTransaction(ctx, tx,
		billingdb.ArchiveTransactionParams{ID: transactionID, Scope: scope})
	if err = guardCount(count, err, ErrTransactionNotFound, "archiving transaction"); err != nil {
		return op.Error(err, "archiving transaction %q", transactionID)
	}

	return nil
}

// drainTransactions turns one list statement's rows into the paged result.
func (s *SQLStore) drainTransactions(
	op observability.Operation,
	rows []billingdb.ListTransactionsRow,
	filter *filtering.QueryFilter,
) *filtering.QueryFilteredResult[Transaction] {
	page := make([]pageRow[Transaction], 0, len(rows))
	for i := range rows {
		page = append(page, transactionPageRow(&rows[i]))
	}

	op.SpanOnly(countKey, len(page))

	return drainPage(page, func(t *Transaction) string { return t.ID }, filter)
}

// refuseTransactionStatusWrite reports why a status write touched nothing: a row
// that is not there, or one already holding the status.
//
// It reads through the executor the write ran on, for the reason
// refuseStatusWrite does.
func (s *SQLStore) refuseTransactionStatusWrite(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	transactionID string,
) error {
	if _, err := s.q.GetTransaction(ctx, q,
		billingdb.GetTransactionParams{ID: transactionID, Scope: scope}); err != nil {
		return notFound(err, ErrTransactionNotFound)
	}

	return platformerrors.Wrapf(ErrStatusUnchanged, "transaction %q", transactionID)
}

// readTransactionByExternalID is the read keyed on a provider's identifier. It
// sees archived rows; its callers decide what one means.
func (s *SQLStore) readTransactionByExternalID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	externalTransactionID string,
) (*Transaction, error) {
	if externalTransactionID == "" {
		return nil, ErrEmptyExternalID
	}

	row, err := s.q.GetTransactionByExternalID(ctx, q, billingdb.GetTransactionByExternalIDParams{
		Scope:                 scope,
		ExternalTransactionID: nullable(externalTransactionID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}

		return nil, platformerrors.Wrap(err, "reading transaction by external id")
	}

	return transactionFromRow((*billingdb.GetTransactionRow)(&row)), nil
}

// refuseTransactionCreate says what the ledger insert lost to, having written
// nothing.
//
// The provider's identifier is asked about first, because a redelivered charge is
// what this count almost always means and ErrTransactionExists is the whole answer
// to it. Then the two rows this one points at, which is the residual only this
// table has: MySQL's IGNORE downgrades a foreign key it could not satisfy to the
// same zero count as a collision, so without asking, a ledger row naming a
// subscription nobody has would be reported as an id somebody else holds.
// Postgres and SQLite raise that case at the insert and never arrive here, so the
// two engines differ in which error names a caller's bad reference and agree in
// refusing to store it.
//
// There is no update counterpart. A ledger row's provider id is written once and
// no statement can change it, so the insert-ignore is the only place any of this
// is asked.
func (s *SQLStore) refuseTransactionCreate(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	recorded *Transaction,
) error {
	return refuseCreate(recorded.ExternalTransactionID, recorded.ID, func() error {
		_, err := s.readTransactionByExternalID(ctx, q, scope, recorded.ExternalTransactionID)

		return err
	}, ErrTransactionNotFound, ErrTransactionExists, func() error {
		if recorded.SubscriptionID != "" {
			_, err := s.q.CheckSubscriptionPresence(ctx, q, billingdb.CheckSubscriptionPresenceParams{
				ID: recorded.SubscriptionID, Scope: scope,
			})
			if err = requirePresence(err, ErrSubscriptionNotFound, recorded.SubscriptionID); err != nil {
				return err
			}
		}

		if recorded.PurchaseID != "" {
			_, err := s.q.CheckPurchasePresence(ctx, q, billingdb.CheckPurchasePresenceParams{
				ID: recorded.PurchaseID, Scope: scope,
			})
			if err = requirePresence(err, ErrPurchaseNotFound, recorded.PurchaseID); err != nil {
				return err
			}
		}

		return nil
	})
}
