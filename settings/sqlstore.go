package settings

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/settings/internal/settingsdb"
	"github.com/primandproper/platform-go/v14/settings/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// DefaultTablePrefix is the namespace the settings tables carry when none is
// configured, which is none — rendering settings_definitions and its two
// siblings.
//
// The settings_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_settings_definitions, for a database shared between applications. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema settings/migrations
// renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
type SQLStore struct {
	q    settingsdb.Querier
	o11y observability.Observer

	resolutionsCounter metrics.Int64Counter

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
// because the one timestamp this package reads back is the database server's. A
// consumer with nothing to join opens a transaction with Client.WithTransaction
// and passes the Tx it is handed.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger and traces to a noop provider.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "settings dialect %q", d)
	}

	s := &SQLStore{prefix: DefaultTablePrefix}

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
	// substitution; see settings/internal/settingsdb.
	qd, err := settingsdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := settingsdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the settings querier")
	}

	s.q = q

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One instrument, and it is the one nothing above this layer can see.
	//
	// A resolution answers from the subject's own value, from the definition's
	// default, or from neither, and which of the three is invisible to a caller
	// that simply reads a value and carries on. The proportions are what say
	// whether a default is doing the work it was written for and whether a
	// setting nobody has answered still has no default — the state that reads as
	// a working setting until something asks for it.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.resolutionsCounter, err = mp.NewInt64Counter(storeName + "_resolutions"); err != nil {
		return nil, platformerrors.Wrap(err, "creating settings store resolutions counter")
	}

	return s, nil
}

// TablePrefix returns the namespace this store's tables carry, for a caller that
// needs the rendered names — a maintenance TRUNCATE, a schema audit. Pass it to
// migrations.Tables.
func (s *SQLStore) TablePrefix() string { return s.prefix }

// countResolution records which of the three cases a resolution was.
func (s *SQLStore) countResolution(ctx context.Context, source Source) {
	s.resolutionsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String(sourceKey, string(source))))
}

// notFound maps a driver's empty-result error onto this package's sentinel for
// the entity that was missing, leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to a user
// as "no such setting". The sentinel is per-entity because the caller's next
// move differs: a missing definition is a mistake in the code that asked for it,
// and a missing value is the ordinary case of a subject who has not chosen.
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}

// guardCount turns "the statement touched nothing" into the sentinel for the
// row that was not there.
//
// Every predicate here includes the scope, so without this a write aimed at
// another tenant's row reports success. A driver that declines to report the
// count reaches this as an error rather than as an acknowledged unknown, because
// the generated method has no seam between running the statement and reading the
// count; none of the three supported drivers declines.
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

// settingsdbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSQLStore has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect, rather than
// panicking or leaning on settingsdb.New refusing the empty string.
func settingsdbDialect(d dialect.Dialect) (settingsdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return settingsdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return settingsdb.DialectMySQL, nil
	case dialect.SQLite:
		return settingsdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated settings queries for dialect %q", d)
	}
}

// walkPages reads a paged query to exhaustion, handing each row to visit.
//
// Two reads here are over a collection rather than a page of one: resolving
// every setting for a subject, and checking every stored value against a
// definition somebody is editing. Both are bounded — by the size of the
// catalog, and by the number of subjects who have answered one setting — and
// neither has a page a caller could ask for, so the walk lives here rather than
// in each of them.
//
// The cursor is required to advance. A store answering a page with the cursor it
// was handed would leave this walking one page forever, and a walk that stopped
// there instead would be worse: the rows past the stall are the ones the caller
// asked about, and a check that skipped them would approve an edit that strands
// values while reporting success.
func walkPages[T any](
	read func(*filtering.QueryFilter) (*filtering.QueryFilteredResult[T], error),
	visit func(*T) error,
) error {
	filter := filtering.DefaultQueryFilter()

	for {
		page, err := read(filter)
		if err != nil {
			return err
		}

		for _, row := range page.Data {
			if err = visit(row); err != nil {
				return err
			}
		}

		if page.Cursor == "" || len(page.Data) == 0 {
			return nil
		}

		if filter.Cursor != nil && *filter.Cursor == page.Cursor {
			return platformerrors.Wrapf(ErrCursorStalled, "cursor %q", page.Cursor)
		}

		next := page.Cursor
		filter.SetCursor(&next)
	}
}
