package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/charset"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/internal/sqlguard"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/operations/migrations"
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
type SQLStore struct {
	client database.Client
	tables *tables
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
func NewSQLStore(client database.Client, opts ...StoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if err := dialect.RequirePostgres("operations", client.Dialect()); err != nil {
		return nil, err
	}

	s := &SQLStore{client: client, tables: newTables(DefaultTablePrefix)}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.tables.prefix()); err != nil {
		return nil, err
	}

	// The channel is bound as text by the statement this package emits, but the
	// listener on the other end has to render it into a LISTEN, which takes no
	// parameters. Vetting it here is what keeps that end from having to.
	if s.notifyChannel != "" && !dialect.ValidIdentifier(s.notifyChannel) {
		return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier,
			"operations notify channel %q", s.notifyChannel)
	}

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	var err error

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

func (s *SQLStore) Insert(ctx context.Context, q database.SQLQueryExecutor, op *Operation) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx)
	defer span.End()

	if q == nil {
		return nil, span.Error(ErrNilExecutor, "inserting operation")
	}

	if op == nil {
		return nil, span.Error(ErrNilOperation, "inserting operation")
	}

	span.SetValues(map[string]any{
		operationIDKey: op.ID,
		kindKey:        op.Kind,
		ownerKey:       op.Owner,
	})

	query, args := s.tables.buildInsert(&insertRow{
		id:         op.ID,
		kind:       op.Kind,
		owner:      op.Owner,
		countLabel: op.Progress.CountLabel,
		request:    op.Request,
	})

	inserted, err := scanOperation(q.QueryRowContext(ctx, query, args...))
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
	return inserted, nil
}

func (s *SQLStore) Get(ctx context.Context, id string) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(operationIDKey, id))
	defer span.End()

	query, args := s.tables.buildSelect(id)

	op, err := scanOperation(s.client.Reader().QueryRowContext(ctx, query, args...))
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			// Attached to the span but not logged as an error. An operation ID
			// that is not in the table is a 404 somebody is owed, not a fault of
			// this process, and painting the trace red for it buries the ones
			// that are.
			span.Set(guardMissedKey, true)

			return nil, platformerrors.Wrapf(ErrOperationNotFound, "operation %q", id)
		}

		return nil, span.Error(err, "reading operation")
	}

	span.Set(stateKey, string(op.State)).Set(kindKey, op.Kind).Set(revisionKey, op.Revision)

	return op, nil
}

func (s *SQLStore) GetMany(ctx context.Context, ids []string) ([]*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(batchKey, len(ids)))
	defer span.End()

	if len(ids) == 0 {
		return nil, nil
	}

	query, args := s.tables.buildSelectMany(ids)

	ops, err := scanOperations(ctx, s.client.Reader(), query, args)
	if err != nil {
		return nil, span.Error(err, "reading operations")
	}

	span.Set(resultCountKey, len(ops))

	return ops, nil
}

func (s *SQLStore) List(
	ctx context.Context,
	scope *ListScope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Operation], error) {
	ctx, span := s.o11y.Begin(ctx)
	defer span.End()

	if scope != nil {
		span.SetValues(map[string]any{
			ownerKey: scope.Owner,
			kindKey:  scope.Kind,
			stateKey: stateStrings(scope.States),
		})
	}

	if filter == nil {
		filter = filtering.DefaultQueryFilter()
	}

	limit := int(filtering.DefaultQueryFilterLimit)
	if filter.MaxResponseSize != nil && *filter.MaxResponseSize > 0 {
		limit = int(*filter.MaxResponseSize)
	}

	var cursor string
	if filter.Cursor != nil {
		cursor = *filter.Cursor
	}

	// Ordering follows the filter rather than a package-local preference.
	// filtering.DefaultQueryFilter asks for ascending, and a package that quietly
	// reversed it would make this the one list endpoint in the module whose sort
	// does not mean what the shared filter says it means.
	descending := filter.SortBy != nil && *filter.SortBy == *filtering.SortDescending

	span.Set(limitKey, limit)

	query, args := s.tables.buildList(scope, cursor, limit, descending)

	ops, err := scanOperations(ctx, s.client.Reader(), query, args)
	if err != nil {
		return nil, span.Error(err, "listing operations")
	}

	countQuery, countArgs := s.tables.buildCount(scope)

	var total uint64
	if err = s.client.Reader().QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, span.Error(err, "counting operations")
	}

	span.Set(resultCountKey, len(ops)).Set(resultTotalKey, total)

	return filtering.NewQueryFilteredResult(
		ops, uint64(len(ops)), total,
		func(o *Operation) string { return o.ID },
		filter,
	), nil
}

func (s *SQLStore) Begin(ctx context.Context, id string, attempts int, lease time.Duration) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		operationIDKey: id,
		attemptsKey:    attempts,
	}))
	defer span.End()

	query, args := s.tables.buildBegin(id, attempts, lease.Microseconds())

	op, err := scanOperation(s.client.Writer().QueryRowContext(ctx, query, args...))
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

	query, args := s.tables.buildProgress(id, progressRow{
		unitsTotal: progress.UnitsTotal,
		unitsDone:  progress.UnitsDone,
		unit:       charset.TruncateUTF8(progress.Unit, MaxMessageLength),
		count:      progress.Count,
		message:    charset.TruncateUTF8(progress.Message, MaxMessageLength),
	}, lease.Microseconds())

	var ack Ack

	err := s.client.Writer().QueryRowContext(ctx, query, args...).Scan(&ack.CancelRequested, &ack.Revision)
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

	ack.Held = true

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

	if opErr != nil {
		truncated := *opErr
		truncated.Message = charset.TruncateUTF8(truncated.Message, MaxMessageLength)
		opErr = &truncated
	}

	query, args := s.tables.buildFinish(finishRow{
		id:           id,
		state:        state,
		result:       result,
		opErr:        opErr,
		unitsAllDone: unitsAllDone,
	})

	return s.execExpectingRow(ctx, span, query, args, id, "finish", "finishing operation")
}

func (s *SQLStore) Release(ctx context.Context, id string, opErr *Error) error {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(operationIDKey, id))
	defer span.End()

	var code, message string
	if opErr != nil {
		code, message = opErr.Code, charset.TruncateUTF8(opErr.Message, MaxMessageLength)
	}

	query, args := s.tables.buildRelease(id, code, message)

	return s.execExpectingRow(ctx, span, query, args, id, "release", "releasing operation")
}

func (s *SQLStore) RequestCancel(ctx context.Context, id string) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(operationIDKey, id))
	defer span.End()

	query, args := s.tables.buildRequestCancel(id)

	result, err := s.client.Writer().ExecContext(ctx, query, args...)
	if err != nil {
		return nil, span.Error(err, "requesting operation cancellation")
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return nil, span.Error(err, "reading operation cancellation result")
	}

	span.Set(rowsAffectedKey, affected)

	if affected > 0 {
		s.notify(ctx)
	}

	// Read back either way. Zero rows means the operation was already terminal,
	// which is not a failure — the caller wanted it not running and it is not
	// running — so the answer is the row as it stands, and Get is what reports a
	// genuinely absent operation.
	return s.Get(ctx, id)
}

func (s *SQLStore) Stranded(ctx context.Context, grace time.Duration, limit int) ([]*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer span.End()

	if limit <= 0 {
		return nil, nil
	}

	query, args := s.tables.buildSelectStranded(grace.Microseconds(), limit)

	ops, err := scanOperations(ctx, s.client.Reader(), query, args)
	if err != nil {
		return nil, span.Error(err, "reading stranded operations")
	}

	span.Set(resultCountKey, len(ops))

	return ops, nil
}

func (s *SQLStore) Reap(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer span.End()

	if limit <= 0 {
		return 0, nil
	}

	query, args := s.tables.buildReap(retention.Microseconds(), limit)

	result, err := s.client.Writer().ExecContext(ctx, query, args...)
	if err != nil {
		return 0, span.Error(err, "reaping operations")
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, span.Error(err, "reading operation reap result")
	}

	span.Set(rowsAffectedKey, affected)

	return affected, nil
}

// WithTransaction delegates to the client, which begins its own span for the
// transaction. Wrapping it here would nest a second span around the first and
// say nothing the client's does not.
func (s *SQLStore) WithTransaction(ctx context.Context, fn func(q database.SQLQueryExecutor) error) error {
	return s.client.WithTransaction(ctx, fn)
}

// notify wakes whatever is watching, after the row has landed.
//
// It is best-effort by design and its failure is counted rather than returned.
// A notification carries no information — see the package documentation — so a
// lost one costs a watcher its poll interval and costs the operation nothing.
// Failing a progress flush because a notification did not go out would trade
// something that matters for something that does not.
func (s *SQLStore) notify(ctx context.Context) {
	if s.notifyChannel == "" {
		return
	}

	if _, err := s.client.Writer().ExecContext(ctx, dialect.PostgresNotifyStatement, s.notifyChannel); err != nil {
		s.notifyCounter.Add(ctx, 1)
		s.o11y.Logger().WithValue(notifyKey, s.notifyChannel).Error("notifying operations channel", err)
	}
}

// execExpectingRow runs a guarded UPDATE and wakes the watchers when it lands.
//
// The guard's distinction matters more here than it looks. A finish that matches
// no rows means the operation left the active set while the Runner was working —
// finished by another worker after a lease lapsed, or cancelled outright — and
// treating that as success would have the worker report a result the database
// never recorded, to a client that will poll the row and see something else.
func (s *SQLStore) execExpectingRow(
	ctx context.Context,
	span observability.Operation,
	query string,
	args []any,
	id, operation, description string,
) error {
	if err := s.guard.Exec(ctx, span, s.client.Writer(), query, args, id, operation, description); err != nil {
		return err
	}

	s.notify(ctx)

	return nil
}

// stateStrings renders a state set for a span attribute. Spans take scalars and
// strings, not []State, and the set a read was scoped to is the first thing
// wanted when it comes back empty.
func stateStrings(states []State) string {
	rendered := make([]string, 0, len(states))
	for _, state := range states {
		rendered = append(rendered, string(state))
	}

	return strings.Join(rendered, ",")
}

// scanOperations drains an operation projection.
func scanOperations(
	ctx context.Context,
	q database.SQLQueryExecutor,
	query string,
	args []any,
) ([]*Operation, error) {
	return database.ScanAll(ctx, q, "operation", query, args, scanOperation)
}

// scanOperation reads one row of operationColumns.
//
// Result and Error are built here rather than stored as encoded structs, and
// each is built only in the state it means something in. A succeeded operation
// carrying an Error left over from a retried attempt, or a failed one carrying
// a half-written Result, would be a row that contradicts itself — and the
// contradiction would reach every client.
func scanOperation(scanner database.Scanner) (*Operation, error) {
	var (
		op             Operation
		state          string
		request        []byte
		resultDetail   []byte
		unitsTotal     sql.NullInt64
		resultURI      string
		errorCode      string
		errorMessage   string
		errorRetryable bool
		startedAt      sql.NullTime
		finishedAt     sql.NullTime
	)

	if err := scanner.Scan(
		&op.ID, &op.Kind, &state, &op.Owner, &request,
		&unitsTotal, &op.Progress.UnitsDone, &op.Progress.Unit, &op.Progress.Count,
		&op.Progress.CountLabel, &op.Progress.Message,
		&resultURI, &resultDetail, &errorCode, &errorMessage, &errorRetryable,
		&op.Revision, &op.Attempts, &op.CancelRequested, &op.CreatedAt, &op.UpdatedAt,
		&startedAt, &finishedAt,
	); err != nil {
		return nil, err
	}

	op.State = State(state)
	op.Done = op.State.Terminal()
	op.CreatedAt = op.CreatedAt.UTC()
	op.UpdatedAt = op.UpdatedAt.UTC()

	if unitsTotal.Valid {
		total := int(unitsTotal.Int64)
		op.Progress.UnitsTotal = &total
	}

	if startedAt.Valid {
		at := startedAt.Time.UTC()
		op.StartedAt = &at
	}

	if finishedAt.Valid {
		at := finishedAt.Time.UTC()
		op.FinishedAt = &at
	}

	// Copied out of the driver's buffer. database/sql reuses the byte slice
	// backing a []byte destination across Next calls, so a batch read would
	// otherwise come back with every operation holding the last row's request.
	if len(request) > 0 {
		op.Request = json.RawMessage(append([]byte(nil), request...))
	}

	if op.State == StateSucceeded && (resultURI != "" || len(resultDetail) > 0) {
		op.Result = &Result{URI: resultURI}
		if len(resultDetail) > 0 {
			op.Result.Detail = json.RawMessage(append([]byte(nil), resultDetail...))
		}
	}

	if op.State == StateFailed && (errorCode != "" || errorMessage != "") {
		op.Error = &Error{Code: errorCode, Message: errorMessage, Retryable: errorRetryable}
	}

	return &op, nil
}
