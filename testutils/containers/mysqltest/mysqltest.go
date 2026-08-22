// Package mysqltest provides the MySQL testcontainer setup that every
// MySQL-backed suite in this repo would otherwise hand-roll: start the
// container with the shared retry policy and wait strategy, open a
// go-sql-driver pool against it, ping it, and tear all of it down afterwards.
// It is pgtest's MySQL counterpart, and exists because three suites (outbox,
// authorization/database, database/mysql/tableaccess) had each grown their own
// copy of exactly this.
//
// Callers describe the shape they want with Options and receive a live Instance
// inside a closure, so a test body says what it does with MySQL and nothing
// about how MySQL is stood up or torn down.
package mysqltest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/testutils/containers"

	// The go-sql-driver is registered here so that callers get a working
	// "mysql" driver from importing mysqltest alone.
	_ "github.com/go-sql-driver/mysql"
	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// DefaultImage is the image Run launches when no override is given. Use
	// WithImage for derivatives the rest of this setup still applies to, e.g.
	// "mariadb:11".
	DefaultImage = "mysql:8.0"

	// DriverName is the database/sql driver Instance.DB and Instance.Open use.
	DriverName = "mysql"

	defaultCredential = "platformtest"

	// A cold start on a busy CI host has to cover an image pull plus the init
	// run.
	startupDeadline = 2 * time.Minute

	// readyLog identifies the real server by the port it announces rather than
	// by counting readiness lines, because the count is not what it looks like:
	// the bootstrap server that runs the init scripts and then shuts down logs
	// "ready for connections" twice on its own — once from the X plugin, once
	// from mysqld on port 0 — so waiting for the second occurrence releases the
	// test against a server that is about to be shut down and restarted.
	//
	// The trailing space is load-bearing. Without it this also matches the X
	// plugin's "port: 33060", which the real server logs a line before it is
	// listening for clients. Matching the port keeps this image-agnostic:
	// MariaDB logs the same field, where the upstream module's
	// "port: 3306  MySQL Community Server" would never match.
	readyLog = "port: 3306 "
)

// defaultParams are the DSN parameters Instance.ConnectionString carries when
// no override is given. parseTime keeps DATETIME(6) round-tripping as
// time.Time rather than []byte; multiStatements lets a test feed rendered DDL
// in one Exec.
var defaultParams = []string{"parseTime=true", "multiStatements=true"}

// Option configures Run.
type Option func(*options)

type options struct {
	image        string
	database     string
	username     string
	password     string
	params       []string
	customizers  []testcontainers.ContainerCustomizer
	maxOpenConns int
}

// WithImage overrides DefaultImage. Use it for MySQL derivatives that the rest
// of this setup still applies to, e.g. "mariadb:11".
func WithImage(image string) Option {
	return func(o *options) { o.image = image }
}

// WithCredentials overrides the database name, user and password the container
// is provisioned with. Tests that create or drop users want distinct
// credentials so they cannot collide with the identifiers under test.
func WithCredentials(database, username, password string) Option {
	return func(o *options) {
		o.database, o.username, o.password = database, username, password
	}
}

// WithConnectionParams replaces the DSN parameters Instance.ConnectionString is
// built with. The defaults are parseTime=true and multiStatements=true.
func WithConnectionParams(params ...string) Option {
	return func(o *options) { o.params = params }
}

// WithMaxOpenConns caps Instance.DB's pool. Set it well above the number of
// concurrent subtests sharing an Instance, otherwise they starve each other.
// Zero (the default) leaves database/sql's unlimited default in place.
func WithMaxOpenConns(n int) Option {
	return func(o *options) { o.maxOpenConns = n }
}

// WithCustomizers appends testcontainers customizers to the ones Run already
// applies. They run after the defaults, so they can override the wait strategy.
func WithCustomizers(customizers ...testcontainers.ContainerCustomizer) Option {
	return func(o *options) { o.customizers = append(o.customizers, customizers...) }
}

func newOptions(opts []Option) *options {
	cfg := &options{
		image:    DefaultImage,
		database: defaultCredential,
		username: defaultCredential,
		password: defaultCredential,
		params:   defaultParams,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// Instance is the live MySQL handed to a Run closure. DB covers the common
// case; the remaining fields are there for the tests that need a second
// connection, root access, or the container API itself.
type Instance struct {
	// DB is an open, pinged pool against Database as Username.
	DB *sql.DB

	// Container is the underlying testcontainer, for the rare test that needs
	// Host, MappedPort or Exec. Its lifecycle is not yours to manage.
	Container *mysqlcontainer.MySQLContainer

	// ConnectionString is the DSN DB was opened with.
	ConnectionString string

	// Database, Username and Password are the credentials the container was
	// provisioned with, exposed so tests can reconnect or grant against them.
	Database string
	Username string
	Password string
}

// RootConnectionString builds a DSN that connects as root, for suites doing
// admin work — CREATE USER, GRANT — that the provisioned user cannot. The
// container provisions MYSQL_ROOT_PASSWORD with the same value as the user
// password, which is what makes this constructible at all. Parameters are the
// caller's verbatim: root work usually wants a different set (e.g.
// allowCleartextPasswords=true) than data-path connections do.
func (i *Instance) RootConnectionString(tb testing.TB, params ...string) string {
	tb.Helper()

	cs, err := i.Container.ConnectionString(tb.Context(), params...)
	must.NoError(tb, err)

	_, rest, found := strings.Cut(cs, "@")
	must.True(tb, found, must.Sprintf("DSN %q has no credentials section", cs))

	return "root:" + i.Password + "@" + rest
}

// Open opens and pings an additional pool against this container and closes it
// when the test ends. Use it alongside RootConnectionString to connect as root;
// for the provisioned user, DB is already open.
func (i *Instance) Open(tb testing.TB, connectionString string) *sql.DB {
	tb.Helper()

	db, err := sql.Open(DriverName, connectionString)
	must.NoError(tb, err)
	must.NotNil(tb, db)

	tb.Cleanup(func() { closePool(tb, db) })

	containers.PingUntilReady(tb, tb.Context(), db.PingContext)

	return db
}

// Run starts a MySQL container, opens a pool against it, and hands both to fn
// as an Instance. It is containers.Run with the MySQL-shaped setup — image,
// credentials, readiness wait, sql.Open, ping — already applied, so the closure
// starts from a database it can query.
//
// As with containers.Run, the RUN_CONTAINER_TESTS gate is enforced here (the
// test skips without a Docker daemon), startup failures fail the test, and
// teardown of both the pool and the container is registered with tb.Cleanup —
// so fn is free to spawn parallel subtests against the Instance and return
// before they run.
func Run(tb testing.TB, fn func(ctx context.Context, my *Instance), opts ...Option) {
	tb.Helper()

	if fn == nil {
		tb.Fatal("mysqltest: Run requires a non-nil fn")
	}

	cfg := newOptions(opts)

	containers.Run(tb,
		func(ctx context.Context) (*mysqlcontainer.MySQLContainer, error) {
			return mysqlcontainer.Run(ctx, cfg.image, cfg.containerOptions()...)
		},
		func(ctx context.Context, container *mysqlcontainer.MySQLContainer) {
			connectionString, err := container.ConnectionString(ctx, cfg.params...)
			must.NoError(tb, err)

			db, err := sql.Open(DriverName, connectionString)
			must.NoError(tb, err)
			must.NotNil(tb, db)

			tb.Cleanup(func() { closePool(tb, db) })

			if cfg.maxOpenConns > 0 {
				db.SetMaxOpenConns(cfg.maxOpenConns)
			}

			containers.PingUntilReady(tb, ctx, db.PingContext)

			fn(ctx, &Instance{
				DB:               db,
				Container:        container,
				ConnectionString: connectionString,
				Database:         cfg.database,
				Username:         cfg.username,
				Password:         cfg.password,
			})
		},
	)
}

// closePool drains a pool at the end of a test, logging rather than failing if
// it cannot: by then the test's own assertions have already had their say.
func closePool(tb testing.TB, db *sql.DB) {
	tb.Helper()

	if err := db.Close(); err != nil {
		tb.Logf("mysqltest: closing pool: %v", err)
	}
}

// containerOptions renders the resolved options as testcontainers customizers.
// User-supplied customizers come last so they can override the defaults.
func (o *options) containerOptions() []testcontainers.ContainerCustomizer {
	return append([]testcontainers.ContainerCustomizer{
		mysqlcontainer.WithDatabase(o.database),
		mysqlcontainer.WithUsername(o.username),
		mysqlcontainer.WithPassword(o.password),
		testcontainers.WithWaitStrategyAndDeadline(
			startupDeadline,
			wait.ForLog(readyLog),
		),
	}, o.customizers...)
}
