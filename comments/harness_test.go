package comments

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/comments/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// The two directories this suite works in. Two rather than one because half of
// what these tests assert is that a read in one cannot see the other's rows.
var (
	testScope  = tenancy.Of("acct_1")
	otherScope = tenancy.Of("acct_2")
)

// The two people. A comment belongs to a scope and was written by somebody
// inside it, and the author is what the privacy path keys on.
const (
	testAuthor  = "user_1"
	otherAuthor = "user_2"
)

// The target vocabulary this suite registers, plus one type deliberately outside
// it for the rejection paths.
const (
	recipeType  TargetType = "recipe"
	mealType    TargetType = "meal"
	unknownType TargetType = "unregistered"
)

// testTargets is the catalog the unit tests build stores against. Neither type
// carries an existence check: the tests that want one register it themselves,
// because "no check" is the ordinary case and the check is what the interesting
// tests are about.
var testTargets = Targets{
	recipeType: {Description: "a recipe"},
	mealType:   {Description: "a meal"},
}

// testTarget is the thing most of these comments are about.
var testTarget = Target{Type: recipeType, ID: "recipe_1"}

// errCheckUnavailable stands in for an existence check that cannot reach the
// table it would have to read.
var errCheckUnavailable = platformerrors.New("the target table is unavailable")

// errCompanionWrite stands in for the write a consumer makes beside a comment —
// the audit entry, the outbox event — failing after the comment itself is in the
// transaction.
var errCompanionWrite = platformerrors.New("the companion write failed")

// errCounterUnavailable stands in for a metrics provider that cannot build an
// instrument.
var errCounterUnavailable = platformerrors.New("the instrument is unavailable")

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

// prefixCounter names a fresh table per subtest. Subtests share one database and
// must not share a table: a target's discussion is keyed on the scope and the
// target and nothing else, so one subtest's thread would be another's.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect its statements are generated
// for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real SQL
// — placeholder rendering, the partial indexes, the empty-string parent — without
// a container.
func newSQLiteEnv(tb testing.TB) *storeEnv {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "comments.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// newStore migrates a uniquely prefixed table and returns a store over it,
// carrying testTargets unless the caller supplies its own catalog after.
func (e *storeEnv) newStore(tb testing.TB, opts ...SQLStoreOption) *SQLStore {
	tb.Helper()

	store, _ := e.newStoreWithPrefix(tb, opts...)

	return store
}

// newStoreWithPrefix is newStore, also handing back the prefix so a test can
// query the table directly.
func (e *storeEnv) newStoreWithPrefix(tb testing.TB, opts ...SQLStoreOption) (store *SQLStore, prefix string) {
	tb.Helper()

	prefix = fmt.Sprintf("cm_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	base := []SQLStoreOption{WithTablePrefix(prefix), WithTargets(testTargets)}

	store, err = NewSQLStore(e.client, append(base, opts...)...)
	must.NoError(tb, err)

	return store, prefix
}

// inTx runs fn inside a transaction on the environment's database and reports
// what fn returned.
//
// Every write in this store takes the caller's transaction, so a test that wants
// a comment written opens one — which is what a consumer does. It hands back
// fn's error rather than asserting on it, because a refused write is what half of
// these cases are about and RunInTransaction returns the callback's error
// unwrapped.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on: the client's, outside any
// transaction. The cases about a read that joins a transaction pass the Tx
// instead, and they are in the transactions suite.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// create writes one comment in a transaction of its own, handing back the stored
// row and what the write returned.
//
// The transaction is a detail here rather than the subject: these cases are about
// what the write checks, and a consumer that has nothing to commit alongside
// opens exactly this. What a comment commits *with* is the transactions suite.
func (e *storeEnv) create(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	comment *Comment,
) (*Comment, error) {
	tb.Helper()

	var created *Comment

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		created, txErr = store.CreateComment(tb.Context(), tx, scope, comment)

		return txErr
	})

	return created, err
}

// createErr is create for the cases whose subject is the refusal rather than the
// row, so a refused write reads as one expression.
func (e *storeEnv) createErr(tb testing.TB, store *SQLStore, scope tenancy.Scope, comment *Comment) error {
	tb.Helper()

	_, err := e.create(tb, store, scope, comment)

	return err
}

// update revises one comment in a transaction of its own, handing back the
// revised row and what the write returned.
func (e *storeEnv) update(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	comment *Comment,
) (*Comment, error) {
	tb.Helper()

	var revised *Comment

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		revised, txErr = store.UpdateComment(tb.Context(), tx, scope, comment)

		return txErr
	})

	return revised, err
}

// updateErr is update for the cases whose subject is the refusal rather than the
// row.
func (e *storeEnv) updateErr(tb testing.TB, store *SQLStore, scope tenancy.Scope, comment *Comment) error {
	tb.Helper()

	_, err := e.update(tb, store, scope, comment)

	return err
}

// archive removes one comment from the discussion in a transaction of its own,
// handing back the hidden row and what the write returned.
func (e *storeEnv) archive(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	commentID string,
) (*Comment, error) {
	tb.Helper()

	var hidden *Comment

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		hidden, txErr = store.ArchiveComment(tb.Context(), tx, scope, commentID)

		return txErr
	})

	return hidden, err
}

// archiveErr is archive for the cases whose subject is the refusal rather than
// the row.
func (e *storeEnv) archiveErr(tb testing.TB, store *SQLStore, scope tenancy.Scope, commentID string) error {
	tb.Helper()

	_, err := e.archive(tb, store, scope, commentID)

	return err
}

// newComment is one row's worth of input, with everything the store requires
// filled in.
func newComment(author, body string) *Comment {
	return &Comment{
		Scope:  testScope,
		Target: testTarget,
		Author: author,
		Body:   body,
	}
}

// written creates a comment and returns the stored row, for the tests whose
// subject is what happens next rather than the write. It writes under the scope
// the comment carries, which is what a fixture means by naming one.
//
// It hands back what the write answered with rather than the value it was given:
// the create does not touch its argument, so the id and the creation time are on
// the returned row alone, and a fixture that returned the input would be a
// fixture whose ID is empty.
func written(tb testing.TB, e *storeEnv, store *SQLStore, comment *Comment) *Comment {
	tb.Helper()

	stored, err := e.create(tb, store, comment.Scope, comment)
	must.NoError(tb, err)

	return stored
}

// reply is one reply's worth of input, naming no target so that it adopts its
// parent's — which is the ordinary shape of a client that has a comment id.
func reply(parentID, author, body string) *Comment {
	return &Comment{
		Scope:    testScope,
		ParentID: parentID,
		Author:   author,
		Body:     body,
	}
}

// recordingCheck is a target definition's existence hook that answers what it is
// told to and records what it was asked about.
//
// It is guarded because a hook is called from whatever goroutine is writing a
// comment, and one shared across two of them would be a data race rather than an
// assertion.
type recordingCheck struct {
	err    error
	asked  []string
	scopes []tenancy.Scope
	mu     sync.Mutex
	answer bool
}

func newRecordingCheck(answer bool, err error) *recordingCheck {
	return &recordingCheck{answer: answer, err: err}
}

func (c *recordingCheck) exists(_ context.Context, scope tenancy.Scope, targetID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.asked = append(c.asked, targetID)
	c.scopes = append(c.scopes, scope)

	return c.answer, c.err
}
