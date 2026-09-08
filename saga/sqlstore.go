package saga

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v14/saga/internal/queries"
	"github.com/primandproper/platform-go/v14/saga/internal/sagadb"
	"github.com/primandproper/platform-go/v14/saga/migrations"

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
)

// DefaultTablePrefix is the namespace the saga tables carry when none is
// configured, which is none — rendering saga_instances.
//
// The saga_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_saga_instances, for a database shared between applications. A namespace must
// not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger. It is deliberately not
// serviceName: a trace showing a saga advancing and the rows it moved wants
// those distinguishable, and one scope for both would make a store read look
// like a step execution in every span listing.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema saga/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
//
// Every statement it runs comes from saga/internal/queries, is checked by sqlc
// against that same schema on each of the three dialects, and is executed
// through the querier sqlc-gen-unison generated from it. There is no SQL in
// this file.
type SQLStore struct {
	client database.Client

	// q is the generated querier, instantiated for the client's dialect at the
	// configured prefix. It takes the executor per call, so a statement running
	// inside a caller's transaction is a different argument rather than a
	// different querier.
	q sagadb.Querier

	o11y observability.Observer

	guardMissCounter metrics.Int64Counter

	// guard is what a guarded write means in this package when it matches no
	// row. See internal/sqlguard.
	guard sqlguard.Guard

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
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
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger, traces to a noop provider, and counts into a noop meter.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "saga dialect %q", d)
	}

	s := &SQLStore{
		client: client,
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
	// substitution; see saga/internal/sagadb.
	qd, err := sagadbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := sagadb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the saga querier")
	}

	s.q = q

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One counter, and only one. The Worker owns the business totals — started,
	// advanced, compensated, stuck — and a second name for the same event is how
	// two dashboards come to disagree. What no caller can count is this: a
	// guarded write that matched no row. That is not a database error, and above
	// this layer it is indistinguishable from one.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.guardMissCounter, err = mp.NewInt64Counter(storeName + "_guard_misses"); err != nil {
		return nil, platformerrors.Wrap(err, "creating saga store guard miss counter")
	}

	s.guard = sqlguard.Guard{
		MissCounter: s.guardMissCounter,
		NotFound:    ErrInstanceNotFound,
		Namespace:   "saga",
		IDKey:       instanceIDKey,
		Message:     "saga instance left the active set before its progress could be recorded",
		Reason:      "saga instance %q is no longer advanceable",
	}

	return s, nil
}

// sagadbDialect maps this module's dialect names onto the generated package's.
// The set is closed on both sides — NewSQLStore has already rejected anything
// d.Valid() declines — so the default arm is reachable only when this module
// learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect rather than
// leaning on sagadb.New refusing the empty string.
func sagadbDialect(d dialect.Dialect) (sagadb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return sagadb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return sagadb.DialectMySQL, nil
	case dialect.SQLite:
		return sagadb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated saga queries for dialect %q", d)
	}
}

func (s *SQLStore) Save(ctx context.Context, q database.Tx, inst *Record, nextAttempt time.Time) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "saving saga instance")
	}

	if inst == nil {
		return op.Error(ErrNilInstance, "saving saga instance")
	}

	op.SetValues(map[string]any{
		instanceIDKey:  inst.ID,
		definitionKey:  inst.Definition,
		statusKey:      string(inst.Status),
		stepCountKey:   len(inst.StepNames),
		nextAttemptKey: nextAttempt,
	})

	if err := s.q.InsertSagaInstance(ctx, q, insertParams(inst, nextAttempt)); err != nil {
		return op.Error(err, "inserting saga instance")
	}

	return nil
}

func (s *SQLStore) Get(ctx context.Context, instanceID string) (*Record, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(instanceIDKey, instanceID))
	defer op.End()

	row, err := s.q.GetSagaInstance(ctx, s.client.Reader(), sagadb.GetSagaInstanceParams{ID: instanceID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Attached to the span but not logged as an error. An instance ID
			// that is not in the table is a 404 somebody is owed, not a fault of
			// this process, and painting the trace red for it buries the ones
			// that are.
			op.Set(guardMissedKey, true)

			return nil, platformerrors.Wrapf(ErrInstanceNotFound, "saga instance %q", instanceID)
		}

		return nil, op.Error(err, "reading saga instance")
	}

	inst, err := recordFromRow(&row)
	if err != nil {
		return nil, op.Error(err, "reading saga instance")
	}

	op.Set(statusKey, string(inst.Status)).Set(definitionKey, inst.Definition)

	return inst, nil
}

func (s *SQLStore) List(
	ctx context.Context,
	scope *ListScope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Record], error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if scope != nil {
		op.Set(definitionKey, scope.Definition).Set(statusKey, statusAttribute(scope.Statuses))
	}

	filter = pageFilter(filter)

	op.Set(limitKey, int(*filter.MaxResponseSize))

	rows, err := s.listRows(ctx, scope, filter)
	if err != nil {
		return nil, op.Error(err, "listing saga instances")
	}

	op.Set(resultCountKey, len(rows))

	// The counts ride on the rows, from the one snapshot the page was read
	// from, so an empty page has none to read — which filtering.Drain reports
	// as unknown rather than as zero.
	if len(rows) > 0 {
		op.Set(resultTotalKey, rows[0].total)
	}

	return filtering.Drain(rows, pageValue, pageCounts,
		func(r *Record) string { return r.ID }, filter), nil
}

// listRows runs whichever of the two listings the scope names, in whichever
// direction the filter asks for.
//
// The definition filter is a statement rather than an argument, because an
// optional equality is not a predicate a caller can relax: a listing asked for
// nothing in particular must not come back with the rows whose definition is
// the empty string. The status filter is an argument in both, because its
// domain is closed — see statusFilterValues.
func (s *SQLStore) listRows(
	ctx context.Context,
	scope *ListScope,
	filter *filtering.QueryFilter,
) ([]pageRow, error) {
	w := windowFrom(filter)
	statuses := statusFilterValues(scope)

	if scope != nil && scope.Definition != "" {
		params := sagadb.ListSagaInstancesByDefinitionParams{
			CreatedAfter:    w.createdAfter,
			CreatedBefore:   w.createdBefore,
			UpdatedAfter:    w.updatedAfter,
			UpdatedBefore:   w.updatedBefore,
			IncludeArchived: w.includeArchived,
			Status1:         statuses[0],
			Status2:         statuses[1],
			Status3:         statuses[2],
			Status4:         statuses[3],
			Status5:         statuses[4],
			Definition:      scope.Definition,
			PageCursor:      w.pageCursor,
			ResultLimit:     w.resultLimit,
		}

		got, err := sortedRows(filter,
			func() ([]sagadb.ListSagaInstancesByDefinitionRow, error) {
				return s.q.ListSagaInstancesByDefinition(ctx, s.client.Reader(), params)
			},
			func() ([]sagadb.ListSagaInstancesByDefinitionDescendingRow, error) {
				return s.q.ListSagaInstancesByDefinitionDescending(ctx, s.client.Reader(),
					sagadb.ListSagaInstancesByDefinitionDescendingParams(params))
			},
			func(r sagadb.ListSagaInstancesByDefinitionDescendingRow) sagadb.ListSagaInstancesByDefinitionRow {
				return sagadb.ListSagaInstancesByDefinitionRow(r)
			})
		if err != nil {
			return nil, err
		}

		return convertRows(got, instancePageRowByDefinition)
	}

	params := sagadb.ListSagaInstancesParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Status1:         statuses[0],
		Status2:         statuses[1],
		Status3:         statuses[2],
		Status4:         statuses[3],
		Status5:         statuses[4],
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}

	got, err := sortedRows(filter,
		func() ([]sagadb.ListSagaInstancesRow, error) {
			return s.q.ListSagaInstances(ctx, s.client.Reader(), params)
		},
		func() ([]sagadb.ListSagaInstancesDescendingRow, error) {
			return s.q.ListSagaInstancesDescending(ctx, s.client.Reader(),
				sagadb.ListSagaInstancesDescendingParams(params))
		},
		func(r sagadb.ListSagaInstancesDescendingRow) sagadb.ListSagaInstancesRow {
			return sagadb.ListSagaInstancesRow(r)
		})
	if err != nil {
		return nil, err
	}

	return convertRows(got, instancePageRow)
}

func (s *SQLStore) Claim(ctx context.Context, now time.Time, limit int, leaseUntil time.Time) ([]*Record, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	if limit <= 0 {
		return nil, nil
	}

	var (
		claimed  []*Record
		selected int
	)

	// The select and the update run in one transaction so that FOR UPDATE SKIP
	// LOCKED means anything. Without it the lock is released before the update,
	// and two workers select the same rows.
	err := s.client.WithTransaction(ctx, func(q database.Tx) error {
		ids, err := s.claimable(ctx, q, now, limit)
		if err != nil {
			return op.Error(err, "selecting claimable saga instances")
		}

		selected = len(ids)
		op.Set(selectedKey, selected)

		if len(ids) == 0 {
			return nil
		}

		lease := leaseUntil.UTC()
		stamp := now.UTC()

		affected, err := s.q.ClaimSagaInstances(ctx, q, sagadb.ClaimSagaInstancesParams{
			ClaimedUntil:       &lease,
			LastUpdatedAt:      &stamp,
			RunningStatus:      string(StatusRunning),
			CompensatingStatus: string(StatusCompensating),
			IDs:                ids,
		})
		if err != nil {
			return op.Error(err, "claiming saga instances")
		}

		op.Set(rowsAffectedKey, affected)

		// Re-read rather than project from the select, so the attempt counts the
		// worker sees are the ones the claim just wrote. A worker deciding
		// whether a step has exhausted its budget from a pre-increment count
		// would grant every step one attempt more than configured.
		rows, err := s.q.ListSagaInstancesByIDs(ctx, q, sagadb.ListSagaInstancesByIDsParams{
			RunningStatus:      string(StatusRunning),
			CompensatingStatus: string(StatusCompensating),
			IDs:                ids,
		})
		if err != nil {
			return op.Error(err, "reading claimed saga instances")
		}

		if claimed, err = convertRows(rows, recordFromBatchRow); err != nil {
			return op.Error(err, "reading claimed saga instances")
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	op.Set(claimedKey, len(claimed))

	// Selected as active, gone by the time the guarded UPDATE ran: another
	// worker's advance finished the saga in between. The claim repeats the
	// status guard for exactly this case, and without this line the batch would
	// simply come back smaller with nothing to say why.
	if selected != len(claimed) {
		op.Logger().WithValues(map[string]any{
			selectedKey: selected,
			claimedKey:  len(claimed),
		}).Info("saga instances left the claimable set mid-claim")
	}

	return claimed, nil
}

// claimable names the next batch of instances to lease: advanceable, due, and
// not currently held by another worker.
//
// The same instant answers both time comparisons, because one cycle asks one
// question about one moment: which instances are due *now*, and whose lease has
// lapsed *by now*.
func (s *SQLStore) claimable(ctx context.Context, q database.Tx, now time.Time, limit int) ([]string, error) {
	at := now.UTC()

	rows, err := s.q.ClaimableSagaInstanceIDs(ctx, q, sagadb.ClaimableSagaInstanceIDsParams{
		RunningStatus:      string(StatusRunning),
		CompensatingStatus: string(StatusCompensating),
		DueAt:              at,
		LeaseExpiredBy:     &at,
		ResultLimit:        int64(limit),
	})
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}

	return ids, nil
}

func (s *SQLStore) Advance(ctx context.Context, q database.Tx, inst *Record, nextAttempt, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "advancing saga instance")
	}

	if inst == nil {
		return op.Error(ErrNilInstance, "advancing saga instance")
	}

	op.SetValues(map[string]any{
		instanceIDKey:  inst.ID,
		statusKey:      string(inst.Status),
		stepIndexKey:   inst.CurrentStep,
		stateBytesKey:  len(inst.State),
		nextAttemptKey: nextAttempt,
	})

	params := advanceParams(inst, nextAttempt, at)

	// The lease is dropped whenever the pass is over: the instance is finished,
	// or it is waiting out a step's delay. Holding it through a delay shorter
	// than the lease would make the lease, not the delay, decide when the next
	// step runs. A mid-pass advance keeps it, because this worker is about to
	// run the next step itself.
	//
	// That is a SET list that differs by one assignment, so it is two checked
	// statements and this is the choice between them.
	if inst.Status.Terminal() || nextAttempt.After(at) {
		affected, err := s.q.AdvanceSagaInstanceAndClearLease(ctx, q,
			sagadb.AdvanceSagaInstanceAndClearLeaseParams(params))

		return s.guard.Count(ctx, op, affected, err, inst.ID, "advance", "advancing saga instance")
	}

	affected, err := s.q.AdvanceSagaInstance(ctx, q, params)

	return s.guard.Count(ctx, op, affected, err, inst.ID, "advance", "advancing saga instance")
}

func (s *SQLStore) Reschedule(
	ctx context.Context,
	instanceID string,
	attempts int,
	nextAttempt time.Time,
	lastErr string,
	at time.Time,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		instanceIDKey:  instanceID,
		attemptsKey:    attempts,
		nextAttemptKey: nextAttempt,
	}))
	defer op.End()

	stamp := at.UTC()

	affected, err := s.q.RescheduleSagaInstance(ctx, s.client.Writer(), sagadb.RescheduleSagaInstanceParams{
		Attempts:           int64(attempts),
		NextAttempt:        nextAttempt.UTC(),
		LastError:          lastErr,
		LastUpdatedAt:      &stamp,
		ID:                 instanceID,
		RunningStatus:      string(StatusRunning),
		CompensatingStatus: string(StatusCompensating),
	})

	return s.guard.Count(ctx, op, affected, err, instanceID, "reschedule", "rescheduling saga instance")
}

func (s *SQLStore) Release(ctx context.Context, instanceID string, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(instanceIDKey, instanceID))
	defer op.End()

	stamp := at.UTC()

	// The only write here that is not guarded, and the count is recorded rather
	// than acted on. Handing back a lease on an instance that has since finished
	// is not a lost race — there is nothing left to record — so a caller has
	// nothing to do about it, while an operator watching a worker shed its whole
	// batch does.
	affected, err := s.q.ReleaseSagaInstance(ctx, s.client.Writer(), sagadb.ReleaseSagaInstanceParams{
		LastUpdatedAt: &stamp,
		ID:            instanceID,
	})
	if err != nil {
		return op.Error(err, "releasing saga instance lease")
	}

	op.Set(rowsAffectedKey, affected)

	return nil
}

func (s *SQLStore) Requeue(
	ctx context.Context,
	instanceID string,
	from []Status,
	to Status,
	at time.Time,
) (*Record, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		instanceIDKey: instanceID,
		statusKey:     string(to),
		fromStatusKey: statusAttribute(from),
	}))
	defer op.End()

	if len(from) == 0 {
		return nil, op.Error(platformerrors.ErrEmptyInputParameter, "no source statuses for saga requeue")
	}

	stamp := at.UTC()

	// The guard is in the predicate rather than in a read-then-write, which is
	// what makes Resume safe against being clicked twice: the second writer
	// matches no rows and is told so, instead of both succeeding and handing two
	// workers the same half-compensated saga.
	affected, err := s.q.RequeueSagaInstance(ctx, s.client.Writer(), sagadb.RequeueSagaInstanceParams{
		Status:        string(to),
		NextAttempt:   stamp,
		LastUpdatedAt: &stamp,
		ID:            instanceID,
		FromStatuses:  statusStrings(from),
	})
	if err = s.guard.Count(ctx, op, affected, err, instanceID, "requeue", "requeuing saga instance"); err != nil {
		return nil, err
	}

	return s.Get(ctx, instanceID)
}

// WithTransaction delegates to the client, which begins its own span for the
// transaction. Wrapping it here would nest a second span around the first and
// say nothing the client's does not.
func (s *SQLStore) WithTransaction(ctx context.Context, fn func(q database.Tx) error) error {
	return s.client.WithTransaction(ctx, fn)
}

// convertRows turns a page of generated rows into whatever this package reads
// them as, failing on the first row it cannot.
//
// It is one loop rather than four because what differs between the reads is the
// conversion, which is the argument.
func convertRows[Row, T any](rows []Row, convert func(*Row) (T, error)) ([]T, error) {
	converted := make([]T, 0, len(rows))

	for i := range rows {
		value, err := convert(&rows[i])
		if err != nil {
			return nil, err
		}

		converted = append(converted, value)
	}

	return converted, nil
}

// listWindow is the filter window every listing binds, in the shape the
// generated params carry it. One reading of the filter, restated into each
// nominal params type by listRows.
type listWindow struct {
	createdAfter    *time.Time
	createdBefore   *time.Time
	updatedAfter    *time.Time
	updatedBefore   *time.Time
	pageCursor      *string
	resultLimit     int64
	includeArchived bool
}

// windowFrom reads the window off a page filter. The filter has been through
// pageFilter, so MaxResponseSize is set; only IncludeArchived defaults here,
// and it defaults to excluding, which is what the statement's COALESCE would
// have done with a NULL anyway — bound explicitly so the parameter is a bool
// rather than a pointer whose nil means the same thing.
//
// The UTC normalization on the four times is load-bearing on SQLite, where a
// timestamp column compares as text: the stored shape is UTC, and a bound value
// in any other zone compares against it with the wrong clock in the comparing
// prefix. The generated SQLite bindings format times into the stored shape
// themselves, so this is belt and braces rather than the only thing making it
// so — and it is also what makes what comes back match what a caller passed.
func windowFrom(filter *filtering.QueryFilter) listWindow {
	w := listWindow{
		createdAfter:  utcPtr(filter.CreatedAfter),
		createdBefore: utcPtr(filter.CreatedBefore),
		updatedAfter:  utcPtr(filter.UpdatedAfter),
		updatedBefore: utcPtr(filter.UpdatedBefore),
		pageCursor:    filter.Cursor,
		resultLimit:   int64(*filter.MaxResponseSize),
	}

	if filter.IncludeArchived != nil {
		w.includeArchived = *filter.IncludeArchived
	}

	return w
}

// pageFilter is the filter a paged read is answered under: the caller's, with
// the page-size ceiling every other paged read in this module applies.
//
// It works on a copy. The clamp has to be applied to what the query binds and
// to what the result reports, and doing that by writing through the caller's
// pointer would hand them back a filter they did not pass — a store that
// rewrites its argument is a store whose caller cannot reuse one.
//
// The sort direction passes through untouched, and is read where it is used:
// filtering.QueryFilter.SortsDescending answers an absent or unrecognized one
// ascending, which is the reading the shared filter applies everywhere.
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

// statusFilterValues renders a listing's status narrowing as the fixed number
// of arguments the statements bind.
//
// The domain is closed at five, so "any of these" is always five arguments and
// the store decides which five. A scope naming none of them is every status,
// which is the same rows an absent predicate would have returned; a scope
// naming fewer than five repeats one, and a duplicate in an IN list matches
// exactly the rows the original did.
//
// A scope naming only statuses this package does not write is the one case
// worth spelling out: it leaves every slot empty, and no row's status is the
// empty string, so it matches nothing — which is what asking for rows in a
// status that cannot exist should answer.
func statusFilterValues(scope *ListScope) [queries.StatusFilterArity]string {
	var bound [queries.StatusFilterArity]string

	if scope == nil || len(scope.Statuses) == 0 {
		for i, status := range allStatuses {
			bound[i] = string(status)
		}

		return bound
	}

	// Intersected with the domain rather than copied, which deduplicates the
	// caller's set and drops anything that is not a status — both of which are
	// what keeps the count at five.
	wanted := make([]Status, 0, len(allStatuses))
	for _, status := range allStatuses {
		if slices.Contains(scope.Statuses, status) {
			wanted = append(wanted, status)
		}
	}

	for i := range bound {
		if len(wanted) == 0 {
			break
		}

		bound[i] = string(wanted[min(i, len(wanted)-1)])
	}

	return bound
}

// statusStrings renders a status set as the []string a bound set binds through.
func statusStrings(statuses []Status) []string {
	rendered := make([]string, 0, len(statuses))
	for _, status := range statuses {
		rendered = append(rendered, string(status))
	}

	return rendered
}

// statusAttribute renders a status set for a span attribute. Spans take scalars
// and strings, not []Status, and the set a write guarded on is the first thing
// wanted when one of them matches nothing.
func statusAttribute(statuses []Status) string {
	return strings.Join(statusStrings(statuses), ",")
}
