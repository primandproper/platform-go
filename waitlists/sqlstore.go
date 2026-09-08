package waitlists

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/waitlists/internal/waitlistsdb"
	"github.com/primandproper/platform-go/v14/waitlists/migrations"

	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/cryptography/hashing"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// DefaultTablePrefix is the namespace the waitlist tables carry when none is
// configured, which is none — rendering waitlists and waitlist_signups.
//
// The waitlist_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders ddb_waitlists,
// for a database shared between applications. A namespace must not end in '_';
// database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema waitlists/migrations
// renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
type SQLStore struct {
	q    waitlistsdb.Querier
	o11y observability.Observer

	clock  clock.Clock
	hasher hashing.Hasher

	signupsCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	prefix          string
}

// NewSQLStore builds a Store over the given database.
//
// The dialect comes from the client, so the two cannot disagree. The prefix must
// still match the one the migrations were rendered with — nothing here can check
// that, and a mismatch surfaces as a missing table on the first query rather
// than at construction.
//
// The client is taken for its dialect and for nothing else, and the store keeps
// no reference to it. Every write is handed a database.Tx and every read an
// executor, so there is no statement this store runs on a connection of its own:
// no Writer() for the writes, no Reader() for the reads, and no CurrentTime(),
// because the two timestamps the database owns are read back through the
// caller's transaction and every other stamp is WithClock's. A consumer with
// nothing to join opens a transaction with Client.WithTransaction and passes the
// Tx it is handed.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger and traces to a noop provider.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "waitlists dialect %q", d)
	}

	s := &SQLStore{
		prefix: DefaultTablePrefix,
		clock:  defaultClock(),
		hasher: defaultHasher(),
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
	// substitution; see waitlists/internal/waitlistsdb.
	qd, err := waitlistsdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := waitlistsdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the waitlists querier")
	}

	s.q = q

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One instrument, and it is the one nothing above this layer can see.
	//
	// A signup either joins, moves through the lifecycle, or is refused — and
	// which of those happened is invisible to a caller that writes a row and
	// carries on. The proportions are the whole health of a launch: a list
	// taking joins and converting nobody is an invitation email that is not
	// arriving, and a rate of withdrawals is the number somebody has to answer
	// for.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.signupsCounter, err = mp.NewInt64Counter(storeName + "_signups"); err != nil {
		return nil, platformerrors.Wrap(err, "creating waitlists store signups counter")
	}

	return s, nil
}

// TablePrefix returns the namespace this store's tables carry, for a caller that
// needs the rendered names — a maintenance TRUNCATE, a schema audit. Pass it to
// migrations.Tables.
func (s *SQLStore) TablePrefix() string { return s.prefix }

// Digest renders what the contact_digest column holds for a contact.
//
// It is exported for the one caller the Store seam cannot serve: a deployment
// migrating off a hand-written table, which has to write the new column from the
// addresses it is holding. It normalizes first, so it is the digest a signup
// made with any capitalization of that address would be found under.
//
// It is not a verification and it is not reversible. What it protects is
// narrower than passwordreset's digest of a random secret — see WithHasher.
func (s *SQLStore) Digest(contact string) string {
	return hashing.HexString(s.hasher, Normalize(contact))
}

// countSignups records n signups reaching a status, including the one a signup
// is written at.
//
// It is called when the statement lands, which is before the caller commits. A
// companion write that fails afterwards takes the row back and not the count,
// and that is the trade: the alternative is a counter fed after a commit this
// store does not perform, which is to say a counter nothing here could feed.
func (s *SQLStore) countSignups(ctx context.Context, status Status, n int64) {
	s.signupsCounter.Add(ctx, n, metric.WithAttributes(attribute.String(statusKey, string(status))))
}

// notFound maps a driver's empty-result error onto this package's sentinel for
// the entity that was missing, leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to
// somebody as "no such waitlist". The sentinel is per-entity because the
// caller's next move differs: a missing list is a broken link, and a missing
// signup is the ordinary case of somebody who has not joined.
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}

// guardCount turns "the statement touched nothing" into the sentinel for the
// row that was not there, or for the guard that was not satisfied.
//
// Every predicate here includes the scope, so without this a write aimed at
// another tenant's row reports success. It is also how a guarded transition
// answers: the count, not the read before it, decides whether this caller is the
// one that moved the signup.
func guardCount(count int64, err, missing error, operation string) error {
	if err != nil {
		return platformerrors.Wrap(err, operation)
	}

	if count == 0 {
		return missing
	}

	return nil
}

// pageFilter bounds a caller's filter, defaulting a missing one.
//
// The page size is what the generated statements bind, and MySQL's LIMIT takes a
// value rather than an expression — so an unset one has to become a number here
// rather than in a COALESCE the SQL cannot spell. Clamping is filtering's, at
// filtering's ceiling, which is the same treatment the URL parameter gets.
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

// waitlistsdbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSQLStore has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect, rather than
// panicking or leaning on waitlistsdb.New refusing the empty string.
func waitlistsdbDialect(d dialect.Dialect) (waitlistsdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return waitlistsdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return waitlistsdb.DialectMySQL, nil
	case dialect.SQLite:
		return waitlistsdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated waitlist queries for dialect %q", d)
	}
}
