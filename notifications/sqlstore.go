package notifications

import (
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/notifications/internal/notificationsdb"
	"github.com/primandproper/platform-go/v14/notifications/migrations"

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
)

// DefaultTablePrefix is the namespace the notifications tables carry when none
// is configured, which is none — rendering notifications_inbox and
// notifications_devices.
//
// The notifications_ segment is the schema's, not the caller's: a table always
// says which package created it. Setting a namespace of "ddb" renders
// ddb_notifications_inbox, for a database shared between applications. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger.
const storeName = serviceName + "_store"

// SQLStore is the SQL-backed [Inbox] and [Registry], against the schema
// notifications/migrations renders.
//
// One type implements both, because they are one schema, one connection, and
// one migration; declaring two would make a consumer that wants an inbox and a
// registry build the same thing twice. The interfaces stay separate because
// their consumers are — see store.go.
//
// It is exported, and returned by [NewSQLStore], so a caller who has chosen SQL
// storage can depend on that choice rather than on the seams every backing
// shares.
type SQLStore struct {
	// client is not what any consumer-facing method runs on — those are handed
	// the caller's transaction or executor. It is here for the two things a
	// Client answers that a caller's Tx cannot: the dialect the generated
	// statements are rendered for, read once at construction, and the connection
	// InvalidateDeviceToken runs on, which belongs to a provider's verdict
	// rather than to anybody's request and so can join nobody's transaction.
	client database.Client
	q      notificationsdb.Querier
	o11y   observability.Observer
	clock  clock.Clock

	// invalidatedTokensCounter counts the rows the provider feedback hook
	// destroyed, which is the one number nothing above this layer can see.
	//
	// A registry's health is the rate at which it sheds dead tokens against the
	// rate it takes new ones. A steady trickle is handsets being wiped and apps
	// being uninstalled, which is the loop working; a step change is a
	// credential or a bundle identifier that has moved, where every token a
	// deployment holds is being classified dead one push at a time — and that is
	// indistinguishable from ordinary churn unless somebody is counting.
	invalidatedTokensCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	prefix          string
}

// NewSQLStore builds an inbox and a device registry over the given database.
//
// The client is not what the consumer-facing methods execute on. Every write a
// consumer calls takes the caller's database.Tx and every read takes an
// executor, so there is no Writer() behind a create and no Reader() behind a
// list; a consumer with nothing to join opens a transaction with
// Client.WithTransaction and passes the Tx it is handed. What this one supplies
// is the dialect, and the connection [SQLStore.InvalidateDeviceToken] runs on —
// the one write here with no consumer request behind it. See [Registry] for why
// that method takes no executor.
//
// The dialect comes from the client, so the two cannot disagree. The prefix must
// still match the one the migrations were rendered with — nothing here can check
// that, and a mismatch surfaces as a missing table on the first query rather
// than at construction.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger and traces to a noop provider.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "notifications dialect %q", d)
	}

	s := &SQLStore{
		client: client,
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
	// substitution; see notifications/internal/notificationsdb.
	qd, err := notificationsdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := notificationsdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the notifications querier")
	}

	s.q = q
	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.invalidatedTokensCounter, err = mp.NewInt64Counter(storeName + "_invalidated_device_tokens"); err != nil {
		return nil, platformerrors.Wrap(err, "creating the invalidated device token counter")
	}

	return s, nil
}

// TablePrefix returns the namespace this store's tables carry, for a caller
// rendering the migrations it needs.
func (s *SQLStore) TablePrefix() string { return s.prefix }

// now is the one clock read every write in this package goes through, in UTC.
//
// The location is load-bearing on SQLite rather than cosmetic: modernc's driver
// stores a bound time.Time as Go's own String() rendering, so a comparison
// against that column is a string comparison, and it is chronological only
// because every value written is UTC in a fixed-width prefix position.
func (s *SQLStore) now() time.Time { return s.clock.Now().UTC() }

// notFound maps a driver's empty-result error onto this package's sentinel for
// the entity that was missing, leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to a user
// as "you have no notifications".
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}

// guardCount turns a guarded write's affected-row count into the answer its
// caller acts on: nothing touched means the row the statement addressed was not
// there to touch.
//
// Every write in this store is keyed on the scope and, for the inbox, on the
// principal, so without this a write aimed at another tenant's row would report
// success having done nothing.
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

// notificationsdbDialect maps this module's dialect names onto the generated
// package's.
//
// The set is closed on both sides — NewSQLStore has already rejected anything
// d.Valid() declines — so the default arm is reachable only when this module
// learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect rather than
// panicking or leaning on notificationsdb.New refusing the empty string.
func notificationsdbDialect(d dialect.Dialect) (notificationsdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return notificationsdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return notificationsdb.DialectMySQL, nil
	case dialect.SQLite:
		return notificationsdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated notifications queries for dialect %q", d)
	}
}
