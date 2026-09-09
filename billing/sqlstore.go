package billing

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/billing/internal/billingdb"
	"github.com/primandproper/platform-go/v14/billing/migrations"

	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// DefaultTablePrefix is the namespace the billing tables carry when none is
// configured, which is none — rendering billing_products, billing_subscriptions,
// billing_purchases and billing_transactions.
//
// The billing_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_billing_products, for a database shared between applications. A namespace
// must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema billing/migrations
// renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
//
// # One note on MySQL, because it changes what an error means
//
// The generated querier reports :execrows as rows *changed* on MySQL and rows
// *matched* on the other two. Every guarded write here discriminates in its own
// predicate — a status write requires the status to differ, a completion
// requires completed_at to be NULL — so those behave identically on all three.
// UpdateProduct and UpdateSubscription do not: on MySQL, saving a value
// identical to the one already stored reports zero rows and therefore surfaces
// as a not-found. A deployment that wants the other reading sets
// clientFoundRows=true in its MySQL DSN, which is the switch that puts MySQL on
// matched semantics.
type SQLStore struct {
	q    billingdb.Querier
	o11y observability.Observer

	clock clock.Clock

	transactionsCounter metrics.Int64Counter

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
// no Writer() for the writes and no Reader() for the reads. A consumer with
// nothing to join opens a transaction with Client.WithTransaction and passes the
// Tx it is handed.
//
// The one clock this store reads is its own, not the client's. It decides which
// subscriptions are current and stamps a completion the caller supplied no time
// for, and both have to move when a test moves them — see WithClock.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger and traces to a noop provider.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "billing dialect %q", d)
	}

	s := &SQLStore{
		prefix: DefaultTablePrefix,
		clock:  defaultClock(),
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
	// substitution; see billing/internal/billingdb.
	qd, err := billingdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := billingdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the billing querier")
	}

	s.q = q

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One instrument, and it is the one nothing above this layer can see.
	//
	// A payment attempt either settles, fails, or is given back, and which of
	// those happened is invisible to a caller that writes a row and carries on.
	// The proportions are the health of a payment integration: a rate of
	// failures is a card form that has stopped working or a provider having a
	// bad afternoon, and a rate of refunds is a number somebody has to answer
	// for. Neither shows up in a request-level metric, because both are recorded
	// from a webhook nobody is waiting on.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.transactionsCounter, err = mp.NewInt64Counter(storeName + "_transactions"); err != nil {
		return nil, platformerrors.Wrap(err, "creating billing store transactions counter")
	}

	return s, nil
}

// TablePrefix returns the namespace this store's tables carry, for a caller that
// needs the rendered names — a maintenance TRUNCATE, a schema audit. Pass it to
// migrations.Tables.
func (s *SQLStore) TablePrefix() string { return s.prefix }

// countTransaction records a ledger row reaching a status, including the one it
// is written at.
func (s *SQLStore) countTransaction(ctx context.Context, status TransactionStatus) {
	s.transactionsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String(statusKey, string(status))))
}

// notFound maps a driver's empty-result error onto this package's sentinel for
// the entity that was missing, leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to
// somebody as "no such subscription". The sentinel is per-entity because the
// caller's next move differs: a missing product is a broken catalog reference,
// and a missing transaction is the ordinary case of an event arriving before the
// charge it describes.
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}

// guardCount turns "the statement touched nothing" into the sentinel for the row
// that was not there, or for the guard that was not satisfied.
//
// Every predicate here includes the scope, so without this a write aimed at
// another tenant's row reports success. It is also how a guarded status write
// answers: the count, not a read before it, decides whether this caller is the
// one that moved the row.
func guardCount(count int64, err, missing error, operation string) error {
	if err != nil {
		return platformerrors.Wrap(err, operation)
	}

	if count == 0 {
		return missing
	}

	return nil
}

// refuseCreate says which of a create's identifiers the insert lost to, having
// written nothing.
//
// The insert-ignore behind every create here reports a loss as a zero affected
// count and cannot say what it lost to — and on MySQL that is broader than the
// conflict target it names, because IGNORE downgrades every constraint on the
// table rather than the one index. So the attribution is a read, made on the
// losing path and therefore never on the hot one, in the same shape
// refuseStatusWrite uses for the same reason.
//
// The provider's identifier is asked about first, because that is the collision
// this schema is shaped around: a redelivered webhook, whose whole answer is the
// exists sentinel the caller acknowledges the delivery on. What is left — no
// provider identifier on the row, or one nobody else holds — is the id, which
// only a caller that supplied its own can collide on.
//
// residual is what a table asks after the provider's identifier has come back
// clean and before the id is blamed. Only the ledger has one — its row points at
// two others, and MySQL's IGNORE reports a foreign key it could not satisfy with
// the same zero count as a collision. Every other table passes nil.
func refuseCreate(externalID, id string, lookup func() error, notFound, exists error, residual func() error) error {
	if externalID != "" {
		switch err := lookup(); {
		case err == nil:
			return platformerrors.Wrapf(exists, "external id %q", externalID)
		case !errors.Is(err, notFound):
			return platformerrors.Wrap(err, "attributing a skipped insert")
		}
	}

	if residual != nil {
		if err := residual(); err != nil {
			return err
		}
	}

	return platformerrors.Wrapf(ErrIDTaken, "%q", id)
}

// requirePresence turns a presence read's outcome into the answer a ledger write
// wants: nothing missing, the referent's own not-found sentinel, or the failure
// as it came.
//
// The statement behind the read it interprets is archived-blind, because
// archiving a subscription deliberately leaves the ledger rows pointing at it
// alone — so an archived referent is present as far as both the foreign key and
// this question are concerned. See queries.referentChecks.
func requirePresence(err, missing error, id string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return platformerrors.Wrapf(missing, "%q", id)
	default:
		return platformerrors.Wrap(err, "checking the row a ledger write names")
	}
}

// requireProduct refuses a write naming a product this scope does not have.
//
// It is asked on the caller's transaction, before the insert, and it is why
// billing_products is the one table here that keeps the standard existence
// check. The foreign key would refuse the row anyway on Postgres and SQLite — but
// MySQL's INSERT IGNORE downgrades a foreign key violation to a warning and a
// zero count, which is indistinguishable from the uniqueness collision that count
// exists to report. Asking first is what makes a bad product id one answer on all
// three dialects instead of two errors and a lie.
func (s *SQLStore) requireProduct(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	productID string,
) error {
	row, err := s.q.CheckProductExistence(ctx, q,
		billingdb.CheckProductExistenceParams{ID: productID, Scope: scope})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return platformerrors.Wrapf(ErrProductNotFound, "product %q", productID)
		}

		return platformerrors.Wrap(err, "checking the product a write names")
	}

	if !row.Exists {
		return platformerrors.Wrapf(ErrProductNotFound, "product %q", productID)
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

// billingdbDialect maps this module's dialect names onto the generated package's.
// The set is closed on both sides — NewSQLStore has already rejected anything
// d.Valid() declines — so the default arm is reachable only when this module
// learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect, rather than
// panicking or leaning on billingdb.New refusing the empty string.
func billingdbDialect(d dialect.Dialect) (billingdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return billingdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return billingdb.DialectMySQL, nil
	case dialect.SQLite:
		return billingdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated billing queries for dialect %q", d)
	}
}
