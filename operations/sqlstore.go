package operations

import (
	"context"
	"database/sql"
	stderrors "errors"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v14/operations/internal/operationsdb"
	"github.com/primandproper/platform-go/v14/operations/migrations"

	"github.com/primandproper/primitives-go/charset"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlguard"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"
)

// storeName scopes the store's spans and logger. It is deliberately not
// serviceName: a trace showing an operation running and the rows it moved wants
// those distinguishable, and one scope for both would make a store read look
// like a Runner executing in every span listing.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the Postgres-backed Store, against the schema
// operations/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
//
// Every statement it runs comes from operations/internal/operationsdb, which is
// what sqlc-gen-unison generated from the corpus in
// operations/internal/queries. Nothing here composes SQL.
type SQLStore struct {
	client database.Client
	q      operationsdb.Querier
	o11y   observability.Observer

	guardMissCounter metrics.Int64Counter

	// guard is what a guarded write means in this package when it matches no
	// row. See internal/sqlguard.
	guard         sqlguard.Guard
	notifyCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	notifyChannel string
	tablePrefix   string
}

// NewSQLStore builds a Store over the given database, which must speak Postgres.
//
// The dialect comes from the client, so the two cannot disagree. The prefix must
// still match the one the migrations were rendered with — nothing here can check
// that, and a mismatch surfaces as a missing table on the first query rather
// than at construction.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger, traces to a noop provider, and counts into a noop meter.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if err := dialect.RequirePostgres("operations", client.Dialect()); err != nil {
		return nil, err
	}

	s := &SQLStore{client: client, tablePrefix: DefaultTablePrefix}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.tablePrefix); err != nil {
		return nil, err
	}

	// The channel is bound as text by the statement this package emits, but the
	// listener on the other end has to render it into a LISTEN, which takes no
	// parameters. Vetting it here is what keeps that end from having to.
	if s.notifyChannel != "" && !dialect.ValidIdentifier(s.notifyChannel) {
		return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier,
			"operations notify channel %q", s.notifyChannel)
	}

	// The generated querier, instantiated once the prefix is settled. The
	// dialect is not a choice here the way it is for a three-dialect store:
	// RequirePostgres has already refused everything else, and the generated
	// package was generated for a roster of one.
	q, err := operationsdb.New(operationsdb.DialectPostgreSQL, ddl.Qualify(s.tablePrefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the operations querier")
	}

	s.q = q

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	// Two counters, and only two. The Service and the Worker own the business
	// totals — started, succeeded, failed — and a second name for the same event
	// is how two dashboards come to disagree. What no caller above this layer can
	// count is a guarded write that matched no row, which is not a database error
	// and is indistinguishable from one from up there; and a notification that
	// could not be sent, which is the difference between a watch path that pushes
	// and one that has quietly become a poll.
	if s.guardMissCounter, err = mp.NewInt64Counter(storeName + "_guard_misses"); err != nil {
		return nil, platformerrors.Wrap(err, "creating operations store guard miss counter")
	}

	if s.notifyCounter, err = mp.NewInt64Counter(storeName + "_notify_failures"); err != nil {
		return nil, platformerrors.Wrap(err, "creating operations store notify failure counter")
	}

	s.guard = sqlguard.Guard{
		MissCounter: s.guardMissCounter,
		NotFound:    ErrOperationNotFound,
		Namespace:   "operations",
		IDKey:       operationIDKey,
		Message:     "operation left the active set before its outcome could be recorded",
		Reason:      "operation %q is no longer active",
	}

	return s, nil
}

func (s *SQLStore) Insert(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	op *Operation,
) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(ownerKey, scope.String()))
	defer span.End()

	if tx == nil {
		return nil, span.Error(ErrNilExecutor, "inserting operation")
	}

	if op == nil {
		return nil, span.Error(ErrNilOperation, "inserting operation")
	}

	if err := adoptScope(scope, op); err != nil {
		return nil, span.Error(err, "inserting operation")
	}

	span.SetValues(map[string]any{
		operationIDKey: op.ID,
		kindKey:        op.Kind,
	})

	row, err := s.q.CreateOperation(ctx, tx, createParams(op))
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			// No rows means the conflict clause absorbed a collision, which
			// means WithID did its job: the caller retried a Start under an ID
			// they derived, and the operation they asked for already exists.
			// Attached to the span but not logged as an error — Service.Start
			// turns this into the operation that is already running.
			span.Set(guardMissedKey, true)

			return nil, platformerrors.Wrapf(ErrDuplicateOperation, "operation %q", op.ID)
		}

		return nil, span.Error(err, "inserting operation")
	}

	// Deliberately not notified here. The insert may be inside the caller's
	// transaction, and a notification sent before that transaction commits
	// announces a row a listener cannot yet read. The enqueue that follows Start
	// is the better signal anyway, and nothing subscribes to an operation whose
	// ID it has not been handed yet.
	shared := operationsdb.GetOperationRow(row)

	return operationFromRow(&shared), nil
}

func (s *SQLStore) Get(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	id string,
) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		operationIDKey: id,
		ownerKey:       scope.String(),
	}))
	defer span.End()

	if q == nil {
		return nil, span.Error(ErrNilExecutor, "reading operation")
	}

	row, err := s.q.GetOperationInScope(ctx, q, operationsdb.GetOperationInScopeParams{ID: id, Scope: scope})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			// Attached to the span but not logged as an error. An operation ID
			// that is not in the table, and one that is in somebody else's
			// scope, are both a 404 somebody is owed rather than a fault of this
			// process, and painting the trace red for them buries the ones that
			// are.
			span.Set(guardMissedKey, true)

			return nil, platformerrors.Wrapf(ErrOperationNotFound, "operation %q", id)
		}

		return nil, span.Error(err, "reading operation")
	}

	shared := operationsdb.GetOperationRow(row)
	op := operationFromRow(&shared)

	span.Set(stateKey, string(op.State)).Set(kindKey, op.Kind).Set(revisionKey, op.Revision)

	return op, nil
}

// unscopedRow is the read-back RequestCancel makes, on an id it has just
// written and with no scope to hold. It is the whole of what the unscoped
// statement in the corpus is for; see Store on the seven methods that take
// neither, and why this is not a variant a consumer read may reach for.
func (s *SQLStore) unscopedRow(ctx context.Context, id string) (*Operation, error) {
	row, err := s.q.GetOperation(ctx, s.client.Reader(), operationsdb.GetOperationParams{ID: id})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return nil, platformerrors.Wrapf(ErrOperationNotFound, "operation %q", id)
		}

		return nil, err
	}

	return operationFromRow(&row), nil
}

func (s *SQLStore) GetMany(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	ids []string,
) ([]*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		batchKey: len(ids),
		ownerKey: scope.String(),
	}))
	defer span.End()

	if q == nil {
		return nil, span.Error(ErrNilExecutor, "reading operations")
	}

	// An empty batch is an empty answer without a query: the statement the
	// corpus carries has no rendering of an empty set, and sending one anyway is
	// a round trip whose answer was known before it left — see
	// querygen.Generator.SetReadQuery, which documents the contract this keeps.
	if len(ids) == 0 {
		return nil, nil
	}

	rows, err := s.q.GetOperations(ctx, q, operationsdb.GetOperationsParams{Scope: scope, IDs: ids})
	if err != nil {
		return nil, span.Error(err, "reading operations")
	}

	ops := operationsFromRows(rows, func(r operationsdb.GetOperationsRow) operationsdb.GetOperationRow {
		return operationsdb.GetOperationRow(r)
	})

	span.Set(resultCountKey, len(ops))

	return ops, nil
}

func (s *SQLStore) List(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	listScope *ListScope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Operation], error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(ownerKey, scope.String()))
	defer span.End()

	if q == nil {
		return nil, span.Error(ErrNilExecutor, "listing operations")
	}

	if listScope != nil {
		span.SetValues(map[string]any{
			kindKey:  listScope.Kind,
			stateKey: strings.Join(stateStrings(listScope.States), ","),
		})
	}

	filter, filterErr := pageFilter(filter)
	if filterErr != nil {
		// A sort direction filtering does not recognize. The filter is usable
		// and ascending, so the page is still answerable and is still what a
		// caller sending nothing would have got; what is not answerable is the
		// direction they asked for, which is worth a line rather than a failed
		// read. Whatever turned a request into this filter has reported it to
		// whoever typed it.
		span.Logger().Error("normalizing operations list filter", filterErr)
	}

	span.Set(limitKey, int(*filter.MaxResponseSize))

	// Ordering follows the filter rather than a package-local preference.
	// filtering.DefaultQueryFilter asks for ascending, and a package that quietly
	// reversed it would make this the one list endpoint in the module whose sort
	// does not mean what the shared filter says it means. A direction is
	// statement text rather than a bound value, so the corpus carries the pair
	// and the reading of the field is filtering's — one home for it, so a store
	// cannot come to differ from the generated statements about what "desc" is.
	listRows, err := sortedRows(filter,
		func() ([]operationsdb.ListOperationsRow, error) {
			return s.q.ListOperations(ctx, q, listParams(scope, listScope, filter))
		},
		func() ([]operationsdb.ListOperationsDescendingRow, error) {
			return s.q.ListOperationsDescending(ctx, q,
				operationsdb.ListOperationsDescendingParams(listParams(scope, listScope, filter)))
		},
		func(r operationsdb.ListOperationsDescendingRow) operationsdb.ListOperationsRow {
			return operationsdb.ListOperationsRow(r)
		})
	if err != nil {
		return nil, span.Error(err, "listing operations")
	}

	rows := make([]pageRow, 0, len(listRows))
	for i := range listRows {
		rows = append(rows, operationPageRow(&listRows[i]))
	}

	span.Set(resultCountKey, len(rows))

	// The cursor is the id, because the statement orders by it. A cursor naming
	// a position in an order the query does not use is a page that skips rows
	// and repeats others, with nothing reporting an error.
	return filtering.Drain(rows, pageValue, pageCounts,
		func(o *Operation) string { return o.ID }, filter), nil
}

func (s *SQLStore) Begin(ctx context.Context, id string, attempts int, lease time.Duration) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		operationIDKey: id,
		attemptsKey:    attempts,
	}))
	defer span.End()

	row, err := s.q.BeginOperation(ctx, s.client.Writer(), operationsdb.BeginOperationParams{
		RunningState:      string(StateRunning),
		Attempts:          int64(attempts),
		LeaseMicroseconds: lease.Microseconds(),
		ID:                id,
		ActiveStates:      activeStates(),
	})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			// The guard did its job: the operation is gone, finished, or still
			// leased by a worker that has not given it up. Counted rather than
			// logged as an error, because from here the three are the same
			// answer — not ours to run — and only one of them is interesting.
			span.Set(guardMissedKey, true)
			s.guardMissCounter.Add(ctx, 1, s.guard.OpAttr("begin"))

			return nil, platformerrors.Wrapf(ErrOperationNotFound, "operation %q is not claimable", id)
		}

		return nil, span.Error(err, "beginning operation")
	}

	shared := operationsdb.GetOperationRow(row)
	op := operationFromRow(&shared)

	span.Set(kindKey, op.Kind).Set(revisionKey, op.Revision)
	s.notify(ctx)

	return op, nil
}

func (s *SQLStore) Progress(ctx context.Context, id string, progress Progress, lease time.Duration) (Ack, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		operationIDKey: id,
		unitKey:        progress.Unit,
		unitsDoneKey:   progress.UnitsDone,
		countKey:       progress.Count,
	}))
	defer span.End()

	var total *int64
	if progress.UnitsTotal != nil {
		widened := int64(*progress.UnitsTotal)
		total = &widened
	}

	row, err := s.q.RecordOperationProgress(ctx, s.client.Writer(), operationsdb.RecordOperationProgressParams{
		UnitsTotal:        total,
		UnitsDone:         int64(progress.UnitsDone),
		ProgressUnit:      charset.TruncateUTF8(progress.Unit, MaxMessageLength),
		ProgressCount:     progress.Count,
		ProgressMessage:   charset.TruncateUTF8(progress.Message, MaxMessageLength),
		LeaseMicroseconds: lease.Microseconds(),
		ID:                id,
		RunningState:      string(StateRunning),
	})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			// Not an error. The operation left the running state under this
			// worker — reclaimed after a lapsed lease, or finished by somebody
			// else — and Held is precisely how that is reported. Returning an
			// error here would have every Runner's progress call fail at the one
			// moment the Runner most needs to hear a plain "stop".
			span.Set(guardMissedKey, true)
			s.guardMissCounter.Add(ctx, 1, s.guard.OpAttr("progress"))

			return Ack{}, nil
		}

		return Ack{}, span.Error(err, "recording operation progress")
	}

	ack := Ack{Revision: row.Revision, CancelRequested: row.CancelRequested, Held: true}

	span.Set(revisionKey, ack.Revision).Set(cancelledKey, ack.CancelRequested)
	s.notify(ctx)

	return ack, nil
}

func (s *SQLStore) Finish(
	ctx context.Context,
	id string,
	state State,
	result *Result,
	opErr *Error,
	unitsAllDone bool,
) error {
	ctx, span := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		operationIDKey: id,
		stateKey:       string(state),
		terminalKey:    true,
	}))
	defer span.End()

	if !state.Terminal() {
		return span.Error(platformerrors.Wrapf(ErrInvalidDefinition,
			"state %q is not terminal", state), "finishing operation")
	}

	if result != nil && len(result.Detail) > MaxResultDetailBytes {
		return span.Error(platformerrors.Wrapf(ErrResultTooLarge,
			"%d bytes, limit %d", len(result.Detail), MaxResultDetailBytes), "finishing operation")
	}

	params := operationsdb.FinishOperationParams{
		State:        string(state),
		ID:           id,
		ActiveStates: activeStates(),
	}

	if result != nil {
		params.ResultURI, params.ResultDetail = result.URI, result.Detail
	}

	if opErr != nil {
		params.ErrorCode = opErr.Code
		params.ErrorMessage = charset.TruncateUTF8(opErr.Message, MaxMessageLength)
		params.ErrorRetryable = opErr.Retryable
	}

	// Two statements rather than one with a conditional SET, because the SET
	// list is the statement rather than an argument to it: raising units_done to
	// the declared total is what a success that finished every unit without
	// reporting the last one needs, and a run that stopped short must not have
	// its counter completed for it.
	affected, err := s.finishRows(ctx, &params, unitsAllDone)

	return s.reportGuardedWrite(ctx, span, affected, err, id, "finish", "finishing operation")
}

// finishRows runs whichever of the two terminal writes the caller asked for.
//
// The params types are distinct and identical, which is the generated package
// saying that these are two statements over one argument list; the conversion is
// what makes a divergence between them a build failure here.
func (s *SQLStore) finishRows(
	ctx context.Context,
	params *operationsdb.FinishOperationParams,
	unitsAllDone bool,
) (int64, error) {
	if unitsAllDone {
		return s.q.FinishOperationWithEveryUnitDone(ctx, s.client.Writer(),
			operationsdb.FinishOperationWithEveryUnitDoneParams(*params))
	}

	return s.q.FinishOperation(ctx, s.client.Writer(), *params)
}

func (s *SQLStore) Release(ctx context.Context, id string, opErr *Error) error {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(operationIDKey, id))
	defer span.End()

	params := operationsdb.ReleaseOperationParams{
		PendingState: string(StatePending),
		ID:           id,
		RunningState: string(StateRunning),
	}

	if opErr != nil {
		params.ErrorCode = opErr.Code
		params.ErrorMessage = charset.TruncateUTF8(opErr.Message, MaxMessageLength)
	}

	affected, err := s.q.ReleaseOperation(ctx, s.client.Writer(), params)

	return s.reportGuardedWrite(ctx, span, affected, err, id, "release", "releasing operation")
}

func (s *SQLStore) RequestCancel(ctx context.Context, id string) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(operationIDKey, id))
	defer span.End()

	affected, err := s.q.RequestOperationCancel(ctx, s.client.Writer(), operationsdb.RequestOperationCancelParams{
		PendingState:   string(StatePending),
		CancelledState: string(StateCancelled),
		ID:             id,
		ActiveStates:   activeStates(),
	})
	if err != nil {
		return nil, span.Error(err, "requesting operation cancellation")
	}

	span.Set(rowsAffectedKey, affected)

	if affected > 0 {
		s.notify(ctx)
	}

	// Read back either way. Zero rows means the operation was already terminal,
	// which is not a failure — the caller wanted it not running and it is not
	// running — so the answer is the row as it stands, and the read-back is what
	// reports a genuinely absent operation.
	op, err := s.unscopedRow(ctx, id)
	if err != nil {
		return nil, span.Error(err, "reading cancelled operation")
	}

	return op, nil
}

func (s *SQLStore) Stranded(ctx context.Context, grace time.Duration, limit int) ([]*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer span.End()

	if limit <= 0 {
		return nil, nil
	}

	rows, err := s.q.ListStrandedOperations(ctx, s.client.Reader(), operationsdb.ListStrandedOperationsParams{
		PendingState:      string(StatePending),
		GraceMicroseconds: grace.Microseconds(),
		RunningState:      string(StateRunning),
		StrandedLimit:     int64(limit),
	})
	if err != nil {
		return nil, span.Error(err, "reading stranded operations")
	}

	ops := operationsFromRows(rows, func(r operationsdb.ListStrandedOperationsRow) operationsdb.GetOperationRow {
		return operationsdb.GetOperationRow(r)
	})

	span.Set(resultCountKey, len(ops))

	return ops, nil
}

func (s *SQLStore) Reap(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer span.End()

	if limit <= 0 {
		return 0, nil
	}

	affected, err := s.q.ReapOperations(ctx, s.client.Writer(), operationsdb.ReapOperationsParams{
		TerminalStates:        terminalStates(),
		RetentionMicroseconds: retention.Microseconds(),
		ReapLimit:             int64(limit),
	})
	if err != nil {
		return 0, span.Error(err, "reaping operations")
	}

	span.Set(rowsAffectedKey, affected)

	return affected, nil
}

// notify wakes whatever is watching, after the row has landed.
//
// It is best-effort by design and its failure is counted rather than returned.
// A notification carries no information — see the package documentation — so a
// lost one costs a watcher its poll interval and costs the operation nothing.
// Failing a progress flush because a notification did not go out would trade
// something that matters for something that does not.
//
// It is the one statement this store runs that is not in the corpus, and it is
// exempt for the reason database/dialect is: NOTIFY addresses a channel rather
// than a table, so there is no schema for sqlc to check it against.
func (s *SQLStore) notify(ctx context.Context) {
	if s.notifyChannel == "" {
		return
	}

	if _, err := s.client.Writer().ExecContext(ctx, dialect.PostgresNotifyStatement, s.notifyChannel); err != nil {
		s.notifyCounter.Add(ctx, 1)
		s.o11y.Logger().WithValue(notifyKey, s.notifyChannel).Error("notifying operations channel", err)
	}
}

// reportGuardedWrite says what a guarded write's row count meant, and wakes the
// watchers when it landed.
//
// The guard's distinction matters more here than it looks. A finish that matches
// no rows means the operation left the active set while the Runner was working —
// finished by another worker after a lease lapsed, or cancelled outright — and
// treating that as success would have the worker report a result the database
// never recorded, to a client that will poll the row and see something else.
//
// The statement has already run by the time this is called, because an
// :execrows method has no seam between running and reading the count. That is
// the one narrowing against the hand-written path it replaced: a driver
// declining to report a count arrives here as an error rather than as an
// acknowledged unknown, and none of the drivers this package supports declines.
func (s *SQLStore) reportGuardedWrite(
	ctx context.Context,
	span observability.Operation,
	affected int64,
	err error,
	id, operation, description string,
) error {
	if countErr := s.guard.Count(ctx, span, affected, err, id, operation, description); countErr != nil {
		return countErr
	}

	s.notify(ctx)

	return nil
}

// sortedRows runs whichever of the listing's two statements the filter's sort
// direction names, and hands back the ascending statement's rows either way.
//
// A paged list is two statements, because a direction is which way the ORDER BY
// runs and which way the cursor comparison points — statement text, not a bound
// value. database/querygen emits the pair and filtering.QueryFilter.SortsDescending
// picks between them; this is where the pick is made. A read that reached for
// the ascending statement while holding a descending filter would answer in the
// order the client did not ask for, and nothing about the rows that came back
// would say so.
//
// The descending rows are converted rather than restated field by field, and the
// conversion is the assertion: the two are one projection rendered twice, with
// the walk reversed and nothing else changed, so the day they stop being
// identical this stops building.
func sortedRows[Ascending, Descending any](
	filter *filtering.QueryFilter,
	ascending func() ([]Ascending, error),
	descending func() ([]Descending, error),
	same func(Descending) Ascending,
) ([]Ascending, error) {
	if !filter.SortsDescending() {
		return ascending()
	}

	rows, err := descending()
	if err != nil {
		return nil, err
	}

	page := make([]Ascending, 0, len(rows))
	for i := range rows {
		page = append(page, same(rows[i]))
	}

	return page, nil
}

// pageFilter is the filter a paged read runs under: a copy of the caller's,
// normalized.
//
// The normalization is filtering's own, so a filter that did not arrive as query
// parameters is held to the same rule as one that did: an absent or zero page
// size becomes the shared default, an over-large one clamps to the shared
// ceiling, and an absent direction is ascending. A zero reaching the statement
// would be a page of no rows, which reads as an empty collection rather than as
// a caller who sent nothing.
//
// The copy matters. Normalize writes those defaults back onto the filter it is
// given, and a handler reusing one across two reads would otherwise find its
// page size rewritten underneath it.
//
// The one error Normalize returns is an unrecognized sort direction, and it is
// handed back rather than swallowed or raised. The filter comes back usable and
// ascending either way — which is the page this store would have answered with
// regardless, see filtering.QueryFilter.SortsDescending — so failing the read
// would deny a caller the rows over the one part of their request that had a
// sensible reading. The caller logs it instead.
func pageFilter(filter *filtering.QueryFilter) (*filtering.QueryFilter, error) {
	if filter == nil {
		filter = filtering.DefaultQueryFilter()
	}

	bounded := *filter

	return &bounded, bounded.Normalize()
}
