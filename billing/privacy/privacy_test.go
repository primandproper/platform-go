package privacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v14/billing"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var testScope = tenancy.Of("acme")

const testAccount = "account-1"

var testSubject = dataprivacy.Subject{ID: testAccount}

// testReader is the executor a collector is built over.
//
// Nothing executes through it: the store beneath the collector is a mock, and
// what these tests assert is that the executor the collector was built with is
// the one it passes down. database.SQLQueryExecutor is an interface with no
// unexported methods, so unlike database.Tx a test can stand in for it.
type testReader struct{}

var _ database.SQLQueryExecutor = (*testReader)(nil)

func (*testReader) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("the collector's store is a mock; nothing runs on this")
}

func (*testReader) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("the collector's store is a mock; nothing runs on this")
}

func (*testReader) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("the collector's store is a mock; nothing runs on this")
}

func (*testReader) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("the collector's store is a mock; nothing runs on this")
}

// pageOf answers one read with these rows and then with nothing, which is what
// makes the collector's cursor walk terminate.
func pageOf[T any](rows []*T, id func(*T) string) func(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[T], error) {
	served := false

	return func(
		_ context.Context,
		_ database.SQLQueryExecutor,
		_ tenancy.Scope,
		_ string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[T], error) {
		if served {
			return filtering.NewQueryFilteredResultWithoutCounts[T](nil, id, filter), nil
		}

		served = true

		return filtering.NewQueryFilteredResultWithoutCounts(rows, id, filter), nil
	}
}

// fullStore answers all three account-keyed reads with one row each.
func fullStore() *billingmock.StoreMock {
	return &billingmock.StoreMock{
		ListSubscriptionsForAccountFunc: pageOf(
			[]*billing.Subscription{{ID: "sub-1", BelongsToAccount: testAccount, ProductID: "pro"}},
			func(s *billing.Subscription) string { return s.ID }),
		ListPurchasesForAccountFunc: pageOf(
			[]*billing.Purchase{{ID: "pur-1", BelongsToAccount: testAccount, AmountCents: 999}},
			func(p *billing.Purchase) string { return p.ID }),
		ListTransactionsForAccountFunc: pageOf(
			[]*billing.Transaction{{ID: "txn-1", BelongsToAccount: testAccount, AmountCents: 999}},
			func(t *billing.Transaction) string { return t.ID }),
	}
}

func TestNewCollector_Refusals(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		_, err := NewCollector(nil, &testReader{}, FixedAccounts(testScope))
		test.ErrorIs(t, err, ErrNilStore)
	})

	T.Run("refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		// It is a constructor argument because Collect has nowhere to put one,
		// so an absent executor has to be refused here or every collection fails
		// at the store instead.
		_, err := NewCollector(fullStore(), nil, FixedAccounts(testScope))
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("refuses a nil resolver", func(t *testing.T) {
		t.Parallel()

		// There is no default: a resolver that answered "the account whose id
		// equals the subject id" would export nothing for most deployments and
		// report success doing it.
		_, err := NewCollector(fullStore(), &testReader{}, nil)
		test.ErrorIs(t, err, ErrNilAccountResolver)
	})
}

func TestCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("exports all three tables for the resolved account", func(t *testing.T) {
		t.Parallel()

		collector, err := NewCollector(fullStore(), &testReader{}, FixedAccounts(testScope))
		must.NoError(t, err)

		body, err := collector.Collect(t.Context(), testSubject)
		must.NoError(t, err)

		var exports []Export
		must.NoError(t, json.Unmarshal(body, &exports))
		must.SliceLen(t, 1, exports)

		test.EqOp(t, testAccount, exports[0].Account)
		test.EqOp(t, testScope, exports[0].Scope)
		test.SliceLen(t, 1, exports[0].Subscriptions)
		test.SliceLen(t, 1, exports[0].Purchases)
		test.SliceLen(t, 1, exports[0].Transactions)
	})

	T.Run("renders the global scope as the identifier it is stored as", func(t *testing.T) {
		t.Parallel()

		collector, err := NewCollector(fullStore(), &testReader{}, FixedAccounts(tenancy.Global()))
		must.NoError(t, err)

		body, err := collector.Collect(t.Context(), testSubject)
		must.NoError(t, err)

		// The artifact goes to the subject and to whoever asks them for it, so
		// what it says a row's scope is has to be what the column says. The
		// global scope's identifier is the empty string; "<global>" is how a
		// Scope reads in a log and is not a value anything is stored under.
		test.StrContains(t, string(body), `"scope":""`)

		var exports []Export
		must.NoError(t, json.Unmarshal(body, &exports))
		must.SliceLen(t, 1, exports)
		test.True(t, exports[0].Scope.IsGlobal())
	})

	T.Run("reads through the executor it was built with", func(t *testing.T) {
		t.Parallel()

		var (
			reader database.SQLQueryExecutor = &testReader{}
			seen   database.SQLQueryExecutor
		)

		store := fullStore()
		store.ListTransactionsForAccountFunc = func(
			_ context.Context,
			q database.SQLQueryExecutor,
			_ tenancy.Scope,
			_ string,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Transaction], error) {
			seen = q

			return filtering.NewQueryFilteredResultWithoutCounts[billing.Transaction](nil,
				func(tr *billing.Transaction) string { return tr.ID }, filter), nil
		}

		collector, err := NewCollector(store, reader, FixedAccounts(testScope))
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testSubject)
		must.NoError(t, err)

		// The executor the collector was built with is the one that reaches the
		// store, which is the whole of what the constructor argument buys.
		test.EqOp(t, reader, seen)
	})

	T.Run("asks for archived rows", func(t *testing.T) {
		t.Parallel()

		var sawArchived bool

		store := fullStore()
		store.ListSubscriptionsForAccountFunc = func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			_ tenancy.Scope,
			_ string,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Subscription], error) {
			sawArchived = filter.IncludeArchived != nil && *filter.IncludeArchived

			return filtering.NewQueryFilteredResultWithoutCounts[billing.Subscription](nil,
				func(s *billing.Subscription) string { return s.ID }, filter), nil
		}

		collector, err := NewCollector(store, &testReader{}, FixedAccounts(testScope))
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testSubject)
		must.NoError(t, err)

		// An archived subscription is still something the subject was sold, and
		// an export showing only what an operator had not yet tidied away would
		// answer a different question than the right of access asks.
		test.True(t, sawArchived)
	})

	T.Run("exports every account the resolver names", func(t *testing.T) {
		t.Parallel()

		resolve := func(context.Context, dataprivacy.Subject) ([]Account, error) {
			return []Account{
				{Scope: testScope, ID: "account-1"},
				{Scope: tenancy.Of("other"), ID: "account-2"},
			}, nil
		}

		collector, err := NewCollector(fullStore(), &testReader{}, resolve)
		must.NoError(t, err)

		body, err := collector.Collect(t.Context(), testSubject)
		must.NoError(t, err)

		var exports []Export
		must.NoError(t, json.Unmarshal(body, &exports))
		test.SliceLen(t, 2, exports)
	})

	T.Run("exports nothing for a subject who has bought nothing", func(t *testing.T) {
		t.Parallel()

		resolve := func(context.Context, dataprivacy.Subject) ([]Account, error) {
			return nil, nil
		}

		collector, err := NewCollector(fullStore(), &testReader{}, resolve)
		must.NoError(t, err)

		body, err := collector.Collect(t.Context(), testSubject)
		must.NoError(t, err)

		var exports []Export
		must.NoError(t, json.Unmarshal(body, &exports))
		test.SliceEmpty(t, exports)
	})

	T.Run("passes a resolver failure through", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("no directory")

		collector, err := NewCollector(fullStore(), &testReader{},
			func(context.Context, dataprivacy.Subject) ([]Account, error) { return nil, boom })
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testSubject)
		test.ErrorIs(t, err, boom)
	})

	T.Run("passes a read failure through", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the database is unwell")

		store := fullStore()
		store.ListPurchasesForAccountFunc = func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Purchase], error) {
			return nil, boom
		}

		collector, err := NewCollector(store, &testReader{}, FixedAccounts(testScope))
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testSubject)
		test.ErrorIs(t, err, boom)
	})
}

// TestPackage_ShipsNoEraser is the ruling, checked rather than only written down.
//
// Financial records carry a statutory retention that outranks a right to
// erasure, so the only correct Eraser here is one that erases nothing — and
// shipping it would invite a deployment to register it and believe its billing
// history had been deleted. If somebody adds one, this stops compiling and they
// have to read the package documentation first.
func TestPackage_ShipsNoEraser(t *testing.T) {
	t.Parallel()

	var collector any = &Collector{}

	_, isEraser := collector.(dataprivacy.Eraser)
	test.False(t, isEraser)

	_, isCollector := collector.(dataprivacy.Collector)
	test.True(t, isCollector)
}
