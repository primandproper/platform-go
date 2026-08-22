package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/hashing/fnv"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// serviceName names the Migrator's span, logger, and metrics.
const serviceName = "database_migrator"

var _ database.Migrator = (*Migrator)(nil)

// Lock-wait defaults. Migrate probes every second rather than goose's 5s
// default so a waiting replica notices the winner promptly, and gives up after
// a minute — long enough for a peer's migrations, short enough that a
// genuinely stuck lock fails the deploy instead of hanging it.
const (
	// DefaultLockProbeInterval is how often a waiting process re-checks the
	// advisory lock.
	DefaultLockProbeInterval = time.Second
	// DefaultLockTimeout is how long Migrate waits to acquire the lock.
	DefaultLockTimeout = time.Minute
	// DefaultUnlockProbeInterval is how often a process re-tries releasing the
	// advisory lock.
	DefaultUnlockProbeInterval = time.Second
	// DefaultUnlockTimeout is how long Migrate waits to release the lock.
	DefaultUnlockTimeout = 30 * time.Second
)

// Migrator applies embedded goose SQL migrations. Construct with New; the
// zero value is not usable.
type Migrator struct {
	o11y observability.Observer
	// What the options wrote, kept only until the observer is built from it.
	// Read m.o11y.Logger() for the logger this migrator actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger              logging.Logger
	tracerProvider      tracing.Provider
	metricsProvider     metrics.Provider
	fsys                fs.FS
	runCounter          metrics.Int64Counter
	appliedCounter      metrics.Int64Counter
	errCounter          metrics.Int64Counter
	latencyHist         metrics.Float64Histogram
	lockKey             string
	dialect             dialect.Dialect
	generated           []generatedMigration
	lockProbeInterval   time.Duration
	lockTimeout         time.Duration
	unlockProbeInterval time.Duration
	unlockTimeout       time.Duration
	// Derived from the four durations above by New, so Migrate never has to
	// recompute a conversion that has already been validated.
	lockPeriod      uint64
	lockThreshold   uint64
	unlockPeriod    uint64
	unlockThreshold uint64
	withoutLock     bool
	// Resolved from the connection at Migrate time rather than from lockKey,
	// because the schema a migration lands in is not known at construction.
	schemaScopedLockKey bool
}

// Option configures a Migrator.
type Option func(*Migrator)

// WithLogger attaches a logger. Goose's own progress output is routed through
// it too, so migration logs are structured and attributable instead of going
// to the standard library's global logger.
func WithLogger(logger logging.Logger) Option {
	return func(m *Migrator) {
		m.logger = logger
	}
}

// WithTracerProvider attaches a tracer provider. Migrate is worth tracing: it
// is typically the longest blocking step in service startup, and on Postgres
// it can spend up to a minute waiting on the advisory lock behind a peer that
// is migrating.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(m *Migrator) {
		m.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider attaches a metrics provider, enabling the
// database_migrator_* instruments.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(m *Migrator) {
		m.metricsProvider = metricsProvider
	}
}

// WithGeneratedMigration adds a migration whose SQL comes from code rather than
// from a file in the migrations filesystem. It exists for platform packages that
// own a table and can render its DDL — outbox is the first — so a consumer does
// not have to copy that DDL into their repository and keep it in sync.
//
// The version is the caller's to choose, and that is deliberate: migration
// numbering belongs to whoever owns the sequence, and a platform-chosen number
// would sooner or later collide with a consumer's. Pick one in your sequence,
// then never change it — goose keys applied migrations by version, so renumbering
// an applied migration makes it look unapplied. A version already claimed by a
// file on disk fails New rather than the first Migrate.
//
// The SQL is annotated and validated exactly like a file would be, so it may
// contain several statements separated by semicolons:
//
//	ddl, err := outboxmigrations.SQL(dialect.Postgres, outbox.DefaultTablePrefix)
//	// ...
//	m, err := migrate.New(dialect.Postgres, myMigrations,
//		migrate.WithGeneratedMigration(37, "create_outbox_messages", ddl),
//	)
func WithGeneratedMigration(version uint64, name, body string) Option {
	return func(m *Migrator) {
		m.generated = append(m.generated, generatedMigration{version: version, name: name, body: body})
	}
}

// WithLockKey partitions the Postgres advisory lock ID. Deployments sharing a
// database should share a key (the default empty key is fine); schema-isolated
// parallel tests pass their schema name so they migrate concurrently instead
// of queueing on one global lock.
func WithLockKey(key string) Option {
	return func(m *Migrator) {
		m.lockKey = key
	}
}

// WithSchemaScopedLockKey derives the lock key from the connection's current
// schema instead of a constant, by reading current_schema() when Migrate runs.
//
// It is WithLockKey for the case where the key is not known at construction: a
// schema-per-test harness names its schema in the DSN's search_path, and the
// Migrator is built before — often far before — anyone knows which schema this
// particular test got. Deployments on the default schema all resolve to "public"
// and keep sharing one lock; test schemas resolve to themselves and never
// contend, so parallel setup stays parallel rather than becoming a queue.
//
// It overrides WithLockKey, and it does nothing outside Postgres, where there is
// no advisory lock to partition. A search_path naming only schemas that do not
// exist has no current schema, and Migrate fails rather than silently falling
// back to the global key.
func WithSchemaScopedLockKey() Option {
	return func(m *Migrator) {
		m.schemaScopedLockKey = true
	}
}

// WithoutLock disables the Postgres session advisory lock. Only safe when
// exactly one process can be migrating at a time.
func WithoutLock() Option {
	return func(m *Migrator) {
		m.withoutLock = true
	}
}

// WithLockTimeout sets how long Migrate waits to acquire the Postgres advisory
// lock, re-checking every probeInterval. Raise the timeout for a deployment
// whose migrations legitimately run longer than a minute, so replicas queued
// behind the winner do not give up mid-deploy.
//
// goose measures the probe interval in whole seconds, so probeInterval must be
// a positive whole number of seconds and timeout must be at least one probe
// interval; New rejects anything else. Defaults are DefaultLockProbeInterval
// and DefaultLockTimeout.
func WithLockTimeout(probeInterval, timeout time.Duration) Option {
	return func(m *Migrator) {
		m.lockProbeInterval = probeInterval
		m.lockTimeout = timeout
	}
}

// WithUnlockTimeout sets how long Migrate waits to release the Postgres
// advisory lock, re-trying every probeInterval. It carries the same
// whole-seconds constraint as WithLockTimeout. Defaults are
// DefaultUnlockProbeInterval and DefaultUnlockTimeout.
func WithUnlockTimeout(probeInterval, timeout time.Duration) Option {
	return func(m *Migrator) {
		m.unlockProbeInterval = probeInterval
		m.unlockTimeout = timeout
	}
}

// New builds a Migrator over an fs.FS of SQL migration files, usually an
// embed.FS subtree. Files are named like 00001_description.sql; the leading
// number orders them and must be unique.
//
// The `-- +goose Up` annotation is optional: New inserts it into any file that
// omits one, so a migration can be plain SQL in a numbered file. Files that do
// carry it are left exactly as written, and goose's other annotations
// (StatementBegin/StatementEnd, NO TRANSACTION, ENVSUB) work as documented
// upstream either way. Migrations are read once, here, so a malformed one
// fails construction rather than the first Migrate.
//
// Only Up sections are ever applied — nothing in this package runs a Down — so
// a Down section, if present, is inert.
func New(d dialect.Dialect, migrations fs.FS, opts ...Option) (*Migrator, error) {
	if migrations == nil {
		return nil, errors.New("nil migrations filesystem provided")
	}
	if _, err := gooseDialect(d); err != nil {
		return nil, err
	}

	annotated, err := annotateMigrations(migrations)
	if err != nil {
		return nil, err
	}

	m := &Migrator{
		dialect:             d,
		fsys:                annotated,
		lockProbeInterval:   DefaultLockProbeInterval,
		lockTimeout:         DefaultLockTimeout,
		unlockProbeInterval: DefaultUnlockProbeInterval,
		unlockTimeout:       DefaultUnlockTimeout,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}

	// Merged after the options, since that is when the generated migrations are
	// known, and before New returns, so a version collision fails construction
	// rather than a deploy.
	if m.fsys, err = mergeGenerated(m.fsys, m.generated); err != nil {
		return nil, err
	}

	// Converted here rather than in the options, which cannot report an error,
	// so a bad timeout fails at construction instead of at first Migrate.
	if m.lockPeriod, m.lockThreshold, err = gooseProbe("lock", m.lockProbeInterval, m.lockTimeout); err != nil {
		return nil, err
	}
	if m.unlockPeriod, m.unlockThreshold, err = gooseProbe("unlock", m.unlockProbeInterval, m.unlockTimeout); err != nil {
		return nil, err
	}

	m.o11y = observability.NewObserver(serviceName, m.logger, m.tracerProvider)

	mp := metrics.EnsureMetricsProvider(m.metricsProvider)

	if m.runCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_runs", serviceName)); err != nil {
		return nil, errors.Wrap(err, "creating migration run counter")
	}
	if m.appliedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_applied", serviceName)); err != nil {
		return nil, errors.Wrap(err, "creating migrations applied counter")
	}
	if m.errCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_errors", serviceName)); err != nil {
		return nil, errors.Wrap(err, "creating migration error counter")
	}
	if m.latencyHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName)); err != nil {
		return nil, errors.Wrap(err, "creating migration latency histogram")
	}

	return m, nil
}

// gooseLogger adapts goose's Printf/Fatalf logger onto the platform logger, so
// goose's progress output joins the service's structured logs rather than the
// standard library's global logger. Fatalf deliberately does not exit: goose
// calls it for conditions it also returns as errors, and a library has no
// business terminating its host process.
type gooseLogger struct {
	logger logging.Logger
}

var _ goose.Logger = (*gooseLogger)(nil)

func (g *gooseLogger) Printf(format string, v ...any) {
	g.logger.Info(fmt.Sprintf(format, v...))
}

func (g *gooseLogger) Fatalf(format string, v ...any) {
	g.logger.Error("migration failure reported by goose", errors.New(fmt.Sprintf(format, v...)))
}

// Migrate implements database.Migrator: it applies all pending migrations,
// and is idempotent — an up-to-date database is a no-op. Concurrent callers
// against one Postgres database serialize on the session advisory lock, so
// racing replicas wait for the winner instead of erroring.
func (m *Migrator) Migrate(ctx context.Context, db *sql.DB) error {
	ctx, op := m.o11y.Begin(ctx, observability.WithValue("migrate.dialect", string(m.dialect)))
	defer op.End()

	if db == nil {
		return errors.New("nil database provided")
	}

	m.runCounter.Add(ctx, 1)

	startTime := time.Now()
	defer op.Time(ctx, nil, m.latencyHist)()

	gd, err := gooseDialect(m.dialect)
	if err != nil {
		m.errCounter.Add(ctx, 1)

		return op.Error(err, "resolving migration dialect")
	}

	providerOpts := []goose.ProviderOption{goose.WithLogger(&gooseLogger{logger: op.Logger()})}

	locked := m.dialect == dialect.Postgres && !m.withoutLock
	op.Set("migrate.locked", locked)

	if locked {
		key, keyErr := m.resolveLockKey(ctx, db)
		if keyErr != nil {
			m.errCounter.Add(ctx, 1)

			return op.Error(keyErr, "resolving migration lock key")
		}

		id := lockID(key)
		op.Set(keys.LockKeyKey, key).Set(keys.LockIDKey, id)
		op.Set("migrate.lock_timeout", m.lockTimeout)

		locker, lockErr := lock.NewPostgresSessionLocker(
			lock.WithLockID(id),
			lock.WithLockTimeout(m.lockPeriod, m.lockThreshold),
			lock.WithUnlockTimeout(m.unlockPeriod, m.unlockThreshold),
		)
		if lockErr != nil {
			m.errCounter.Add(ctx, 1)

			return op.Error(lockErr, "building migration session locker")
		}

		providerOpts = append(providerOpts, goose.WithSessionLocker(locker))
	}

	// Instance-based provider: no package-global goose state, so parallel
	// tests (and concurrent Migrators generally) never race on configuration.
	provider, err := goose.NewProvider(gd, db, m.fsys, providerOpts...)
	if err != nil {
		m.errCounter.Add(ctx, 1)

		return op.Error(err, "building migration provider")
	}

	// Logged before Up rather than after, because Up is where a losing replica
	// blocks on the advisory lock for up to the configured lock timeout.
	// Without this line, that wait is indistinguishable from a hang.
	if locked {
		op.Logger().Info("acquiring migration lock and applying migrations")
	}

	results, err := provider.Up(ctx)
	if err != nil {
		m.errCounter.Add(ctx, 1)

		return op.Error(err, "applying migrations")
	}

	applied := make([]string, 0, len(results))
	for _, result := range results {
		applied = append(applied, result.Source.Path)
	}

	m.appliedCounter.Add(ctx, int64(len(results)))
	op.SetValues(map[string]any{"migrate.applied": len(results), "migrate.versions": applied}).
		Logger().
		WithValue("duration_ms", time.Since(startTime).Milliseconds()).
		Info("migrations applied")

	return nil
}

// resolveLockKey answers with the configured lock key, or — under
// WithSchemaScopedLockKey — with the schema this connection's search_path
// actually resolves to.
//
// current_schema() is the first existing schema in the search path, which is
// the one unqualified DDL lands in and therefore the one whose migrations need
// serializing. It is null when the search path names nothing that exists, and
// that is a caller error worth reporting: migrating there would fail anyway,
// and quietly taking the global key would put every test back in one queue.
func (m *Migrator) resolveLockKey(ctx context.Context, db *sql.DB) (string, error) {
	if !m.schemaScopedLockKey {
		return m.lockKey, nil
	}

	var schema sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return "", errors.Wrap(err, "reading current schema")
	}

	if !schema.Valid {
		return "", errors.New("connection has no current schema; check the search_path names a schema that exists")
	}

	return schema.String, nil
}

// gooseProbe converts a probe interval and total timeout into the (period in
// seconds, failure threshold) pair goose's lock options take. goose multiplies
// the two back into the total wait, and rejects a sub-second period, so the
// interval has to divide cleanly into seconds — rounding it silently would
// hand back a different timeout than the caller asked for.
func gooseProbe(what string, probeInterval, timeout time.Duration) (period, threshold uint64, err error) {
	switch {
	case probeInterval < time.Second:
		return 0, 0, errors.Newf("%s probe interval must be at least 1s, got %s", what, probeInterval)
	case probeInterval%time.Second != 0:
		return 0, 0, errors.Newf("%s probe interval must be a whole number of seconds, got %s", what, probeInterval)
	case timeout < probeInterval:
		return 0, 0, errors.Newf("%s timeout (%s) must be at least one probe interval (%s)", what, timeout, probeInterval)
	}

	return uint64(probeInterval / time.Second), uint64(timeout / probeInterval), nil
}

// gooseDialect maps the package's Dialect to goose's, rejecting unknowns.
func gooseDialect(d dialect.Dialect) (goose.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return goose.DialectPostgres, nil
	case dialect.MySQL:
		return goose.DialectMySQL, nil
	case dialect.SQLite:
		return goose.DialectSQLite3, nil
	default:
		return "", errors.Newf("unknown migration dialect %q", d)
	}
}

// lockID derives a stable advisory-lock ID from the lock key using FNV-64a. A
// hash collision between two keys merely over-serializes their migrations —
// never corruption.
func lockID(key string) int64 {
	return int64(fnv.Sum64a([]byte("platform-migrations:" + key)))
}
