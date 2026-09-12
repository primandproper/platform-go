package waitlists

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/waitlists/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	clockmock "github.com/primandproper/primitives-go/v2/clock/mock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

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

// prefixCounter names a fresh table set per subtest. Subtests share one database
// and must not share tables: a contact is unique per list across live and
// archived rows alike, and a suite that reused one table set would have subtests
// colliding on contacts they each chose freely.
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

// testSubject is whose signups the subject-keyed reads are about.
var testSubject = Subject{Type: SubjectUser, ID: "user-1"}

// testNow is the instant the suite's clock starts at. Every closing time below
// is expressed relative to it, so a list is open or closed because the test said
// so rather than because the wall clock happened to agree.
//
// It is well clear of both horizons the schema has to survive: the SQLite
// lexicographic comparison over a text column wants a four-digit year, and the
// MySQL DATETIME range starts in 1000.
var testNow = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// storeEnv is one live database plus the dialect it speaks.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real SQL
// — placeholder rendering, the guarded updates' predicates, the partial indexes
// — without a container.
func newSQLiteEnv(tb testing.TB) *storeEnv {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "waitlists.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// newStore migrates a uniquely prefixed table set and returns a store over it,
// on a clock parked at testNow.
func (e *storeEnv) newStore(tb testing.TB, opts ...SQLStoreOption) *SQLStore {
	tb.Helper()

	store, _ := e.newStoreWithPrefix(tb, opts...)

	return store
}

// newStoreWithPrefix is newStore, also handing back the prefix so a test can
// reach the tables directly.
func (e *storeEnv) newStoreWithPrefix(tb testing.TB, opts ...SQLStoreOption) (store *SQLStore, prefix string) {
	tb.Helper()

	prefix = fmt.Sprintf("wl_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	base := []SQLStoreOption{WithTablePrefix(prefix), WithClock(newStubClock())}

	store, err = NewSQLStore(e.client, append(base, opts...)...)
	must.NoError(tb, err)

	return store, prefix
}

// inTx runs fn inside a transaction on the environment's database and reports
// what fn returned.
//
// Every write in this store takes the caller's transaction, so a test that wants
// a list opened or somebody joined opens one — which is what a consumer does. It
// hands back fn's error rather than asserting on it, because a refused write is
// what half of these cases are about and RunInTransaction returns the callback's
// error unwrapped.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on: the client's, outside any
// transaction. The cases about a read that joins a transaction pass the Tx
// instead, and they are in the transactions suite.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// The nine writes, each in a transaction of its own, reporting both halves of
// what the write returned: the row, and the error.
//
// The transaction is a detail in these rather than the subject: the list, signup
// and withdrawal suites are about what a write checks and what it leaves behind,
// and a consumer with nothing to commit alongside opens exactly this. What a
// signup commits *with* is the transactions suite, which calls the store
// directly.
//
// The row is an answer rather than a convenience. No write here touches the
// value it was handed, so a case that wants to know what the statement settled —
// the stamp, the status, the contact a withdrawal is about to blank — reads it
// off what came back or not at all. Eight of the nine answer with the row after
// the write; withdraw is the one whose answer is the row from before it.

func (e *storeEnv) createList(tb testing.TB, store *SQLStore, scope tenancy.Scope, list *List) (*List, error) {
	tb.Helper()

	var created *List

	err := e.inTx(tb, func(tx database.Tx) (createErr error) {
		created, createErr = store.CreateList(tb.Context(), tx, scope, list)

		return createErr
	})

	return created, err
}

func (e *storeEnv) updateList(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	list *List,
) (*List, error) {
	tb.Helper()

	var updated *List

	err := e.inTx(tb, func(tx database.Tx) (updateErr error) {
		updated, updateErr = store.UpdateList(tb.Context(), tx, scope, list)

		return updateErr
	})

	return updated, err
}

func (e *storeEnv) archiveList(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	listID string,
) (*List, error) {
	tb.Helper()

	var archived *List

	err := e.inTx(tb, func(tx database.Tx) (archiveErr error) {
		archived, archiveErr = store.ArchiveList(tb.Context(), tx, scope, listID)

		return archiveErr
	})

	return archived, err
}

func (e *storeEnv) join(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	listID string,
	signup *Signup,
) (*Signup, error) {
	tb.Helper()

	var joined *Signup

	err := e.inTx(tb, func(tx database.Tx) (joinErr error) {
		joined, joinErr = store.Join(tb.Context(), tx, scope, listID, signup)

		return joinErr
	})

	return joined, err
}

func (e *storeEnv) updateNotes(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	listID, signupID, notes string,
) (*Signup, error) {
	tb.Helper()

	var updated *Signup

	err := e.inTx(tb, func(tx database.Tx) (updateErr error) {
		updated, updateErr = store.UpdateSignupNotes(tb.Context(), tx, scope, listID, signupID, notes)

		return updateErr
	})

	return updated, err
}

func (e *storeEnv) invite(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	listID, signupID string,
) (*Signup, error) {
	tb.Helper()

	var invited *Signup

	err := e.inTx(tb, func(tx database.Tx) (inviteErr error) {
		invited, inviteErr = store.Invite(tb.Context(), tx, scope, listID, signupID)

		return inviteErr
	})

	return invited, err
}

func (e *storeEnv) convert(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	listID, signupID string,
) (*Signup, error) {
	tb.Helper()

	var converted *Signup

	err := e.inTx(tb, func(tx database.Tx) (convertErr error) {
		converted, convertErr = store.Convert(tb.Context(), tx, scope, listID, signupID)

		return convertErr
	})

	return converted, err
}

func (e *storeEnv) withdraw(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	listID, signupID string,
) (*Signup, error) {
	tb.Helper()

	var withdrawn *Signup

	err := e.inTx(tb, func(tx database.Tx) (withdrawErr error) {
		withdrawn, withdrawErr = store.Withdraw(tb.Context(), tx, scope, listID, signupID)

		return withdrawErr
	})

	return withdrawn, err
}

func (e *storeEnv) archiveSignup(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	listID, signupID string,
) (*Signup, error) {
	tb.Helper()

	var archived *Signup

	err := e.inTx(tb, func(tx database.Tx) (archiveErr error) {
		archived, archiveErr = store.ArchiveSignup(tb.Context(), tx, scope, listID, signupID)

		return archiveErr
	})

	return archived, err
}

// openList is a list that is still taking signups at testNow.
func openList(name string) *List {
	return &List{
		Name:        name,
		Description: "early access to the beta",
		ClosesAt:    testNow.Add(30 * 24 * time.Hour),
	}
}

// closedList is a list whose closing time has already passed at testNow.
func closedList(name string) *List {
	return &List{Name: name, ClosesAt: testNow.Add(-time.Hour)}
}

// mustCreateList writes a list in a transaction of its own and fails the test if
// it will not go in.
func mustCreateList(tb testing.TB, e *storeEnv, store *SQLStore, scope tenancy.Scope, list *List) *List {
	tb.Helper()

	created, err := e.createList(tb, store, scope, list)
	must.NoError(tb, err)
	must.NotNil(tb, created)

	return created
}

// mustJoin adds a contact to a list in a transaction of its own and fails the
// test if it will not go in.
func mustJoin(
	tb testing.TB,
	e *storeEnv,
	store *SQLStore,
	scope tenancy.Scope,
	listID string,
	signup *Signup,
) *Signup {
	tb.Helper()

	joined, err := e.join(tb, store, scope, listID, signup)
	must.NoError(tb, err)
	must.NotNil(tb, joined)

	return joined
}

// refused reports only the error half of a write's answer.
//
// It is for the cases whose subject is the refusal itself, where naming the row
// would mean a blank identifier on every line. That the row is nil whenever the
// error is not is a claim about all nine writes rather than about any one case,
// so it is asserted once — see TestSQLStore_RefusedWritesAnswerWithNoRow.
func refused[T any](_ *T, err error) error { return err }

// mustArchiveList and mustArchiveSignup retire a row in a transaction of their
// own and fail the test if it will not go, handing back the row the write hid.
func mustArchiveList(tb testing.TB, e *storeEnv, store *SQLStore, scope tenancy.Scope, listID string) *List {
	tb.Helper()

	archived, err := e.archiveList(tb, store, scope, listID)
	must.NoError(tb, err)
	must.NotNil(tb, archived)

	return archived
}

func mustArchiveSignup(
	tb testing.TB,
	e *storeEnv,
	store *SQLStore,
	scope tenancy.Scope,
	listID, signupID string,
) *Signup {
	tb.Helper()

	archived, err := e.archiveSignup(tb, store, scope, listID, signupID)
	must.NoError(tb, err)
	must.NotNil(tb, archived)

	return archived
}

// mustInvite and mustWithdraw move a signup in a transaction of its own and fail
// the test if the move will not go through. They are the fixture forms, for the
// cases whose subject is what happens *after* the move.
func mustInvite(
	tb testing.TB,
	e *storeEnv,
	store *SQLStore,
	scope tenancy.Scope,
	listID, signupID string,
) *Signup {
	tb.Helper()

	invited, err := e.invite(tb, store, scope, listID, signupID)
	must.NoError(tb, err)
	must.NotNil(tb, invited)

	return invited
}

func mustWithdraw(
	tb testing.TB,
	e *storeEnv,
	store *SQLStore,
	scope tenancy.Scope,
	listID, signupID string,
) *Signup {
	tb.Helper()

	withdrawn, err := e.withdraw(tb, store, scope, listID, signupID)
	must.NoError(tb, err)
	must.NotNil(tb, withdrawn)

	return withdrawn
}

// stubClock is a manually advanced clock parked at testNow.
//
// This package decides whether a list is open by comparing its closing time
// against whatever the clock reads, and stamps every transition from the same
// reading — so a suite racing the wall would be a suite whose lists close when
// the machine is slow. It is built on the generated mock, so a method nothing
// here calls panics rather than quietly answering.
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
