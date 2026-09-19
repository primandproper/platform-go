package passwordreset

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

// testUserID is the principal most of these tests reset.
const testUserID = "user_01"

// testScope is a named tenant, deliberately not Global: a store that dropped its
// scope predicate would still pass every assertion made under Global, since the
// empty identifier is what an unscoped column holds anyway.
func testScope() tenancy.Scope { return tenancy.Of("tenant_a") }

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct {
	connectionString string

	// maxOpenConns is one for SQLite, whose writers serialize on the file
	// anyway, and several for a container run — the case that proves one token
	// goes to one consumer means nothing if the pool hands every contender the
	// same connection.
	maxOpenConns int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string { return c.connectionString }

// A container reports "ready" from its log line slightly before it accepts TCP
// connections, so the first statement after construction can land on a socket
// that is still closing. These values give IsReady room to ride that out; a
// SQLite client succeeds on the first ping and pays none of it.
func (c *testClientConfig) GetMaxPingAttempts() uint64       { return 30 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration { return time.Second }
func (c *testClientConfig) GetMaxIdleConns() int             { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int {
	if c.maxOpenConns > 0 {
		return c.maxOpenConns
	}

	return 1
}

func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// fakeClock is a Clock whose time only moves when a test moves it.
type fakeClock struct {
	now    time.Time
	ticker chan time.Time
	mu     sync.Mutex
}

var _ clock.Clock = (*fakeClock)(nil)

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:    time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC),
		ticker: make(chan time.Time),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Since(t time.Time) time.Duration                  { return c.Now().Sub(t) }
func (c *fakeClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func (c *fakeClock) NewTicker(_ time.Duration) clock.Ticker { return &fakeTicker{c: c} }

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// tick releases one iteration of the background sweep loop.
func (c *fakeClock) tick() { c.ticker <- c.Now() }

// fakeTicker hands the loop the channel the test drives.
type fakeTicker struct {
	c *fakeClock
}

var _ clock.Ticker = (*fakeTicker)(nil)

func (t *fakeTicker) Chan() <-chan time.Time { return t.c.ticker }
func (t *fakeTicker) Stop()                  {}

// newTestClient builds a SQLite-backed client with the token table created.
//
// SQLite exercises the real SQL — the placeholder rendering, the transaction
// Consume runs in, the unique index the digest column carries — without a
// container, so the store's core behavior is covered by `make test` rather than
// only by integration runs.
func newTestClient(tb testing.TB) database.Client {
	tb.Helper()

	client := openTestClient(tb, filepath.Join(tb.TempDir(), "passwordreset.db"))

	createTable(tb, client, dialect.SQLite, DefaultTablePrefix)

	return client
}

// openTestClient connects to a SQLite file without creating anything in it, for
// the callers that want a different set of tables than newTestClient makes.
func openTestClient(tb testing.TB, path string) database.Client {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(), &testClientConfig{connectionString: path})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return client
}

// createTable runs the shipped DDL against a client.
func createTable(tb testing.TB, client database.Client, d dialect.Dialect, prefix string) {
	tb.Helper()

	stmts, err := migrations.Statements(d, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr)
	}
}

// newTestStore builds a store over a fresh SQLite database and a clock the test
// controls.
func newTestStore(tb testing.TB, opts ...Option) (*SQLStore, *fakeClock) {
	tb.Helper()

	c := newFakeClock()

	store, err := NewSQLStore(&Config{}, newTestClient(tb), append([]Option{
		WithClock(c),
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	}, opts...)...)
	must.NoError(tb, err)

	return store, c
}

// withTx runs fn inside a real transaction on the store's client.
//
// Every write in these tests goes through it, because the store opens no
// transaction of its own any more — and it is also the production shape for a
// caller with nothing to join: Client.WithTransaction, with the Tx passed
// straight through. A refusal rolls the transaction back and comes back
// unwrapped, which is what lets these tests keep asserting on the sentinel.
func withTx(tb testing.TB, store *SQLStore, fn func(tx database.Tx) error) error {
	tb.Helper()

	return store.db.WithTransaction(tb.Context(), fn)
}

// issue mints one token for the usual principal, failing the test if it cannot.
func issue(tb testing.TB, store *SQLStore, ttl time.Duration) *Issuance {
	tb.Helper()

	issuance, err := issueFor(tb, store, testScope(), testUserID, ttl)
	must.NoError(tb, err)
	must.NotNil(tb, issuance)

	return issuance
}

// issueFor mints one token for a named principal in a named scope, reporting
// what the store said rather than failing on it.
func issueFor(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
	ttl time.Duration,
) (*Issuance, error) {
	tb.Helper()

	var issuance *Issuance

	err := withTx(tb, store, func(tx database.Tx) error {
		var issueErr error
		issuance, issueErr = store.Issue(tb.Context(), tx, scope, userID, ttl)

		return issueErr
	})

	return issuance, err
}

// verify resolves a secret through the write pool, which is the executor a page
// load holds: a replica can answer "not found" for a link that arrived seconds
// ago. The cases that want the transaction's own view pass a Tx instead.
func verify(tb testing.TB, store *SQLStore, scope tenancy.Scope, secret string) (*Token, error) {
	tb.Helper()

	return store.Verify(tb.Context(), store.db.Writer(), scope, secret)
}

// consume spends a secret in a transaction of its own, which is what a caller
// with nothing else to write does.
func consume(tb testing.TB, store *SQLStore, scope tenancy.Scope, secret string) (*Token, error) {
	tb.Helper()

	var token *Token

	err := withTx(tb, store, func(tx database.Tx) error {
		var consumeErr error
		token, consumeErr = store.Consume(tb.Context(), tx, scope, secret)

		return consumeErr
	})

	return token, err
}

// revokeForUser destroys one principal's outstanding tokens in a transaction of
// its own.
func revokeForUser(tb testing.TB, store *SQLStore, scope tenancy.Scope, userID string) (int64, error) {
	tb.Helper()

	var revoked int64

	err := withTx(tb, store, func(tx database.Tx) error {
		var revokeErr error
		revoked, revokeErr = store.RevokeForUser(tb.Context(), tx, scope, userID)

		return revokeErr
	})

	return revoked, err
}

// deleteForUser destroys every token one principal holds, in a transaction of
// its own.
func deleteForUser(tb testing.TB, store *SQLStore, scope tenancy.Scope, userID string) (int64, error) {
	tb.Helper()

	var deleted int64

	err := withTx(tb, store, func(tx database.Tx) error {
		var deleteErr error
		deleted, deleteErr = store.DeleteForUser(tb.Context(), tx, scope, userID)

		return deleteErr
	})

	return deleted, err
}

// recordingLogger counts what was logged as an error, for the one code path in
// this package whose only effect is a log line: the background sweep, which
// nothing is waiting on.
type recordingLogger struct {
	logging.Logger

	errors []string

	mu sync.Mutex
}

var _ logging.Logger = (*recordingLogger)(nil)

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{Logger: loggingnoop.NewLogger()}
}

func (l *recordingLogger) Error(whatWasHappening string, _ error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.errors = append(l.errors, whatWasHappening)
}

// The derivation methods hand back this same recorder, so that a logger named
// by observability.NewObserver still records.
func (l *recordingLogger) Clone() logging.Logger                    { return l }
func (l *recordingLogger) WithName(string) logging.Logger           { return l }
func (l *recordingLogger) WithValue(string, any) logging.Logger     { return l }
func (l *recordingLogger) WithValues(map[string]any) logging.Logger { return l }
func (l *recordingLogger) WithError(error) logging.Logger           { return l }
func (l *recordingLogger) WithSpan(trace.Span) logging.Logger       { return l }

// count reports how often one message was logged as an error. It counts by
// message rather than in total because Sweep records its own failure through the
// same logger, and the loop's line is the one under test.
func (l *recordingLogger) count(message string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	var n int
	for _, logged := range l.errors {
		if logged == message {
			n++
		}
	}

	return n
}

// rowsIn counts the rows in one table, for the assertions about which table a
// namespaced store addressed. It is raw SQL in a test, which is the one place
// this package still has any: the point of the assertion is the table name.
func rowsIn(t *testing.T, client database.Client, table string) int {
	t.Helper()

	var count int
	must.NoError(t, client.Writer().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))

	return count
}

// The flow's fixtures: everything Service needs that is not the store.

// testEmailAddress is the address most of the flow tests ask for a reset at.
const testEmailAddress = "reset.me@example.com"

// testUsersTable is the directory double's storage. It lives in the same
// database the tokens do, which is the whole point: the property the flow
// exists for is that the password write and the redemption commit together, and
// a directory that kept its passwords in a map would report success for a
// transaction that rolled back.
const testUsersTable = `CREATE TABLE test_users (
	id               TEXT NOT NULL PRIMARY KEY,
	email_address    TEXT NOT NULL,
	hashed_password  TEXT NOT NULL
)`

// testDirectory is a Directory over that table.
//
// Reads come out of a map because nothing in this flow writes a user row, and
// the write goes through the Tx it is handed because everything in this flow
// depends on it doing so.
type testDirectory struct {
	byAddress map[string]*identity.User

	// readErr, when set, is what GetUserByEmailAddress answers instead of
	// looking — the directory being unwell rather than the address being
	// nobody's, which are the two answers the flow must not confuse.
	readErr error

	// updateErr, when set, is what UpdateUserPassword answers instead of
	// writing — the directory being unwell partway through a redemption.
	updateErr error

	// scopes records the scope of every call, so a test can assert the flow
	// passes the one it was given rather than one it derived.
	scopes []tenancy.Scope

	mu sync.Mutex
}

var _ Directory = (*testDirectory)(nil)

func (d *testDirectory) GetUserByEmailAddress(
	_ context.Context,
	_ database.SQLQueryExecutor,
	scope tenancy.Scope,
	emailAddress string,
) (*identity.User, error) {
	d.mu.Lock()
	d.scopes = append(d.scopes, scope)
	d.mu.Unlock()

	if d.readErr != nil {
		return nil, d.readErr
	}

	user, ok := d.byAddress[strings.ToLower(emailAddress)]
	if !ok {
		return nil, platformerrors.Wrap(identity.ErrUserNotFound, "reading a test user by address")
	}

	return user, nil
}

func (d *testDirectory) UpdateUserPassword(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID, hashedPassword string,
) error {
	d.mu.Lock()
	d.scopes = append(d.scopes, scope)
	d.mu.Unlock()

	if d.updateErr != nil {
		return d.updateErr
	}

	_, err := tx.ExecContext(ctx,
		`UPDATE test_users SET hashed_password = ? WHERE id = ?`, hashedPassword, userID)

	return err
}

// recordingMailer keeps what it was handed and answers with whatever the test
// told it to.
type recordingMailer struct {
	err error

	// inspect runs at send time, which is how a test asserts what was true of
	// the database at the moment the mail went out.
	inspect func(mail *Mail)

	sent []*Mail

	mu sync.Mutex
}

var _ Mailer = (*recordingMailer)(nil)

func (m *recordingMailer) SendPasswordReset(_ context.Context, mail *Mail) error {
	m.mu.Lock()
	m.sent = append(m.sent, mail)
	inspect := m.inspect
	m.mu.Unlock()

	if inspect != nil {
		inspect(mail)
	}

	return m.err
}

func (m *recordingMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sent)
}

func (m *recordingMailer) last() *Mail {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.sent) == 0 {
		return nil
	}

	return m.sent[len(m.sent)-1]
}

// fakeAuthenticator hashes by prefixing, so a test can read a stored hash and
// say which password produced it.
type fakeAuthenticator struct {
	err error
}

var _ authentication.Authenticator = (*fakeAuthenticator)(nil)

func (a *fakeAuthenticator) HashPassword(_ context.Context, password string) (string, error) {
	if a.err != nil {
		return "", a.err
	}

	return "hashed:" + password, nil
}

func (a *fakeAuthenticator) PasswordMatches(_ context.Context, hash, password string) (bool, error) {
	return hash == "hashed:"+password, nil
}

// interceptingStore wraps a Store so one of its methods can fail without the
// others changing. The failures it stands in for are a driver's, which is the
// only way a redemption's three writes come apart in production.
type interceptingStore struct {
	Store

	issueErr  error
	revokeErr error
}

var _ Store = (*interceptingStore)(nil)

func (s *interceptingStore) Issue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
	ttl time.Duration,
) (*Issuance, error) {
	if s.issueErr != nil {
		return nil, s.issueErr
	}

	return s.Store.Issue(ctx, tx, scope, userID, ttl)
}

func (s *interceptingStore) RevokeForUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
) (int64, error) {
	if s.revokeErr != nil {
		return 0, s.revokeErr
	}

	return s.Store.RevokeForUser(ctx, tx, scope, userID)
}

// sleepRecordingClock is a fakeClock that records every duration it was asked
// to sleep for and advances itself by it, so a test can assert the deadline two
// code paths were held to without waiting for either.
type sleepRecordingClock struct {
	*fakeClock

	slept []time.Duration

	mu sync.Mutex
}

var _ clock.Clock = (*sleepRecordingClock)(nil)

func newSleepRecordingClock() *sleepRecordingClock {
	return &sleepRecordingClock{fakeClock: newFakeClock()}
}

func (c *sleepRecordingClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	c.mu.Unlock()

	// A caller who has gone gets the wait cut short, which is what a real clock
	// does and what the flow records on its span.
	if err := ctx.Err(); err != nil {
		return err
	}

	c.advance(d)

	return nil
}

func (c *sleepRecordingClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.slept)
}

// serviceEnv is one wired-up flow and the pieces a test reaches back into.
type serviceEnv struct {
	service   *Service
	store     *SQLStore
	tokens    *interceptingStore
	directory *testDirectory
	mailer    *recordingMailer
	auth      *fakeAuthenticator
	client    database.Client
	clock     *sleepRecordingClock

	// storeClock is what the tokens expire against, which is a different clock
	// from the flow's: one test advances a link past its deadline without
	// moving the one the request floor is measured on.
	storeClock *fakeClock
}

// newTestService builds the flow over a fresh SQLite database holding both the
// token table and the directory double's.
func newTestService(tb testing.TB, opts ...ServiceOption) *serviceEnv {
	tb.Helper()

	path := filepath.Join(tb.TempDir(), "passwordreset.db")
	client := openTestClient(tb, path)

	createTable(tb, client, dialect.SQLite, DefaultTablePrefix)

	_, err := client.Writer().ExecContext(tb.Context(), testUsersTable)
	must.NoError(tb, err)

	_, err = client.Writer().ExecContext(tb.Context(),
		`INSERT INTO test_users (id, email_address, hashed_password) VALUES (?, ?, ?)`,
		testUserID, testEmailAddress, "hashed:original")
	must.NoError(tb, err)

	storeClock := newFakeClock()

	store, err := NewSQLStore(&Config{}, client,
		WithClock(storeClock),
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	)
	must.NoError(tb, err)

	env := &serviceEnv{
		storeClock: storeClock,
		store:      store,
		tokens:     &interceptingStore{Store: store},
		directory: &testDirectory{byAddress: map[string]*identity.User{
			testEmailAddress: {ID: testUserID, EmailAddress: testEmailAddress, Scope: testScope()},
		}},
		mailer: &recordingMailer{},
		auth:   &fakeAuthenticator{},
		client: client,
		clock:  newSleepRecordingClock(),
	}

	env.service, err = NewService(client, env.tokens, env.directory, env.auth, env.mailer,
		append([]ServiceOption{
			WithServiceClock(env.clock),
			WithServiceLogger(loggingnoop.NewLogger()),
			WithServiceTracerProvider(tracingnoop.NewTracerProvider()),
		}, opts...)...)
	must.NoError(tb, err)

	return env
}

// storedPassword reads the directory double's column back, on a connection that
// is not in anybody's transaction — so what it answers is what committed.
func (e *serviceEnv) storedPassword(tb testing.TB) string {
	tb.Helper()

	var hashed string

	must.NoError(tb, e.client.Writer().
		QueryRowContext(tb.Context(), `SELECT hashed_password FROM test_users WHERE id = ?`, testUserID).
		Scan(&hashed))

	return hashed
}

// liveTokens counts the principal's unspent, unexpired tokens.
func (e *serviceEnv) liveTokens(tb testing.TB) int {
	tb.Helper()

	tokens, err := e.store.ListForUser(tb.Context(), e.client.Writer(), testScope(), testUserID)
	must.NoError(tb, err)

	live := 0

	for _, token := range tokens {
		if token.Live(e.store.clock.Now()) {
			live++
		}
	}

	return live
}

// request runs one reset request and hands back the secret that was mailed.
func (e *serviceEnv) request(tb testing.TB) string {
	tb.Helper()

	before := e.mailer.count()

	must.NoError(tb, e.service.Request(tb.Context(), testScope(), testEmailAddress))
	must.EqOp(tb, before+1, e.mailer.count())

	mail := e.mailer.last()
	must.NotNil(tb, mail)
	must.NotNil(tb, mail.Issuance)

	return mail.Issuance.Secret
}
