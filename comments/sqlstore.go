package comments

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/comments/internal/commentsdb"
	"github.com/primandproper/platform-go/v14/comments/migrations"

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

// DefaultTablePrefix is the namespace the comments table carries when none is
// configured, which is none — rendering comments.
//
// The comments name is the schema's, not the caller's: a table always says which
// package created it. Setting a namespace of "ddb" renders ddb_comments, for a
// database shared between applications. A namespace must not end in '_';
// database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans, logger, and instruments.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed [Store], against the schema comments/migrations
// renders.
//
// It is exported, and returned by [NewSQLStore], so a caller who has chosen SQL
// storage can depend on that choice rather than on the seam every backing
// shares.
type SQLStore struct {
	q       commentsdb.Querier
	o11y    observability.Observer
	targets Targets

	// absentTargetCounter counts creates refused because a registered existence
	// check did not find the target, which is the one number nothing above this
	// layer can see.
	//
	// Each one arrives above the store as a single refused write that a handler
	// turns into a 404 and somebody dismisses. In aggregate they are the
	// package's central hazard arriving as a race rather than as a leftover row:
	// a client working from a stale list, or a target being deleted underneath
	// the form somebody is typing into. A trickle is the web being the web; a
	// step change is a delete path that has started running without its sweep.
	absentTargetCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	prefix          string
}

// NewSQLStore builds a comment store over the given database.
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
// because every timestamp in this table is the database server's. A consumer
// with nothing to join opens a transaction with Client.WithTransaction and
// passes the Tx it is handed.
//
// The target catalog is an option and it defaults to empty, which is a store
// that refuses every write and answers every read. That is the same reading
// webhooks takes of its event catalog, and it is the safe one: a store built
// without a catalog is a wiring mistake, and a wiring mistake that stores rows
// under types nothing lists is worse than one that fails on the first write.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger, traces to a noop provider, and counts into a noop meter.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "comments dialect %q", d)
	}

	s := &SQLStore{
		prefix:  DefaultTablePrefix,
		targets: Targets{},
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
	// substitution; see comments/internal/commentsdb.
	qd, err := commentsdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := commentsdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the comments querier")
	}

	s.q = q
	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.absentTargetCounter, err = mp.NewInt64Counter(storeName + "_absent_targets"); err != nil {
		return nil, platformerrors.Wrap(err, "creating the comments store absent target counter")
	}

	return s, nil
}

// TablePrefix returns the namespace this store's table carries, for a caller
// rendering the migrations it needs.
func (s *SQLStore) TablePrefix() string { return s.prefix }

// TargetTypes returns the target types this store was built to accept, sorted.
//
// It is here so that the console rendering "what can be commented on" reads the
// catalog the store is actually enforcing rather than a second copy of it.
func (s *SQLStore) TargetTypes() []TargetType { return s.targets.TargetTypes() }

// countAbsentTarget records one create refused because the consumer's existence
// check did not find the target.
func (s *SQLStore) countAbsentTarget(ctx context.Context, targetType TargetType) {
	s.absentTargetCounter.Add(ctx, 1,
		metric.WithAttributes(attribute.String(targetTypeKey, targetType.String())))
}

// notFound maps a driver's empty-result error onto this package's sentinel,
// leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to a user
// as "nobody has said anything".
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}

// guardCount turns a write's affected-row count into the answer its caller acts
// on: nothing touched means the row the statement addressed was not there to
// touch.
//
// Every write in this store is keyed on the scope, so without this a write aimed
// at another tenant's row would report success having done nothing.
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

// commentsdbDialect maps this module's dialect names onto the generated
// package's.
//
// The set is closed on both sides — NewSQLStore has already rejected anything
// d.Valid() declines — so the default arm is reachable only when this module
// learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect rather than
// panicking or leaning on commentsdb.New refusing the empty string.
func commentsdbDialect(d dialect.Dialect) (commentsdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return commentsdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return commentsdb.DialectMySQL, nil
	case dialect.SQLite:
		return commentsdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated comments queries for dialect %q", d)
	}
}
