package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/random"

	"github.com/shoenig/test/must"
)

const (
	// DefaultIsolatedMaxOpenConns and DefaultIsolatedMaxIdleConns size the pool
	// Schema and Clone hand back. They are deliberately tiny: the connection
	// ceiling belongs to the whole run, not to one test, and a pool sized for a
	// service that owns its database is the wrong shape for a few dozen suites
	// sharing one server. What over-sizing looks like downstream is "too many
	// clients already" from whichever test connects last.
	DefaultIsolatedMaxOpenConns = 4
	DefaultIsolatedMaxIdleConns = 2

	// maxIdentifierLength is postgres' NAMEDATALEN-1. Identifiers past it are
	// truncated silently, so two long names can arrive at the same schema or
	// database without anything saying so.
	maxIdentifierLength = 63

	// testNameBudget is how much of a test's name survives into an identifier.
	// The rest of the budget goes to the prefix and the random suffix, which is
	// what actually keeps two tests apart.
	testNameBudget = 40

	// randomSuffixBytes is hex-encoded, so it costs twice this many characters.
	randomSuffixBytes = 6

	schemaPrefix   = "pgtest"
	templatePrefix = "tmpl"
	clonePrefix    = "clone"
)

// MigrateFunc applies a schema to a freshly created schema or database. It is
// exactly the shape of database.Migrator's Migrate method, so a *migrate.Migrator
// satisfies it as a method value:
//
//	m, err := migrate.New(dialect.Postgres, migrations, migrate.WithSchemaScopedLockKey())
//	must.NoError(t, err)
//	schema := pg.Schema(t, pgtest.WithMigration(m.Migrate))
//
// It is a parameter rather than an import because database/migrate's own tests
// use this package, and importing it back would close the cycle.
type MigrateFunc func(ctx context.Context, db *sql.DB) error

// IsolationOption configures Instance.Schema, Instance.Template and
// Template.Clone.
type IsolationOption func(*isolationOptions)

type isolationOptions struct {
	migrate      MigrateFunc
	label        string
	maxOpenConns int
	maxIdleConns int
}

// WithMigration supplies the migration to run against the new schema or
// database. Absent, the schema or clone is handed back empty, which is what a
// test that creates its own tables wants.
//
// For a schema, build the Migrator with migrate.WithSchemaScopedLockKey() or
// parallel setup serializes on one advisory lock. For a template it does not
// matter: the migration runs once, before any clone exists.
func WithMigration(fn MigrateFunc) IsolationOption {
	return func(o *isolationOptions) { o.migrate = fn }
}

// WithPoolSize overrides DefaultIsolatedMaxOpenConns and
// DefaultIsolatedMaxIdleConns for this schema or clone. Non-positive values
// leave database/sql's own unlimited defaults in place, which for a suite
// sharing one server is rarely what you want.
func WithPoolSize(maxOpen, maxIdle int) IsolationOption {
	return func(o *isolationOptions) {
		o.maxOpenConns, o.maxIdleConns = maxOpen, maxIdle
	}
}

// WithLabel names the schema or database for the human reading a failure. It is
// sanitized and trimmed like a test's name, and what actually keeps two of them
// apart is the random suffix either way.
//
// Schema, Template and Clone default to the name of the test that asked, and
// need this only when that name is not the useful one. NewTemplate has no test
// to take a name from, so without it a per-binary template is tmpl_<random> —
// unique, but anonymous in a `\l` listing or a stuck-session query.
func WithLabel(label string) IsolationOption {
	return func(o *isolationOptions) { o.label = label }
}

func newIsolationOptions(opts []IsolationOption) *isolationOptions {
	cfg := &isolationOptions{
		maxOpenConns: DefaultIsolatedMaxOpenConns,
		maxIdleConns: DefaultIsolatedMaxIdleConns,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// labelOr is WithLabel's resolution: what the caller named, or what the caller
// of a testing.TB-taking entry point gets for free.
func (o *isolationOptions) labelOr(fallback string) string {
	if o.label != "" {
		return o.label
	}

	return fallback
}

// Isolated is one test's private corner of a shared server: a schema from
// Instance.Schema, or a database from Template.Clone. Either way DB is a live,
// migrated pool and the underlying object is dropped when the test ends.
type Isolated struct {
	// DB is an open, pinged, deliberately small pool. For a schema its
	// search_path names Name, so unqualified DDL and DML land inside it.
	DB *sql.DB

	// Name is the schema or database name, unique within the run. Tests that
	// need to reconnect, or to assert on catalog rows, need it.
	Name string

	// ConnectionString is the DSN DB was opened with.
	ConnectionString string
}

// Schema creates a private schema on this instance, opens a pool whose
// search_path points at it, runs WithMigration if one was given, and drops the
// schema when tb ends.
//
// Everything unqualified — the tables the migrations create, the rows the test
// writes, and goose's own version table — lands inside the schema, so two tests
// running in parallel against one server never see each other's data. Give each
// test its own Schema; sharing one puts them back in the same database they were
// trying to get out of.
//
// The migration must not serialize with its peers. See WithMigration.
func (i *Instance) Schema(tb testing.TB, opts ...IsolationOption) *Isolated {
	tb.Helper()

	cfg := newIsolationOptions(opts)
	ctx := tb.Context()

	name, err := isolationName(ctx, schemaPrefix, cfg.labelOr(tb.Name()))
	must.NoError(tb, err)

	must.NoError(tb, i.exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", quoteIdentifier(name))))

	// Registered before the pool is opened so that Cleanup's LIFO order closes
	// the pool first: DROP SCHEMA waits behind anything still holding a lock in
	// it, and a test that failed mid-transaction is exactly that.
	tb.Cleanup(func() {
		logTeardown(tb, func() error { return i.dropSchema(context.WithoutCancel(ctx), name) })
	})

	connectionString, err := i.searchPathDSN(name)
	must.NoError(tb, err)

	db := openPoolForTest(tb, ctx, connectionString, cfg.maxOpenConns, cfg.maxIdleConns)

	if cfg.migrate != nil {
		must.NoError(tb, cfg.migrate(ctx, db))
	}

	return &Isolated{DB: db, Name: name, ConnectionString: connectionString}
}

// Template is a migrated database that Clone copies per test. Build one per
// binary, from Instance.Template.
type Template struct {
	instance *Instance

	// Name is the template database's name.
	Name string
}

// Template creates a database, runs WithMigration against it once, and returns
// a handle that Clone copies per test. The database is dropped when tb ends.
//
// The migration pool is closed before Template returns, and that is load-bearing
// rather than tidy: CREATE DATABASE ... TEMPLATE refuses to run while any session
// is attached to the template, so a pool left open would fail the first clone
// instead of this call.
func (i *Instance) Template(tb testing.TB, opts ...IsolationOption) *Template {
	tb.Helper()

	template, teardown, err := i.newTemplate(tb.Context(), newIsolationOptions(opts), tb.Name())
	must.NoError(tb, err)

	tb.Cleanup(func() { logTeardown(tb, teardown) })

	return template
}

// NewTemplate is Instance.Template for a caller with no testing.TB — a
// TestMain, most often, which is the shape a per-binary template already
// belongs to. It is Template's body with the testing.TB-shaped decisions handed
// back instead of taken: the drop is returned as a teardown rather than
// registered, and a failure anywhere in there is an error rather than a fatal.
//
// See Start, which is where a caller in that position gets its Instance, and
// which documents when the returned teardown has to run.
//
// Name it with WithLabel if the databases want to be identifiable; without a
// test to borrow a name from the template is tmpl_<random>.
func (i *Instance) NewTemplate(ctx context.Context, opts ...IsolationOption) (*Template, func() error, error) {
	return i.newTemplate(ctx, newIsolationOptions(opts), "")
}

// newTemplate is the core both spellings share. fallbackLabel is what a caller
// with a testing.TB has and NewTemplate's caller does not.
func (i *Instance) newTemplate(ctx context.Context, cfg *isolationOptions, fallbackLabel string) (*Template, func() error, error) {
	name, err := isolationName(ctx, templatePrefix, cfg.labelOr(fallbackLabel))
	if err != nil {
		return nil, nil, err
	}

	// CREATE DATABASE cannot run inside a transaction, so this is deliberately a
	// bare Exec on the instance pool rather than anything guarded.
	if err = i.exec(ctx, fmt.Sprintf("CREATE DATABASE %s", quoteIdentifier(name))); err != nil {
		return nil, nil, err
	}

	teardown := func() error { return i.dropDatabase(context.WithoutCancel(ctx), name) }

	if cfg.migrate != nil {
		if err = i.migrateTemplate(ctx, cfg.migrate, name); err != nil {
			// Dropped here rather than left to the caller: it was never handed a
			// teardown, because it was never handed a template.
			return nil, nil, platformerrors.Join(err, teardown())
		}
	}

	return &Template{instance: i, Name: name}, teardown, nil
}

// migrateTemplate runs the migration against a pool of its own and closes that
// pool before returning, which is load-bearing rather than tidy: CREATE
// DATABASE ... TEMPLATE refuses to run while any session is attached to the
// template, so a pool left open would fail the first clone instead of this
// call. The close comes before the migration's own error is reported, so a
// failed migration still detaches.
func (i *Instance) migrateTemplate(ctx context.Context, migrate MigrateFunc, name string) error {
	dsn, err := i.databaseDSN(name)
	if err != nil {
		return err
	}

	db, err := openPool(ctx, dsn, 0, 0)
	if err != nil {
		return err
	}

	migrateErr := migrate(ctx, db)

	return platformerrors.Join(migrateErr, closePool(db))
}

// Clone copies the template into a fresh database and hands back a pool over it,
// dropped when tb ends. The copy is a file copy rather than a replay of every
// migration, which is what makes per-test isolation affordable.
//
// WithMigration is honored here too, for the occasional test that needs a
// migration the template does not carry; most callers migrate the template and
// pass nothing.
func (t *Template) Clone(tb testing.TB, opts ...IsolationOption) *Isolated {
	tb.Helper()

	cfg := newIsolationOptions(opts)
	ctx := tb.Context()

	name, err := isolationName(ctx, clonePrefix, cfg.labelOr(tb.Name()))
	must.NoError(tb, err)

	must.NoError(tb, t.instance.exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s",
		quoteIdentifier(name), quoteIdentifier(t.Name))))

	tb.Cleanup(func() {
		logTeardown(tb, func() error { return t.instance.dropDatabase(context.WithoutCancel(ctx), name) })
	})

	connectionString, err := t.instance.databaseDSN(name)
	must.NoError(tb, err)

	db := openPoolForTest(tb, ctx, connectionString, cfg.maxOpenConns, cfg.maxIdleConns)

	if cfg.migrate != nil {
		must.NoError(tb, cfg.migrate(ctx, db))
	}

	return &Isolated{DB: db, Name: name, ConnectionString: connectionString}
}

// exec runs a statement on the instance pool.
func (i *Instance) exec(ctx context.Context, query string) error {
	_, err := i.DB.ExecContext(ctx, query)

	return platformerrors.Wrapf(err, "pgtest: running %s", query)
}

// dropSchema and dropDatabase are the teardown half of Schema, Template and
// Clone. A caller with a testing.TB runs them through logTeardown, which logs
// rather than fails: by cleanup time the test's own assertions have had their
// say, and a leftover object on a container about to be reaped is not worth
// turning a passing test red.
func (i *Instance) dropSchema(ctx context.Context, name string) error {
	_, err := i.DB.ExecContext(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(name)))

	return platformerrors.Wrapf(err, "pgtest: dropping schema %s", name)
}

// dropDatabase drops a database out from under whatever is still connected to
// it. WITH (FORCE) terminates those sessions rather than erroring, which matters
// because a pool that logged its close failure is still holding a socket. It
// wants postgres 13 or newer, as every image this package defaults to is.
func (i *Instance) dropDatabase(ctx context.Context, name string) error {
	_, err := i.DB.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", quoteIdentifier(name)))

	return platformerrors.Wrapf(err, "pgtest: dropping database %s", name)
}

// searchPathDSN renders this instance's DSN with search_path pointed at schema.
// pgx forwards unrecognized parameters as startup runtime parameters, so the
// setting arrives with the connection rather than as a statement some later
// connection in the pool might miss.
func (i *Instance) searchPathDSN(schema string) (string, error) {
	return i.rewriteDSN(func(u *url.URL) {
		query := u.Query()
		query.Set("search_path", schema)
		u.RawQuery = query.Encode()
	})
}

// databaseDSN renders this instance's DSN pointed at a different database.
func (i *Instance) databaseDSN(database string) (string, error) {
	return i.rewriteDSN(func(u *url.URL) { u.Path = "/" + database })
}

func (i *Instance) rewriteDSN(rewrite func(*url.URL)) (string, error) {
	parsed, err := url.Parse(i.ConnectionString)
	if err != nil {
		return "", platformerrors.Wrap(err, "pgtest: parsing the instance's connection string")
	}

	rewrite(parsed)

	return parsed.String(), nil
}

// isolationName builds an identifier that is unique within a run and stays
// inside postgres' 63-byte limit. The label — a test's name, most of the time —
// is in it for the human reading a failure; the random suffix is what makes it
// unique, since two long test names truncate to the same prefix. A label that
// sanitizes away to nothing, or that was never given, leaves prefix_<random>,
// which is anonymous but no less unique.
func isolationName(ctx context.Context, prefix, label string) (string, error) {
	suffix, err := random.GenerateHexEncodedString(context.WithoutCancel(ctx), randomSuffixBytes)
	if err != nil {
		return "", platformerrors.Wrap(err, "pgtest: generating an isolation name")
	}

	name := fmt.Sprintf("%s_%s", prefix, suffix)
	if sanitized := sanitizeIdentifier(label, testNameBudget); sanitized != "" {
		name = fmt.Sprintf("%s_%s_%s", prefix, sanitized, suffix)
	}

	if len(name) > maxIdentifierLength {
		// Unreachable with the budgets above, but truncating the *prefix* end
		// rather than letting postgres truncate the suffix end keeps the random
		// part, which is the part that has to survive.
		name = name[len(name)-maxIdentifierLength:]
	}

	return name, nil
}

// sanitizeIdentifier reduces a test name to lowercase ASCII letters, digits and
// underscores, then trims it to budget. Subtest names carry slashes, spaces and
// whatever else a test author wrote in a t.Run label.
func sanitizeIdentifier(name string, budget int) string {
	var out strings.Builder

	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			out.WriteRune(r)
		default:
			out.WriteByte('_')
		}

		if out.Len() >= budget {
			break
		}
	}

	return strings.Trim(out.String(), "_")
}

// quoteIdentifier renders a name as a quoted postgres identifier. Everything
// this package generates is already safe, but the identifiers are interpolated
// into DDL that no placeholder can carry, so they are quoted rather than
// trusted.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
