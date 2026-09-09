package registry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/uploads/registry/internal/registrydb"
	"github.com/primandproper/platform-go/v14/uploads/registry/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"
)

// DefaultTablePrefix is the namespace the registry table carries when none is
// configured, which is none — rendering uploads_objects.
//
// The uploads_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_uploads_objects, for a database shared between applications. A namespace
// must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans, logger, and instruments.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema
// uploads/registry/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
type SQLStore struct {
	q           registrydb.Querier
	o11y        observability.Observer
	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer and the instruments
	// are built from it. Read s.o11y.Logger() for the logger this store
	// actually uses; this one may be nil, because supplying none is how a
	// caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	prefix          string
}

// NewSQLStore builds a Store over the given database.
//
// The client is read at construction and not kept. It supplies the dialect, so
// the store and the database cannot disagree about which SQL to emit, and that
// is the whole of what it is for: every statement this store runs goes on the
// executor its caller hands over, so there is no connection of its own to hold.
// The prefix must still match the one the migrations were rendered with —
// nothing here can check that, and a mismatch surfaces as a missing table on
// the first query rather than at construction.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger, traces to a noop provider, and records to noop instruments.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "uploads registry dialect %q", d)
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
	// substitution; see uploads/registry/internal/registrydb.
	qd, err := registrydbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := registrydb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the uploads registry querier")
	}

	s.q = q

	if s.instruments, err = metrics.NewOperationSet(s.metricsProvider, storeName); err != nil {
		return nil, err
	}

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	return s, nil
}

// registrydbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSQLStore has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is
// a construction failure like any other, and it names the dialect, rather than
// panicking or leaning on registrydb.New refusing the empty string.
func registrydbDialect(d dialect.Dialect) (registrydb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return registrydb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return registrydb.DialectMySQL, nil
	case dialect.SQLite:
		return registrydb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "uploads registry dialect %q", d)
	}
}

// failed counts a failed operation and hands the error back unchanged.
//
// It takes the already-recorded error rather than the description, so that the
// description stays a literal at the call site: a helper that formatted it
// would be a printf wrapper whose format string is never constant.
//
// Errors is a subset of Requests rather than a series beside it, so this counts
// only the failure; the attempt was counted when the operation began.
func (s *SQLStore) failed(ctx context.Context, err error) error {
	s.instruments.Failed(ctx)

	return err
}

// RecordObject writes the row for an object in storage, through the caller's
// transaction.
//
// The three statements are one unit and always were — a collision check that
// cleared a key somebody else then took, an insert, and the read-back of the
// stamp the database assigned — but the transaction they are one unit in is now
// the caller's rather than one this store opened and closed behind their back.
// That is what lets the row commit with whatever references it: the profile row
// naming the avatar, the audit entry naming who uploaded it. It also means the
// creation time handed back is the one this transaction wrote, visible here
// before anybody else can see it.
func (s *SQLStore) RecordObject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	object *Object,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return s.failed(ctx, op.Error(ErrNilExecutor, "recording uploaded object"))
	}

	if object == nil {
		return s.failed(ctx, op.Error(ErrNilObject, "recording uploaded object"))
	}

	if err := object.ValidateWithContext(ctx); err != nil {
		return s.failed(ctx, op.Error(err, "recording uploaded object"))
	}

	if err := scope.Validate(); err != nil {
		return s.failed(ctx, op.Error(err, "recording uploaded object"))
	}

	if err := adoptScope(scope, object); err != nil {
		return s.failed(ctx, op.Error(err, "recording uploaded object"))
	}

	object.ID = newID(object.ID)

	op.Set(objectIDKey, object.ID).
		Set(objectKeyKey, object.Key)

	if err := s.ensureKeyFree(ctx, tx, scope, object.Key); err != nil {
		return s.failed(ctx, op.Error(err, "recording uploaded object"))
	}

	if err := s.q.CreateObject(ctx, tx, createObjectParams(scope, object)); err != nil {
		return s.failed(ctx, op.Error(err, "writing the uploads registry row"))
	}

	created, readErr := s.q.GetObjectCreatedAt(ctx, tx,
		registrydb.GetObjectCreatedAtParams{ID: object.ID, Scope: scope})
	if err := stampCreatedAt(&object.CreatedAt, created.CreatedAt, readErr); err != nil {
		return s.failed(ctx, op.Error(err, "recording uploaded object"))
	}

	return nil
}

// adoptScope settles which tenant a write is for, and writes the answer back
// onto the object.
//
// The scope the call named is the one the statement binds, so an object that
// names a different one is refused rather than corrected: the two disagreeing is
// a caller holding one tenant's object and registering it into another, which is
// a stale value or a mix-up and is not a thing to guess at. An object that names
// none adopts the argument. tenancy.Scope tells the zero value apart from
// Global(), so "unset" here is genuinely unset rather than the global scope
// spelled shortly.
func adoptScope(scope tenancy.Scope, object *Object) error {
	if object.Scope != (tenancy.Scope{}) && object.Scope != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"object names %q, the write names %q", object.Scope, scope)
	}

	object.Scope = scope

	return nil
}

// ensureKeyFree is the collision check the create runs before it writes, so a
// key already registered reports ErrObjectKeyTaken rather than a
// driver-specific constraint violation the caller would have to parse a
// SQLSTATE out of.
//
// The index is what actually guarantees uniqueness; this read is what turns the
// ordinary case into a sentinel. Two registrations racing for one key still
// reach the index, and the loser gets the driver's error — which is correct,
// rare, and the reason this check is not presented as the guarantee.
//
// It reads archived rows too, because the index does. Archival here is
// metadata-only and the bytes stay in the bucket, so a check that skipped
// archived rows would clear a write the index then refuses.
func (s *SQLStore) ensureKeyFree(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, key string) error {
	_, err := s.q.GetObjectIDByKey(ctx, q, registrydb.GetObjectIDByKeyParams{ObjectKey: key, Scope: scope})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return platformerrors.Wrap(err, "checking uploads registry key uniqueness")
	default:
		return ErrObjectKeyTaken
	}
}

// GetObject reads one of the scope's objects by row id, on the caller's
// executor — so a caller inside a transaction reads the row that transaction has
// written and not yet committed.
func (s *SQLStore) GetObject(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	objectID string,
) (*Object, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(objectIDKey, objectID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "reading uploaded object %q", objectID))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading uploaded object %q", objectID))
	}

	row, err := s.q.GetObject(ctx, q, registrydb.GetObjectParams{ID: objectID, Scope: scope})
	if err != nil {
		return nil, s.failed(ctx, op.Error(notFound(err, ErrObjectNotFound), "reading uploaded object %q", objectID))
	}

	return objectFromRow(&row), nil
}

// GetObjectByKey reads one of the scope's objects by the key its bytes live at,
// on the caller's executor.
func (s *SQLStore) GetObjectByKey(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	key string,
) (*Object, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(objectKeyKey, key),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "reading uploaded object at key %q", key))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading uploaded object at key %q", key))
	}

	row, err := s.q.GetObjectByKey(ctx, q, registrydb.GetObjectByKeyParams{ObjectKey: key, Scope: scope})
	if err != nil {
		return nil, s.failed(ctx, op.Error(notFound(err, ErrObjectNotFound), "reading uploaded object at key %q", key))
	}

	return objectFromKeyRow(&row), nil
}

// ListObjects pages the scope's objects, in the direction the filter names, on
// the caller's executor.
func (s *SQLStore) ListObjects(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Object], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing uploaded objects"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing uploaded objects"))
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]registrydb.ListObjectsRow, error) {
			return s.q.ListObjects(ctx, q, listObjectsParams(scope, filter))
		},
		func() ([]registrydb.ListObjectsDescendingRow, error) {
			return s.q.ListObjectsDescending(ctx, q,
				registrydb.ListObjectsDescendingParams(listObjectsParams(scope, filter)))
		},
		func(r registrydb.ListObjectsDescendingRow) registrydb.ListObjectsRow {
			return registrydb.ListObjectsRow(r)
		})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing uploaded objects"))
	}

	rows := make([]pageRow, 0, len(listRows))
	for i := range listRows {
		rows = append(rows, objectPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts, objectID, filter), nil
}

// ListObjectsByOwner pages one owner's objects within the scope, on the caller's
// executor.
func (s *SQLStore) ListObjectsByOwner(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	ownerID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Object], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(ownerIDKey, ownerID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing uploaded objects by owner"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing uploaded objects by owner"))
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]registrydb.ListObjectsByOwnerRow, error) {
			return s.q.ListObjectsByOwner(ctx, q, listByOwnerParams(scope, ownerID, filter))
		},
		func() ([]registrydb.ListObjectsByOwnerDescendingRow, error) {
			return s.q.ListObjectsByOwnerDescending(ctx, q,
				registrydb.ListObjectsByOwnerDescendingParams(listByOwnerParams(scope, ownerID, filter)))
		},
		func(r registrydb.ListObjectsByOwnerDescendingRow) registrydb.ListObjectsByOwnerRow {
			return registrydb.ListObjectsByOwnerRow(r)
		})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing uploaded objects by owner"))
	}

	rows := make([]pageRow, 0, len(listRows))
	for i := range listRows {
		rows = append(rows, objectPageRowForOwner(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts, objectID, filter), nil
}

// ListObjectsBySubject pages the objects attached to one thing within the scope,
// on the caller's executor.
func (s *SQLStore) ListObjectsBySubject(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject Subject,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Object], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectKey, subject.String()),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing uploaded objects by subject"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing uploaded objects by subject"))
	}

	// An unattached subject is refused rather than bound. The statement would
	// happily match every standalone upload in the scope — they carry the empty
	// pair the zero Subject binds — and report one caller's unrelated uploads as
	// another thing's attachments.
	if !subject.Attached() {
		return nil, s.failed(ctx, op.Error(ErrUnattachedSubject, "listing uploaded objects by subject"))
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]registrydb.ListObjectsBySubjectRow, error) {
			return s.q.ListObjectsBySubject(ctx, q, listBySubjectParams(scope, subject, filter))
		},
		func() ([]registrydb.ListObjectsBySubjectDescendingRow, error) {
			return s.q.ListObjectsBySubjectDescending(ctx, q,
				registrydb.ListObjectsBySubjectDescendingParams(listBySubjectParams(scope, subject, filter)))
		},
		func(r registrydb.ListObjectsBySubjectDescendingRow) registrydb.ListObjectsBySubjectRow {
			return registrydb.ListObjectsBySubjectRow(r)
		})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing uploaded objects by subject"))
	}

	rows := make([]pageRow, 0, len(listRows))
	for i := range listRows {
		rows = append(rows, objectPageRowForSubject(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts, objectID, filter), nil
}

// ArchiveObject soft-deletes the row through the caller's transaction. The
// object stays in the bucket.
func (s *SQLStore) ArchiveObject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	objectID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(objectIDKey, objectID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return s.failed(ctx, op.Error(ErrNilExecutor, "archiving uploaded object %q", objectID))
	}

	if err := scope.Validate(); err != nil {
		return s.failed(ctx, op.Error(err, "archiving uploaded object %q", objectID))
	}

	count, err := s.q.ArchiveObject(ctx, tx, registrydb.ArchiveObjectParams{ID: objectID, Scope: scope})
	if err = guardCount(count, err, ErrObjectNotFound); err != nil {
		return s.failed(ctx, op.Error(err, "archiving uploaded object %q", objectID))
	}

	return nil
}

// guardCount maps "touched nothing" onto the sentinel for the row that was not
// there.
//
// Every predicate in this schema includes the scope, so without this a write
// aimed at another tenant's row reports success. The statement also carries the
// archived predicate, which is what makes archiving an already-archived row
// report the row as absent rather than silently moving its timestamp forward.
func guardCount(count int64, err, missing error) error {
	if err != nil {
		return err
	}

	if count == 0 {
		return missing
	}

	return nil
}

// stampCreatedAt writes the creation time the database assigned onto the value
// the caller handed over.
//
// The column is the database's — see uploads/registry/internal/queries — so the
// create does not carry it, and the alternative to this read is a caller whose
// struct says 0001-01-01 for a row that was written a moment ago. CreatedAt is
// exported and a service serializes the value it just created straight into a
// response, where a zero time renders as a date rather than reading as an
// absence.
func stampCreatedAt(at *time.Time, created time.Time, err error) error {
	if err != nil {
		return platformerrors.Wrap(err, "reading back the assigned creation time")
	}

	*at = created.UTC()

	return nil
}

// notFound maps a driver's empty-result error onto this package's sentinel,
// leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to a user
// as "no such object" — which, on a path that decides whether somebody may have
// a file, is the difference between an outage and a permission denial.
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}

// newID returns the identifier a write should carry: the one the caller set, or
// a fresh one.
//
// Accepting a caller-supplied ID matters where the row referencing the upload is
// written in the same request as the upload itself, and the reference has to be
// minted before either write runs.
func newID(existing string) string {
	if existing != "" {
		return existing
	}

	return identifiers.New()
}

// pageFilter bounds a caller's filter, and supplies the default one for a caller
// who passed none.
//
// The bound is not politeness: MaxResponseSize reaches the statement as its
// LIMIT, and an unbounded one is a page that reads the whole table.
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
