package saga

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/internal/sqlguard"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/saga/migrations"
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
type SQLStore struct {
	client database.Client
	tables *tables
	o11y   observability.Observer

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
	dialect         dialect.Dialect
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
		client:  client,
		dialect: d,
		tables:  newTables(DefaultTablePrefix),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.tables.prefix()); err != nil {
		return nil, err
	}

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One counter, and only one. The Worker owns the business totals — started,
	// advanced, compensated, stuck — and a second name for the same event is how
	// two dashboards come to disagree. What no caller can count is this: a
	// guarded write that matched no row. That is not a database error, and above
	// this layer it is indistinguishable from one.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	var err error
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

func (s *SQLStore) Save(ctx context.Context, q database.SQLQueryExecutor, inst *Record, nextAttempt time.Time) error {
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

	query, args := s.tables.buildInsertInstance(s.dialect, inst, encodeStepNames(inst.StepNames), nextAttempt)

	if _, err := q.ExecContext(ctx, query, args...); err != nil {
		return op.Error(err, "inserting saga instance")
	}

	return nil
}

// encodeStepNames renders a step list for storage.
//
// It returns no error because it cannot produce one. json.Marshal fails on
// cycles, channels, funcs, and NaNs; a []string is none of those, and an error
// branch here would be one nothing can reach and no test can cover. The
// decode side does return an error, because a column can be edited by hand.
func encodeStepNames(names []string) []byte {
	//nolint:errcheck,errchkjson // a []string always marshals; see the comment above.
	encoded, _ := json.Marshal(names)

	return encoded
}

func (s *SQLStore) Get(ctx context.Context, instanceID string) (*Record, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(instanceIDKey, instanceID))
	defer op.End()

	query, args := s.tables.buildSelectInstance(s.dialect, instanceID)

	inst, err := scanInstance(s.client.Reader().QueryRowContext(ctx, query, args...))
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
		op.Set(definitionKey, scope.Definition).Set(statusKey, statusStrings(scope.Statuses))
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
	// filtering.DefaultQueryFilter asks for ascending, and a package that
	// quietly reversed it would make this the one list endpoint in the module
	// whose sort does not mean what the shared filter says it means.
	descending := filter.SortBy != nil && *filter.SortBy == *filtering.SortDescending

	op.Set(limitKey, limit)

	query, args := s.tables.buildListInstances(s.dialect, scope, cursor, limit, descending)

	instances, err := scanInstances(ctx, s.client.Reader(), query, args)
	if err != nil {
		return nil, op.Error(err, "listing saga instances")
	}

	countQuery, countArgs := s.tables.buildCountInstances(s.dialect, scope)

	var total uint64
	if err = s.client.Reader().QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, op.Error(err, "counting saga instances")
	}

	op.Set(resultCountKey, len(instances)).Set(resultTotalKey, total)

	return filtering.NewQueryFilteredResult(
		instances, uint64(len(instances)), total,
		func(r *Record) string { return r.ID },
		filter,
	), nil
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
	err := s.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		selectQuery, selectArgs := s.tables.buildSelectClaimable(s.dialect, now, limit, true)

		ids, err := scanIDs(ctx, q, selectQuery, selectArgs)
		if err != nil {
			return op.Error(err, "selecting claimable saga instances")
		}

		selected = len(ids)
		op.Set(selectedKey, selected)

		if len(ids) == 0 {
			return nil
		}

		claimQuery, claimArgs := s.tables.buildClaim(s.dialect, ids, leaseUntil, now)
		if _, err = q.ExecContext(ctx, claimQuery, claimArgs...); err != nil {
			return op.Error(err, "claiming saga instances")
		}

		// Re-read rather than project from the select, so the attempt counts the
		// worker sees are the ones the claim just wrote. A worker deciding
		// whether a step has exhausted its budget from a pre-increment count
		// would grant every step one attempt more than configured.
		fetchQuery, fetchArgs := s.tables.buildFetchByIDs(s.dialect, ids)

		if claimed, err = scanInstances(ctx, q, fetchQuery, fetchArgs); err != nil {
			return op.Error(err, "reading claimed saga instances")
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	op.Set(claimedKey, len(claimed))

	// Selected as active, gone by the time the guarded UPDATE ran: another
	// worker's advance finished the saga in between. buildClaim repeats the
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

func (s *SQLStore) Advance(ctx context.Context, q database.SQLQueryExecutor, inst *Record, nextAttempt time.Time) error {
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

	query, args := s.tables.buildAdvance(s.dialect, inst, nextAttempt, inst.UpdatedAt)

	return s.guard.Exec(ctx, op, q, query, args, inst.ID, "advance", "advancing saga instance")
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

	query, args := s.tables.buildReschedule(s.dialect, instanceID, attempts, nextAttempt, lastErr, at)

	return s.guard.Exec(ctx, op, s.client.Writer(), query, args, instanceID, "reschedule", "rescheduling saga instance")
}

func (s *SQLStore) Release(ctx context.Context, instanceID string, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(instanceIDKey, instanceID))
	defer op.End()

	query, args := s.tables.buildRelease(s.dialect, instanceID, at)

	if _, err := s.client.Writer().ExecContext(ctx, query, args...); err != nil {
		return op.Error(err, "releasing saga instance lease")
	}

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
		fromStatusKey: statusStrings(from),
	}))
	defer op.End()

	if len(from) == 0 {
		return nil, op.Error(platformerrors.ErrEmptyInputParameter, "no source statuses for saga requeue")
	}

	query, args := s.tables.buildRequeue(s.dialect, instanceID, from, to, at)

	result, err := s.client.Writer().ExecContext(ctx, query, args...)
	if err != nil {
		return nil, op.Error(err, "requeuing saga instance")
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return nil, op.Error(err, "reading saga requeue result")
	}

	op.Set(rowsAffectedKey, affected)

	if affected == 0 {
		// The guard in the predicate did its job and nothing moved: the instance
		// is gone, or somebody resumed it a moment ago. Counted rather than
		// logged as an error, because from here the two are indistinguishable
		// and it is the caller that knows whether losing this race matters.
		op.Set(guardMissedKey, true)
		s.guardMissCounter.Add(ctx, 1, s.guard.OpAttr("requeue"))

		return nil, platformerrors.Wrapf(ErrInstanceNotFound, "saga instance %q in expected status", instanceID)
	}

	return s.Get(ctx, instanceID)
}

// WithTransaction delegates to the client, which begins its own span for the
// transaction. Wrapping it here would nest a second span around the first and
// say nothing the client's does not.
func (s *SQLStore) WithTransaction(ctx context.Context, fn func(q database.SQLQueryExecutor) error) error {
	return s.client.WithTransaction(ctx, fn)
}

// statusStrings renders a status set for a span attribute. Spans take scalars
// and strings, not []Status, and the set a write guarded on is the first thing
// wanted when one of them matches nothing.
func statusStrings(statuses []Status) string {
	rendered := make([]string, 0, len(statuses))
	for _, status := range statuses {
		rendered = append(rendered, string(status))
	}

	return strings.Join(rendered, ",")
}

// scanIDs drains a single-column ID projection.
func scanIDs(ctx context.Context, q database.SQLQueryExecutor, query string, args []any) ([]string, error) {
	return database.ScanStrings(ctx, q, "saga instance ID", query, args)
}

// scanInstances drains an instance projection.
func scanInstances(ctx context.Context, q database.SQLQueryExecutor, query string, args []any) ([]*Record, error) {
	return database.ScanAll(ctx, q, "saga instance", query, args, scanInstance)
}

// scanInstance reads one row of instanceColumns.
func scanInstance(scanner database.Scanner) (*Record, error) {
	var (
		inst         Record
		status       string
		resumeStatus string
		stepNames    string
		state        []byte
		lastError    sql.NullString
	)

	if err := scanner.Scan(
		&inst.ID, &inst.Definition, &status, &inst.CurrentStep, &stepNames, &state,
		&inst.Attempts, &lastError, &resumeStatus, &inst.StartedAt, &inst.UpdatedAt,
	); err != nil {
		return nil, err
	}

	inst.Status = Status(status)
	inst.ResumeStatus = Status(resumeStatus)
	inst.LastError = database.StringFromNullString(lastError)
	inst.StartedAt = inst.StartedAt.UTC()
	inst.UpdatedAt = inst.UpdatedAt.UTC()

	if len(state) > 0 {
		// Copied out of the driver's buffer. database/sql reuses the byte slice
		// backing a []byte destination across Next calls, so a claimed batch
		// would otherwise come back with every instance holding the last row's
		// state.
		inst.State = json.RawMessage(append([]byte(nil), state...))
	}

	if err := json.Unmarshal([]byte(stepNames), &inst.StepNames); err != nil {
		return nil, platformerrors.Wrap(err, "decoding saga step names")
	}

	return &inst, nil
}
