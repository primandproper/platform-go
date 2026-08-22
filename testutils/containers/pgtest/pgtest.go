// Package pgtest provides the postgres testcontainer setup that every
// postgres-backed suite in this repo would otherwise hand-roll: start the
// container with the shared retry policy and wait strategy, open a pgx-backed
// *sql.DB against it, ping it, and tear all of it down afterwards.
//
// Callers describe the shape they want with Options and receive a live Instance
// inside a closure, so a test body says what it does with postgres and nothing
// about how postgres is stood up or torn down.
//
// # One server, one isolated database per test
//
// A container per test gives perfect isolation at a price that stops scaling
// once a package has a few dozen: every test pays a container start plus a full
// migration replay, and a package running its tests in parallel asks the Docker
// daemon for that many postgres instances at once. Past a certain width the
// daemon stops answering and containers fail their readiness wait — not because
// anything is wrong with the test, but because nothing was rationing the daemon.
//
// Isolation does not require a container, though. Start one Instance per test
// binary and hand each test its own corner of it:
//
//   - Instance.Schema creates a private schema, injects it through the DSN's
//     search_path, and drops it on cleanup. Cheap, and available on managed
//     postgres where CREATE DATABASE is restricted.
//   - Instance.Template migrates one database, and Template.Clone copies it per
//     test with CREATE DATABASE ... TEMPLATE — a file copy rather than a replay
//     of every migration. Stronger isolation, since it covers extensions and
//     everything else schema-scoped rules do not, and no search_path to inject.
//
// Neither is strictly better. Schemas are cheaper and portable; clones isolate
// more and skip per-test migration entirely.
//
// # The lock key that makes schemas parallel
//
// Schema-isolated tests migrate concurrently, and they must not serialize on
// one advisory lock while doing it. Pass migrate.WithSchemaScopedLockKey() to
// the Migrator you hand to WithMigration: it derives the lock ID from the
// connection's current schema, so deployments on the default schema still share
// one lock and test schemas never contend with each other. Without it, parallel
// setup becomes a queue.
//
// Clones need no such thing. Postgres advisory locks are per-database, and
// migrations run once into the template before any test starts.
//
// # Per-binary setup from TestMain
//
// Run is one of the two per-binary shapes; TestMain is the other, and it is the
// one a suite arrives with when it already had per-package fixtures. A
// *testing.M is not a testing.TB and cannot be adapted into one — the interface
// has an unexported method for exactly that reason — so Start and
// Instance.NewTemplate are Run and Instance.Template with the two testing.TB
// decisions handed back instead of taken: teardown is returned rather than
// registered, and an unavailable postgres is ErrNoPostgres rather than a skip.
//
//	var template *pgtest.Template
//
//	func TestMain(m *testing.M) { os.Exit(run(m)) }
//
//	func run(m *testing.M) int {
//		// testing.Short() panics before flag.Parse, so a TestMain that gates on
//		// -short parses first. Without the gate a -short run starts a container
//		// and then skips every test that would have queried it.
//		flag.Parse()
//		if testing.Short() {
//			return m.Run()
//		}
//
//		pg, teardown, err := pgtest.Start(context.Background())
//		if err != nil {
//			// ErrNoPostgres means nothing was started, which is this suite's
//			// cue to let its tests skip themselves.
//			return m.Run()
//		}
//		defer func() { _ = teardown() }()
//
//		tmpl, dropTemplate, err := pg.NewTemplate(context.Background(), pgtest.WithMigration(migrator.Migrate))
//		if err != nil {
//			return 1
//		}
//		defer func() { _ = dropTemplate() }()
//
//		template = tmpl
//
//		return m.Run()
//	}
//
// The body is a function returning a code rather than TestMain itself because
// os.Exit does not run deferred functions: teardown has to happen before the
// exit, and the only way to have both is to put the exit outside.
//
// Both teardowns return an error, because draining a pool and terminating a
// container can each fail and there is nothing here to log it to on their
// behalf. A suite that wants to hear about it logs it; the discard above is the
// other legitimate answer, and it is at least written down as one.
//
// Instance.Schema and Template.Clone keep their testing.TB and need no
// companion — by the time either is called there is a test in hand.
package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/testutils/containers"

	// The pgx stdlib driver is registered here so that callers get a working
	// "pgx" driver from importing pgtest alone.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// DefaultImage is the postgres image Run launches when no override is given.
	DefaultImage = "postgres:17-alpine"

	// DriverName is the database/sql driver Instance.DB and Instance.Open use.
	DriverName = "pgx"

	// DefaultMaxConnections is the server-wide connection ceiling Run provisions
	// the container with, replacing postgres' default of 100.
	//
	// One container now serves every test in a binary — Schema and Clone hand out
	// pools against one server rather than one server each — so the ceiling is
	// spent by the whole run at once instead of per test. At the default it is
	// whichever test connects last that fails, with "too many clients already",
	// which reads as flake rather than as the budget it is.
	DefaultMaxConnections = 200

	defaultCredential = "platformtest"

	// mappedPort is the container port Instance.Port is read from.
	mappedPort = "5432/tcp"

	// A cold start on a busy CI host has to cover an image pull plus initdb, and
	// postgres logs its readiness line twice — once for the bootstrap server that
	// runs the init scripts, once for the real one.
	startupDeadline   = 2 * time.Minute
	readyLog          = "database system is ready to accept connections"
	readyLogOccurence = 2
)

// ErrNoPostgres reports that no postgres was available and none was started:
// the RUN_CONTAINER_TESTS gate is closed and WithRequiredPostgres was not
// given. Run turns that situation into a skip, which is a testing.TB's move and
// therefore not one Start can make — so Start names it instead, and the caller
// decides. A TestMain that wants the suite to skip its way through ignores this
// sentinel; one that wants a hard failure returns the error.
var ErrNoPostgres = platformerrors.New("pgtest: no postgres available")

// Option configures Run and Start.
type Option func(*options)

type options struct {
	image          string
	database       string
	username       string
	password       string
	dsnEnvVar      string
	customizers    []testcontainers.ContainerCustomizer
	maxOpenConns   int
	maxConnections int
	required       bool
}

// WithImage overrides DefaultImage. Use it for postgres derivatives that the
// rest of this setup still applies to, e.g. "pgvector/pgvector:pg17".
func WithImage(image string) Option {
	return func(o *options) { o.image = image }
}

// WithCredentials overrides the database name, superuser and password the
// container is provisioned with. Tests that create or drop roles want distinct
// credentials so they cannot collide with the identifiers under test.
func WithCredentials(database, username, password string) Option {
	return func(o *options) {
		o.database, o.username, o.password = database, username, password
	}
}

// WithMaxOpenConns caps Instance.DB's pool. Set it well above the number of
// concurrent subtests sharing an Instance, otherwise they starve each other.
// Zero (the default) leaves database/sql's unlimited default in place.
func WithMaxOpenConns(n int) Option {
	return func(o *options) { o.maxOpenConns = n }
}

// WithMaxConnections overrides DefaultMaxConnections, the server-wide ceiling
// the container is started with. Raise it for a binary whose tests are both
// numerous and parallel; the budget is one number shared by every pool Run,
// Schema and Clone hand out, so it has to cover the widest moment of the run
// rather than the widest single test. Zero leaves the image's own default.
func WithMaxConnections(n int) Option {
	return func(o *options) { o.maxConnections = n }
}

// WithRequiredPostgres makes an unavailable postgres a test failure instead of
// a skip, by way of containers.Required.
//
// The default gate is right for this module and wrong for a service: a library
// whose consumers may have no Docker daemon should skip, while a service whose
// postgres backend is only ever exercised here should fail loudly, because a
// skip is indistinguishable from a pass and a backend can reach zero coverage
// that way without anyone noticing. -short still skips either way.
func WithRequiredPostgres() Option {
	return func(o *options) { o.required = true }
}

// WithDSNFromEnv names an environment variable holding a postgres DSN. When it
// is set and non-empty, Run connects to that server and starts no container at
// all — the first rung of the resolution ladder, ahead of -short and ahead of
// starting anything.
//
// It is how a suite runs against a postgres that CI already provides, and how a
// developer points the whole binary at a local server. The container-only
// fields of Instance are absent on this path: Container is nil, and Database,
// Username and Password are read out of the DSN rather than from
// WithCredentials.
func WithDSNFromEnv(name string) Option {
	return func(o *options) { o.dsnEnvVar = name }
}

// WithCustomizers appends testcontainers customizers to the ones Run already
// applies. They run after the defaults, so they can override the wait strategy.
func WithCustomizers(customizers ...testcontainers.ContainerCustomizer) Option {
	return func(o *options) { o.customizers = append(o.customizers, customizers...) }
}

func newOptions(opts []Option) *options {
	cfg := &options{
		image:          DefaultImage,
		database:       defaultCredential,
		username:       defaultCredential,
		password:       defaultCredential,
		maxConnections: DefaultMaxConnections,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	return cfg
}

// Instance is the live postgres handed to a Run closure. DB covers the common
// case; the remaining fields are there for the tests that need a second
// connection, a different role, or the container API itself.
type Instance struct {
	// DB is an open, pinged pool against Database as Username.
	DB *sql.DB

	// Container is the underlying testcontainer, for the rare test that needs
	// Exec or a snapshot. Its lifecycle is not yours to manage. It is nil when
	// WithDSNFromEnv resolved the server, since there is no container then;
	// Host and Port are populated either way.
	Container *postgrescontainer.PostgresContainer

	// ConnectionString is the DSN DB was opened with.
	ConnectionString string

	// Host and Port locate the server DB is connected to, whether that is the
	// container or the server named by WithDSNFromEnv.
	Host string
	Port string

	// Database, Username and Password are the credentials the server was
	// reached with, exposed so tests can reconnect or grant against them.
	Database string
	Username string
	Password string
}

// ConnectionStringFor builds a DSN for this server under different credentials,
// for suites that connect as a role they created rather than as the
// provisioning superuser.
func (i *Instance) ConnectionStringFor(tb testing.TB, database, username, password string) string {
	tb.Helper()

	if i.Host == "" {
		tb.Fatal("pgtest: Instance has no host; it was not produced by Run")
	}

	return fmt.Sprintf("postgres://%s@%s/%s",
		url.UserPassword(username, password).String(),
		net.JoinHostPort(i.Host, i.Port),
		database,
	)
}

// Open opens and pings an additional pool against this container and closes it
// when the test ends. Use it alongside ConnectionStringFor to connect as another
// role; for the provisioning role, DB is already open.
func (i *Instance) Open(tb testing.TB, connectionString string) *sql.DB {
	tb.Helper()

	return openPoolForTest(tb, tb.Context(), connectionString, 0, 0)
}

// Run resolves a postgres, opens a pool against it, and hands both to fn as an
// Instance. It is Start with the postgres-shaped setup — image, credentials,
// readiness wait, sql.Open, ping — already applied and the lifecycle owned, so
// the closure starts from a database it can query and ends without tidying up.
//
// The resolution ladder, in order:
//
//  1. the DSN in the environment variable named by WithDSNFromEnv, if that
//     option was given and the variable is set. No container is started.
//  2. -short, which skips.
//  3. a container. Whether an unavailable one skips or fails is the suite's
//     call — see WithRequiredPostgres — and by default it skips, along with the
//     RUN_CONTAINER_TESTS gate.
//
// Startup failures fail the test, and teardown of both the pool and the
// container is registered with tb.Cleanup — so fn is free to spawn parallel
// subtests against the Instance and return before they run.
//
// One Run per test binary is the shape this is built for. Give each test its
// own schema with Instance.Schema, or its own database with Instance.Template
// and Template.Clone, rather than a container each. A binary whose per-binary
// setup lives in TestMain wants Start instead; Run is that function with a
// testing.TB's skip and cleanup applied on top.
func Run(tb testing.TB, fn func(ctx context.Context, pg *Instance), opts ...Option) {
	tb.Helper()

	if fn == nil {
		tb.Fatal("pgtest: Run requires a non-nil fn")
	}

	cfg := newOptions(opts)
	cfg.gate(tb)

	ctx := tb.Context()

	pg, teardown, err := cfg.start(ctx)
	must.NoError(tb, err)

	// Registered rather than deferred until fn returns: a closure that spawns
	// parallel subtests returns before they run, and a deferred teardown would
	// take the pool and the container away from underneath them.
	tb.Cleanup(func() { logTeardown(tb, teardown) })

	fn(ctx, pg)
}

// Start resolves a postgres and opens a pool against it for a caller with no
// testing.TB — a TestMain, most often. It is Run's body with the two
// testing.TB-shaped decisions handed back instead of taken: teardown is
// returned rather than registered, and an unavailable postgres is ErrNoPostgres
// rather than a skip.
//
// The resolution ladder is Run's, minus the rung that needs a test:
//
//  1. the DSN in the environment variable named by WithDSNFromEnv, if that
//     option was given and the variable is set. No container is started.
//  2. a container, unless the RUN_CONTAINER_TESTS gate is closed and
//     WithRequiredPostgres was not given, which is ErrNoPostgres.
//
// -short is not consulted here, because Start cannot know whether its caller has
// parsed flags yet: testing.Short() before flag.Parse panics rather than
// reporting false, and a library entry point is the wrong place to find that
// out. A TestMain that wants -short honored parses first and gates itself, which
// costs one line and saves starting a container the run will not use:
//
//	func run(m *testing.M) int {
//		flag.Parse()
//		if testing.Short() {
//			return m.Run() // nothing started; the tests skip themselves
//		}
//		...
//	}
//
// Individual tests can skip through containers.SkipIfNotRunning instead, which
// reads -short at a point in the binary's life where it has been parsed.
//
// The returned teardown closes the pool and terminates the container, in that
// order, and running it is the caller's job. Running it *before* os.Exit is the
// part that is easy to get wrong, since os.Exit does not run deferred
// functions — see the package documentation for the shape that gets it right.
func Start(ctx context.Context, opts ...Option) (*Instance, func() error, error) {
	return newOptions(opts).start(ctx)
}

// gate applies the ladder rungs that need a testing.TB, which is every rung
// that ends in a skip. Run calls it before start, so start never has to decide
// what a skip would mean.
func (o *options) gate(tb testing.TB) {
	tb.Helper()

	switch {
	case o.dsnFromEnv() != "":
		// A server somebody else is running: nothing to gate on but -short,
		// because a caller asking for a fast answer does not want a database
		// round-trip either.
		if testing.Short() {
			tb.SkipNow()
		}
	case o.required:
		containers.RequireRunning(tb)
	default:
		containers.SkipIfNotRunning(tb)
	}
}

// start is the testing.TB-free core of Run: the resolution ladder, with
// teardown returned rather than registered.
func (o *options) start(ctx context.Context) (*Instance, func() error, error) {
	if dsn := o.dsnFromEnv(); dsn != "" {
		return o.startAgainstDSN(ctx, dsn)
	}

	if !o.required && !containers.RunningTests {
		return nil, nil, ErrNoPostgres
	}

	return o.startContainer(ctx)
}

// dsnFromEnv reads the first rung of the resolution ladder, or "" when the
// caller named no variable or the one they named is unset.
func (o *options) dsnFromEnv() string {
	if o.dsnEnvVar == "" {
		return ""
	}

	return strings.TrimSpace(os.Getenv(o.dsnEnvVar))
}

// startAgainstDSN is the WithDSNFromEnv path: a server somebody else is
// running, so there is nothing to start and nothing to terminate, and teardown
// is the pool and only the pool.
func (o *options) startAgainstDSN(ctx context.Context, dsn string) (*Instance, func() error, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return nil, nil, platformerrors.Wrapf(err, "pgtest: parsing DSN from %s", o.dsnEnvVar)
	}

	db, err := openPool(ctx, dsn, o.maxOpenConns, 0)
	if err != nil {
		return nil, nil, err
	}

	password, _ := parsed.User.Password()

	return &Instance{
			DB:               db,
			ConnectionString: dsn,
			Host:             parsed.Hostname(),
			Port:             parsed.Port(),
			Database:         strings.TrimPrefix(parsed.Path, "/"),
			Username:         parsed.User.Username(),
			Password:         password,
		},
		func() error { return closePool(db) },
		nil
}

// startContainer starts a container with the shared backoff policy and opens a
// pool against it. Everything after the container comes up terminates it on the
// way out, so a failure between a live container and a live pool does not leak
// the half that did come up.
func (o *options) startContainer(ctx context.Context) (*Instance, func() error, error) {
	container, err := containers.StartWithRetry(ctx, func(ctx context.Context) (*postgrescontainer.PostgresContainer, error) {
		return postgrescontainer.Run(ctx, o.image, o.containerOptions()...)
	})
	if err != nil {
		return nil, nil, platformerrors.Wrap(err, "pgtest: starting postgres container")
	}

	terminate := func() error { return terminateContainer(ctx, container) }

	connectionString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, nil, platformerrors.Join(platformerrors.Wrap(err, "pgtest: reading connection string"), terminate())
	}

	host, err := container.Host(ctx)
	if err != nil {
		return nil, nil, platformerrors.Join(platformerrors.Wrap(err, "pgtest: reading container host"), terminate())
	}

	port, err := container.MappedPort(ctx, mappedPort)
	if err != nil {
		return nil, nil, platformerrors.Join(platformerrors.Wrap(err, "pgtest: reading mapped port"), terminate())
	}

	db, err := openPool(ctx, connectionString, o.maxOpenConns, 0)
	if err != nil {
		return nil, nil, platformerrors.Join(err, terminate())
	}

	return &Instance{
			DB:               db,
			Container:        container,
			ConnectionString: connectionString,
			Host:             host,
			Port:             port.Port(),
			Database:         o.database,
			Username:         o.username,
			Password:         o.password,
		},
		// The pool first: Terminate takes the server away, and a pool drained
		// after that reports a failure that is only the teardown's own doing.
		func() error { return platformerrors.Join(closePool(db), terminate()) },
		nil
}

// terminateContainer reaps a container on a context of its own, so a caller
// whose context is already done — a test past its deadline, a TestMain past
// m.Run — still gets the container back.
func terminateContainer(ctx context.Context, container *postgrescontainer.PostgresContainer) error {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), containers.DefaultShutdownTimeout)
	defer cancel()

	return platformerrors.Wrap(container.Terminate(shutdownCtx), "pgtest: terminating container")
}

// openPool opens, sizes and pings a pool. Non-positive sizes leave
// database/sql's own defaults in place. The caller closes what it gets back; a
// pool that never became ready is closed here instead, since the caller is
// handed nothing to close it with.
func openPool(ctx context.Context, connectionString string, maxOpen, maxIdle int) (*sql.DB, error) {
	db, err := sql.Open(DriverName, connectionString)
	if err != nil {
		return nil, platformerrors.Wrap(err, "pgtest: opening pool")
	}
	if db == nil {
		return nil, platformerrors.New("pgtest: driver returned a nil pool")
	}

	if maxOpen > 0 {
		db.SetMaxOpenConns(maxOpen)
	}
	if maxIdle > 0 {
		db.SetMaxIdleConns(maxIdle)
	}

	if err = containers.PingWithRetry(ctx, db.PingContext); err != nil {
		return nil, platformerrors.Join(platformerrors.Wrap(err, "pgtest: waiting for postgres"), closePool(db))
	}

	return db, nil
}

// openPoolForTest is openPool with the two testing.TB conveniences reapplied:
// a failure to open fails the test, and the pool is drained when tb ends.
func openPoolForTest(tb testing.TB, ctx context.Context, connectionString string, maxOpen, maxIdle int) *sql.DB {
	tb.Helper()

	db, err := openPool(ctx, connectionString, maxOpen, maxIdle)
	must.NoError(tb, err)

	tb.Cleanup(func() { logTeardown(tb, func() error { return closePool(db) }) })

	return db
}

// closePool drains a pool.
func closePool(db *sql.DB) error {
	return platformerrors.Wrap(db.Close(), "pgtest: closing pool")
}

// logTeardown runs a teardown at the end of a test, logging rather than failing
// if it does not go cleanly: by then the test's own assertions have already had
// their say, and a leftover object on a container about to be reaped is not
// worth turning a passing test red.
func logTeardown(tb testing.TB, teardown func() error) {
	tb.Helper()

	if err := teardown(); err != nil {
		tb.Logf("%v", err)
	}
}

// containerOptions renders the resolved options as testcontainers customizers.
// User-supplied customizers come last so they can override the defaults.
func (o *options) containerOptions() []testcontainers.ContainerCustomizer {
	defaults := []testcontainers.ContainerCustomizer{
		postgrescontainer.WithDatabase(o.database),
		postgrescontainer.WithUsername(o.username),
		postgrescontainer.WithPassword(o.password),
		testcontainers.WithWaitStrategyAndDeadline(
			startupDeadline,
			wait.ForLog(readyLog).WithOccurrence(readyLogOccurence),
		),
	}

	// Appended to the module's own `postgres -c fsync=off` rather than replacing
	// it, so the image keeps whatever else it wants on the command line.
	if o.maxConnections > 0 {
		defaults = append(defaults, testcontainers.WithCmdArgs("-c", fmt.Sprintf("max_connections=%d", o.maxConnections)))
	}

	return append(defaults, o.customizers...)
}
