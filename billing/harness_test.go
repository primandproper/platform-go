package billing

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/billing/migrations"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/clock"
	clockmock "github.com/primandproper/primitives-go/v2/clock/mock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a client needs.
//
// maxOpenConns is a field rather than the constant it used to be because one of
// these tests is about concurrency, and a suite whose pool opens a single
// connection cannot have any: database/sql queues the callers, every statement
// runs alone, and an assertion about two writes crossing passes without either
// of them crossing anything. Zero means one, which is what every test but that
// one wants — SQLite serializes its writers regardless, and a shared file
// database with several of them is SQLITE_BUSY rather than a finding.
type testClientConfig struct {
	connectionString string
	maxOpenConns     int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64       { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int             { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int {
	if c.maxOpenConns > 0 {
		return c.maxOpenConns
	}

	return 1
}

func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// prefixCounter names a fresh table set per subtest. Subtests share one database
// and must not share tables: a provider identifier is unique per scope across
// live and archived rows alike, and a suite that reused one table set would have
// subtests colliding on identifiers they each chose freely.
var prefixCounter atomic.Uint64

// The scope most of this suite works in, and a second one every isolation
// assertion reads through. Neither is global, because tenancy.Global() is the
// scope a bug defaults to — a predicate that lost its binding matches it — so a
// suite that worked entirely in it would pass with the scope dropped from every
// statement.
var (
	testScope  = tenancy.Of("acme")
	otherScope = tenancy.Of("other")
)

// The accounts this suite bills. Two, so that every account-keyed read has
// something it must not return.
const (
	testAccount  = "account-1"
	otherAccount = "account-2"
)

// testNow is the instant the suite's clock starts at. Every subscription period
// below is expressed relative to it, so an agreement is current or lapsed because
// the test said so rather than because the wall clock happened to agree.
//
// It is well clear of both horizons the schema has to survive: the SQLite
// lexicographic comparison over a text column wants a four-digit year, and the
// MySQL DATETIME range starts in 1000.
var testNow = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// errCompanionWrite stands in for the write a consumer makes beside a billing
// row — the audit entry, the outbox event a webhook dispatcher fans out —
// failing after the row itself is in the transaction.
var errCompanionWrite = platformerrors.New("the companion write failed")

// storeEnv is one live database plus the dialect it speaks.
//
// connectionString is kept so that a test needing its own pool can open one onto
// the same database. Only the concurrency test does, and only on the two engines
// where concurrent writers are a real thing.
type storeEnv struct {
	client           database.Client
	dialect          dialect.Dialect
	connectionString string
}

// concurrentEnv returns an environment onto the same database whose pool is wide
// enough for conns writers at once, and whether there is one to be had.
//
// It is false for SQLite, whose writer is single however wide the pool is, so a
// test asking for this is asking for something that engine does not have.
func (e *storeEnv) concurrentEnv(tb testing.TB, conns int) (*storeEnv, bool) {
	tb.Helper()

	cfg := &testClientConfig{connectionString: e.connectionString, maxOpenConns: conns}

	var (
		client database.Client
		err    error
	)

	switch e.dialect {
	case dialect.Postgres:
		client, err = postgres.NewDatabaseClient(tb.Context(), cfg)
	case dialect.MySQL:
		client, err = mysql.NewDatabaseClient(tb.Context(), cfg)
	default:
		return nil, false
	}

	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: e.dialect, connectionString: e.connectionString}, true
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real SQL
// — placeholder rendering, the guarded updates' predicates, the partial indexes
// — without a container.
func newSQLiteEnv(tb testing.TB) *storeEnv {
	tb.Helper()

	connectionString := filepath.Join(tb.TempDir(), "billing.db")

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: connectionString})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite, connectionString: connectionString}
}

// newStore migrates a uniquely prefixed table set and returns a store over it, on
// a clock parked at testNow.
func (e *storeEnv) newStore(tb testing.TB, opts ...SQLStoreOption) *SQLStore {
	tb.Helper()

	store, _ := e.newStoreWithClock(tb, opts...)

	return store
}

// newStoreWithClock is newStore, also handing back the clock so a test can move
// time past a subscription's period end.
func (e *storeEnv) newStoreWithClock(tb testing.TB, opts ...SQLStoreOption) (*SQLStore, *stubClock) {
	tb.Helper()

	prefix := fmt.Sprintf("bl_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	stub := newStubClock()

	base := []SQLStoreOption{WithTablePrefix(prefix), WithClock(stub)}

	store, err := NewSQLStore(e.client, append(base, opts...)...)
	must.NoError(tb, err)

	return store, stub
}

// inTx runs fn inside a transaction on the environment's database and reports
// what fn returned.
//
// Every write in this store takes the caller's transaction, so a test that wants
// a row written opens one — which is what a consumer does. It hands back fn's
// error rather than asserting on it, because a refused write is what half of
// these cases are about and RunInTransaction returns the callback's error
// unwrapped.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on: the client's, outside any
// transaction. The cases about a read that joins a transaction pass the Tx
// instead, and they are in the caller-transaction suite.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// The thirteen writes, each in a transaction of its own, reporting what the
// write returned.
//
// The transaction is a detail in most of these cases rather than the subject: a
// consumer with nothing to commit alongside opens exactly this. What a billing
// row commits *with* is the caller-transaction suite.

func (e *storeEnv) createProduct(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	product *Product,
) (*Product, error) {
	tb.Helper()

	var created *Product

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		created, txErr = store.CreateProduct(tb.Context(), tx, scope, product)

		return txErr
	})

	return created, err
}

func (e *storeEnv) updateProduct(tb testing.TB, store *SQLStore, scope tenancy.Scope, product *Product) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.UpdateProduct(tb.Context(), tx, scope, product)
	})
}

func (e *storeEnv) archiveProduct(tb testing.TB, store *SQLStore, scope tenancy.Scope, productID string) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.ArchiveProduct(tb.Context(), tx, scope, productID)
	})
}

func (e *storeEnv) createSubscription(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	subscription *Subscription,
) (*Subscription, error) {
	tb.Helper()

	var created *Subscription

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		created, txErr = store.CreateSubscription(tb.Context(), tx, scope, subscription)

		return txErr
	})

	return created, err
}

func (e *storeEnv) updateSubscription(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	subscription *Subscription,
) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.UpdateSubscription(tb.Context(), tx, scope, subscription)
	})
}

func (e *storeEnv) setSubscriptionStatus(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	subscriptionID string,
	status capitalism.SubscriptionStatus,
) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.SetSubscriptionStatus(tb.Context(), tx, scope, subscriptionID, status)
	})
}

func (e *storeEnv) archiveSubscription(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	subscriptionID string,
) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.ArchiveSubscription(tb.Context(), tx, scope, subscriptionID)
	})
}

func (e *storeEnv) createPurchase(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	purchase *Purchase,
) (*Purchase, error) {
	tb.Helper()

	var created *Purchase

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		created, txErr = store.CreatePurchase(tb.Context(), tx, scope, purchase)

		return txErr
	})

	return created, err
}

func (e *storeEnv) completePurchase(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	purchaseID string,
	at time.Time,
) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.CompletePurchase(tb.Context(), tx, scope, purchaseID, at)
	})
}

func (e *storeEnv) archivePurchase(tb testing.TB, store *SQLStore, scope tenancy.Scope, purchaseID string) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.ArchivePurchase(tb.Context(), tx, scope, purchaseID)
	})
}

func (e *storeEnv) recordTransaction(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	transaction *Transaction,
) (*Transaction, error) {
	tb.Helper()

	var recorded *Transaction

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		recorded, txErr = store.RecordTransaction(tb.Context(), tx, scope, transaction)

		return txErr
	})

	return recorded, err
}

func (e *storeEnv) setTransactionStatus(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	transactionID string,
	status TransactionStatus,
) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.SetTransactionStatus(tb.Context(), tx, scope, transactionID, status)
	})
}

func (e *storeEnv) archiveTransaction(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	transactionID string,
) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.ArchiveTransaction(tb.Context(), tx, scope, transactionID)
	})
}

// recurringProduct is a subscription product priced in whole dollars.
func recurringProduct(name string) *Product {
	return &Product{
		Name:                  name,
		Description:           "the paid tier",
		Kind:                  KindRecurring,
		AmountCents:           2_500,
		Currency:              "USD",
		BillingIntervalMonths: 1,
	}
}

// oneTimeProduct is a product sold once.
func oneTimeProduct(name string) *Product {
	return &Product{
		Name:        name,
		Kind:        KindOneTime,
		AmountCents: 999,
		Currency:    "USD",
	}
}

// currentSubscription is an agreement whose paid period covers testNow.
func currentSubscription(productID, accountID string) *Subscription {
	return &Subscription{
		BelongsToAccount:   accountID,
		ProductID:          productID,
		Status:             capitalism.SubscriptionStatusActive,
		CurrentPeriodStart: testNow.Add(-24 * time.Hour),
		CurrentPeriodEnd:   testNow.Add(30 * 24 * time.Hour),
	}
}

// lapsedSubscription is an agreement whose paid period ended before testNow.
func lapsedSubscription(productID, accountID string) *Subscription {
	return &Subscription{
		BelongsToAccount:   accountID,
		ProductID:          productID,
		Status:             capitalism.SubscriptionStatusCanceled,
		CurrentPeriodStart: testNow.Add(-60 * 24 * time.Hour),
		CurrentPeriodEnd:   testNow.Add(-30 * 24 * time.Hour),
	}
}

// outstandingPurchase is a sale whose money has not arrived.
func outstandingPurchase(productID, accountID string) *Purchase {
	return &Purchase{
		BelongsToAccount: accountID,
		ProductID:        productID,
		AmountCents:      999,
		Currency:         "USD",
	}
}

// pendingTransaction is a ledger row for an attempt still in flight.
func pendingTransaction(accountID string) *Transaction {
	return &Transaction{
		BelongsToAccount: accountID,
		Status:           TransactionPending,
		AmountCents:      999,
		Currency:         "USD",
	}
}

// mustCreateProduct writes a product and fails the test if it will not go in.
func mustCreateProduct(
	tb testing.TB,
	e *storeEnv,
	store *SQLStore,
	scope tenancy.Scope,
	product *Product,
) *Product {
	tb.Helper()

	created, err := e.createProduct(tb, store, scope, product)
	must.NoError(tb, err)
	must.NotNil(tb, created)

	return created
}

// mustCreateSubscription writes a subscription and fails the test if it will not
// go in.
func mustCreateSubscription(
	tb testing.TB,
	e *storeEnv,
	store *SQLStore,
	scope tenancy.Scope,
	subscription *Subscription,
) *Subscription {
	tb.Helper()

	created, err := e.createSubscription(tb, store, scope, subscription)
	must.NoError(tb, err)
	must.NotNil(tb, created)

	return created
}

// mustCreatePurchase writes a purchase and fails the test if it will not go in.
func mustCreatePurchase(
	tb testing.TB,
	e *storeEnv,
	store *SQLStore,
	scope tenancy.Scope,
	purchase *Purchase,
) *Purchase {
	tb.Helper()

	created, err := e.createPurchase(tb, store, scope, purchase)
	must.NoError(tb, err)
	must.NotNil(tb, created)

	return created
}

// mustRecordTransaction writes a ledger row and fails the test if it will not go
// in.
func mustRecordTransaction(
	tb testing.TB,
	e *storeEnv,
	store *SQLStore,
	scope tenancy.Scope,
	transaction *Transaction,
) *Transaction {
	tb.Helper()

	recorded, err := e.recordTransaction(tb, store, scope, transaction)
	must.NoError(tb, err)
	must.NotNil(tb, recorded)

	return recorded
}

// stubClock is a manually advanced clock parked at testNow.
//
// This package decides which subscriptions are current by comparing their paid
// period against whatever the clock reads — so a suite racing the wall would be a
// suite whose agreements lapse when the machine is slow. It is built on the
// generated mock, so a method nothing here calls panics rather than quietly
// answering.
type stubClock struct {
	*clockmock.ClockMock

	now time.Time

	mu sync.Mutex
}

var _ clock.Clock = (*stubClock)(nil)

func newStubClock() *stubClock {
	c := &stubClock{now: testNow}

	c.ClockMock = &clockmock.ClockMock{
		NowFunc:   c.read,
		SinceFunc: func(t time.Time) time.Duration { return c.read().Sub(t) },
	}

	return c
}

func (c *stubClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *stubClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}
