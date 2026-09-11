package issuereports

import (
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/issuereports/internal/issuereportsdb"
	"github.com/primandproper/platform-go/v14/issuereports/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlguard"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// DefaultTablePrefix is the namespace the issue reports table carries when none
// is configured, which is none — rendering issue_reports.
//
// The issue_reports name is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_issue_reports, for a database shared between applications. A namespace
// must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans, logger, and instruments.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed [Store], against the schema issuereports/migrations
// renders.
//
// It is exported, and returned by [NewSQLStore], so a caller who has chosen SQL
// storage can depend on that choice rather than on the seam every backing
// shares.
type SQLStore struct {
	q     issuereportsdb.Querier
	o11y  observability.Observer
	clock clock.Clock

	// guardMissCounter counts transitions whose guard matched no row, which is
	// the one number nothing above this layer can see.
	//
	// A guard miss is not a database error and is not a missing row: it is two
	// people deciding the same report at once. A trickle of them is a queue with
	// more than one triager in it, which is the system working; a step change is
	// a client retrying a transition it already made, or two workers racing over
	// the same queue — and from above this layer both look like an occasional
	// error somebody dismisses.
	guardMissCounter metrics.Int64Counter

	// guard is what a guarded write means in this package when it matches no
	// row. See internal/sqlguard.
	guard sqlguard.Guard

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	prefix          string
}

// NewSQLStore builds an issue report store over the given database.
//
// The dialect comes from the client, so the two cannot disagree. The prefix must
// still match the one the migrations were rendered with — nothing here can check
// that, and a mismatch surfaces as a missing table on the first query rather
// than at construction.
//
// The client is taken for its dialect and for nothing else, and the store keeps
// no reference to it. Every write is handed a database.Tx and every read an
// executor, so there is no statement this store runs on a connection of its own:
// no Writer() for the writes and no Reader() for the reads. It does not take
// CurrentTime() either — closed_at is stamped from the injected clock, so a test
// can put a filing and its resolution a known distance apart, and every other
// timestamp in this table is the database server's. A consumer with nothing to
// join opens a transaction with Client.WithTransaction and passes the Tx it is
// handed.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger, traces to a noop provider, and counts into a noop meter.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "issuereports dialect %q", d)
	}

	s := &SQLStore{
		clock:  clock.NewClock(),
		prefix: DefaultTablePrefix,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.prefix); err != nil {
		return nil, err
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see issuereports/internal/issuereportsdb.
	qd, err := issuereportsdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := issuereportsdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the issuereports querier")
	}

	s.q = q
	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.guardMissCounter, err = mp.NewInt64Counter(storeName + "_guard_misses"); err != nil {
		return nil, platformerrors.Wrap(err, "creating the issuereports store guard miss counter")
	}

	s.guard = sqlguard.Guard{
		MissCounter: s.guardMissCounter,
		NotFound:    ErrStatusConflict,
		Namespace:   serviceName,
		IDKey:       reportIDKey,
		Message:     "issue report moved before the transition could be recorded",
		Reason:      "issue report %q is no longer in the expected status",
	}

	return s, nil
}

// TablePrefix returns the namespace this store's table carries, for a caller
// rendering the migrations it needs.
func (s *SQLStore) TablePrefix() string { return s.prefix }

// now is the one clock read this package makes, in UTC.
//
// The location is load-bearing on SQLite rather than cosmetic: modernc's driver
// stores a bound time.Time as Go's own String() rendering, so a comparison
// against that column is a string comparison, and it is chronological only
// because every value written is UTC in a fixed-width prefix position.
func (s *SQLStore) now() time.Time { return s.clock.Now().UTC() }

// notFound maps a driver's empty-result error onto this package's sentinel,
// leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to a user
// as "you have not reported anything".
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}

// guardCount turns an unguarded write's affected-row count into the answer its
// caller acts on: nothing touched means the row the statement addressed was not
// there to touch.
//
// Every write in this store is keyed on the scope, so without this a write aimed
// at another tenant's row would report success having done nothing.
//
// It is not the status guard — that one goes through internal/sqlguard, because
// a miss there means something different and is worth counting.
func guardCount(count int64, err, missing error, operation string) error {
	if err != nil {
		return platformerrors.Wrap(err, operation)
	}

	if count == 0 {
		return missing
	}

	return nil
}

// pageFilter is the filter a paged read is answered under: the caller's, with
// the page-size ceiling every other paged read in this module applies.
//
// It works on a copy. The clamp has to be applied to what the query binds and to
// what the result reports, and doing that by writing through the caller's
// pointer would hand them back a filter they did not pass — a store that
// rewrites its argument is a store whose caller cannot reuse one.
//
// A page size that is present and zero is left alone and returns no rows, which
// is the loud reading of an explicit zero. Only absence is defaulted.
func pageFilter(filter *filtering.QueryFilter) *filtering.QueryFilter {
	if filter == nil {
		return filtering.DefaultQueryFilter()
	}

	bounded := *filter

	size := uint16(filtering.DefaultQueryFilterLimit)
	if bounded.MaxResponseSize != nil {
		size = filtering.ClampResponseSize(uint64(*bounded.MaxResponseSize))
	}

	bounded.MaxResponseSize = &size

	return &bounded
}

// issuereportsdbDialect maps this module's dialect names onto the generated
// package's.
//
// The set is closed on both sides — NewSQLStore has already rejected anything
// d.Valid() declines — so the default arm is reachable only when this module
// learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect rather than
// panicking or leaning on issuereportsdb.New refusing the empty string.
func issuereportsdbDialect(d dialect.Dialect) (issuereportsdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return issuereportsdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return issuereportsdb.DialectMySQL, nil
	case dialect.SQLite:
		return issuereportsdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated issuereports queries for dialect %q", d)
	}
}
