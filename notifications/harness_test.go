package notifications

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/notifications/migrations"

	"github.com/primandproper/primitives-go/clock"
	clockmock "github.com/primandproper/primitives-go/clock/mock"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test/must"
)

// baseTime is the instant this suite works relative to. Deliberately mid-month
// and mid-day, so a boundary is never coincidentally "now".
var baseTime = time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

// The two directories this suite works in. Two rather than one because half of
// what these tests assert is that a read in one cannot see the other's rows.
var (
	testScope  = tenancy.Of("acct_1")
	otherScope = tenancy.Of("acct_2")
)

// The two people. Same reasoning: an inbox belongs to somebody inside a scope,
// and the read that forgets the principal is the one that reads their colleague's
// notifications.
const (
	testPrincipal  = "user_1"
	otherPrincipal = "user_2"
)

// errCounterUnavailable stands in for a metrics provider that cannot build an
// instrument.
var errCounterUnavailable = platformerrors.New("the instrument is unavailable")

// errCompanionWrite stands in for the write a consumer makes beside a
// notification — the audit entry, the outbox row, the order itself — failing
// after this store has already written. It is the reason every write here takes
// the caller's transaction, so it is what the rollback cases fail with.
var errCompanionWrite = platformerrors.New("the companion write was refused")

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// stubClock is a manually advanced clock. The read stamp and the last-seen stamp
// are the two values this store writes from a clock, and the tests that care
// about them need a known distance between two calls rather than whatever the
// wall did.
//
// Built on the generated mock so the methods nothing calls fail loudly instead
// of lying.
type stubClock struct {
	*clockmock.ClockMock

	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*stubClock)(nil)

func newStubClock() *stubClock {
	c := &stubClock{now: baseTime}

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

// prefixCounter names a fresh table pair per subtest. Subtests share one
// database and must not share tables: a mark-all-read is keyed on a principal
// and nothing else, so one subtest's inbox would be another's.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect its statements are generated
// for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real SQL
// — placeholder rendering, the conflict clause, the partial indexes — without a
// container.
func newSQLiteEnv(tb testing.TB) *storeEnv {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "notifications.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// newStore migrates a uniquely prefixed table pair and returns a store over it.
func (e *storeEnv) newStore(tb testing.TB, opts ...SQLStoreOption) *SQLStore {
	tb.Helper()

	store, _ := e.newStoreWithPrefix(tb, opts...)

	return store
}

// newStoreWithPrefix is newStore, also handing back the prefix so a test can
// query the tables directly.
func (e *storeEnv) newStoreWithPrefix(tb testing.TB, opts ...SQLStoreOption) (store *SQLStore, prefix string) {
	tb.Helper()

	prefix = fmt.Sprintf("ntf_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err = NewSQLStore(e.client, append([]SQLStoreOption{WithTablePrefix(prefix)}, opts...)...)
	must.NoError(tb, err)

	return store, prefix
}

// inTx runs fn inside a transaction on the environment's database and reports
// what fn returned.
//
// Every write a consumer calls takes the caller's transaction, so a test that
// wants a notification filed opens one — which is what a consumer does. It hands
// back fn's error rather than asserting on it, because a refused write is what
// half of these cases are about and WithTransaction returns the callback's error
// unwrapped.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on: the client's, outside any
// transaction. The cases about a read that joins a transaction pass the Tx
// instead, and they are in the transactions suite.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// create files one notification in a transaction of its own and reports both
// halves of what the write returned: the row, and the error.
//
// The transaction is a detail here rather than the subject: these cases are about
// what the write checks, and a consumer that has nothing to commit alongside
// opens exactly this. What a notification commits *with* is the transactions
// suite.
//
// The row is the answer rather than a convenience. The write does not touch the
// Notification it is handed, so a case that needs the id, the scope or the
// creation time reads them here or not at all — which is the property these
// helpers exist to keep the suite honest about.
func (e *storeEnv) create(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	n *Notification,
) (*Notification, error) {
	tb.Helper()

	var filed *Notification

	err := e.inTx(tb, func(tx database.Tx) error {
		var createErr error

		filed, createErr = store.CreateNotification(tb.Context(), tx, scope, n)

		return createErr
	})

	return filed, err
}

// mustCreate is create for the cases that are about something else: it fails the
// test on a refusal and hands back the row.
func (e *storeEnv) mustCreate(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	n *Notification,
) *Notification {
	tb.Helper()

	filed, err := e.create(tb, store, scope, n)
	must.NoError(tb, err)
	must.NotNil(tb, filed)

	return filed
}

// markRead stamps one notification read in a transaction of its own, reporting
// the row the write answered with.
func (e *storeEnv) markRead(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	principal, id string,
) (*Notification, error) {
	tb.Helper()

	var marked *Notification

	err := e.inTx(tb, func(tx database.Tx) error {
		var markErr error

		marked, markErr = store.MarkNotificationRead(tb.Context(), tx, scope, principal, id)

		return markErr
	})

	return marked, err
}

// markAllRead stamps everything unread in a transaction of its own, handing back
// the count the write reported alongside its error.
func (e *storeEnv) markAllRead(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	principal string,
) (count int64, err error) {
	tb.Helper()

	err = e.inTx(tb, func(tx database.Tx) error {
		var markErr error

		count, markErr = store.MarkAllNotificationsRead(tb.Context(), tx, scope, principal)

		return markErr
	})

	return count, err
}

// archive dismisses one notification in a transaction of its own, reporting the
// row the write answered with.
func (e *storeEnv) archive(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	principal, id string,
) (*Notification, error) {
	tb.Helper()

	var archived *Notification

	err := e.inTx(tb, func(tx database.Tx) error {
		var archiveErr error

		archived, archiveErr = store.ArchiveNotification(tb.Context(), tx, scope, principal, id)

		return archiveErr
	})

	return archived, err
}

// register records one device in a transaction of its own, reporting the row the
// write answered with.
func (e *storeEnv) register(tb testing.TB, store *SQLStore, scope tenancy.Scope, d *Device) (*Device, error) {
	tb.Helper()

	var registered *Device

	err := e.inTx(tb, func(tx database.Tx) error {
		var registerErr error

		registered, registerErr = store.RegisterDevice(tb.Context(), tx, scope, d)

		return registerErr
	})

	return registered, err
}

// mustRegister is register for the cases that are about something else.
func (e *storeEnv) mustRegister(tb testing.TB, store *SQLStore, scope tenancy.Scope, d *Device) *Device {
	tb.Helper()

	registered, err := e.register(tb, store, scope, d)
	must.NoError(tb, err)
	must.NotNil(tb, registered)

	return registered
}

// revoke removes one registration in a transaction of its own, reporting the
// registration the write says it removed.
func (e *storeEnv) revoke(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	principal, deviceID string,
) (*Device, error) {
	tb.Helper()

	var revoked *Device

	err := e.inTx(tb, func(tx database.Tx) error {
		var revokeErr error

		revoked, revokeErr = store.RevokeDevice(tb.Context(), tx, scope, principal, deviceID)

		return revokeErr
	})

	return revoked, err
}

// newNotification is one inbox row's worth of input, with everything the store
// requires filled in.
func newNotification(principal, topic, title string) *Notification {
	return &Notification{
		Scope:     testScope,
		Principal: principal,
		Topic:     topic,
		Title:     title,
		Body:      "the body",
		Link:      "/orders/1",
	}
}

// newDevice is one registration's worth of input.
func newDevice(principal string, platform Platform, token string) *Device {
	return &Device{
		Scope:     testScope,
		Principal: principal,
		Platform:  platform,
		Token:     token,
	}
}
