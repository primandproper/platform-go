package metering

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/sqlguard"
	"github.com/primandproper/platform-go/v13/metering/migrations"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// DefaultTablePrefix is the namespace the metering tables carry when none is
// configured, which is none — rendering metering_events and metering_totals.
//
// The metering_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_metering_events, for a database shared between applications. A namespace must
// not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger. It is deliberately not
// serviceName: a trace showing the enforcement decision and the rows behind it
// wants those distinguishable, and one scope for both would make a total read
// look like a Check in every span listing.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema metering/migrations
// renders.
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
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "metering dialect %q", d)
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

	// One counter, and only one. The Recorder, Enforcer, and Flusher own the
	// business totals — recorded, allowed, denied, flushed — and a second name for
	// the same event is how two dashboards come to disagree. What no caller can
	// count is this: a guarded write that matched no row. That is not a database
	// error, and above this layer it is indistinguishable from one.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	var err error
	if s.guardMissCounter, err = mp.NewInt64Counter(storeName + "_guard_misses"); err != nil {
		return nil, platformerrors.Wrap(err, "creating metering store guard miss counter")
	}

	// The meter reaches the line here where it did not before: a total that
	// moved on is one row among a deployment's many, and a line that named none
	// of them left an operator nothing to look the total up by.
	s.guard = sqlguard.Guard{
		MissCounter: s.guardMissCounter,
		Namespace:   "metering",
		IDKey:       meterKey,
		Message:     "metering total moved on before its flush could be settled",
		Reason:      "metering total for meter %q is no longer at the expected flush sequence",
	}

	return s, nil
}

func (s *SQLStore) Record(ctx context.Context, entries []Entry, at time.Time) (RecordResult, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(batchSizeKey, len(entries)))
	defer op.End()

	if len(entries) == 0 {
		return RecordResult{}, nil
	}

	var result RecordResult

	// One transaction for the whole batch, so a crash mid-batch leaves neither
	// half-counted events nor a total folded from records whose ledger rows never
	// landed. The ledger and the aggregate it feeds are one fact.
	err := s.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		var txErr error
		result, txErr = s.record(ctx, op, q, entries, at)

		return txErr
	})
	if err != nil {
		return RecordResult{}, err
	}

	op.Set(acceptedKey, result.Accepted).Set(duplicateKey, result.Duplicates)

	return result, nil
}

func (s *SQLStore) RecordTx(
	ctx context.Context,
	q database.SQLQueryExecutor,
	entries []Entry,
	at time.Time,
) (RecordResult, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(batchSizeKey, len(entries)))
	defer op.End()

	if q == nil {
		return RecordResult{}, op.Error(ErrNilExecutor, "recording metering usage")
	}

	if len(entries) == 0 {
		return RecordResult{}, nil
	}

	result, err := s.record(ctx, op, q, entries, at)
	if err != nil {
		return RecordResult{}, err
	}

	op.Set(acceptedKey, result.Accepted).Set(duplicateKey, result.Duplicates)

	return result, nil
}

// record is the shared body of Record and RecordTx: dedupe every entry against
// the ledger, then fold what survived into its period's total.
func (s *SQLStore) record(
	ctx context.Context,
	op observability.Operation,
	q database.SQLQueryExecutor,
	entries []Entry,
	at time.Time,
) (RecordResult, error) {
	var (
		result   RecordResult
		accepted = make([]Entry, 0, len(entries))
	)

	for i := range entries {
		entry := &entries[i]

		inserted, err := s.insertEvent(ctx, q, entry, at)
		if err != nil {
			return RecordResult{}, op.Error(err, "recording metering usage event")
		}

		if !inserted {
			result.Duplicates++

			continue
		}

		accepted = append(accepted, *entry)
	}

	result.Accepted = len(accepted)

	// Grouped, so a thousand records for one subject and period cost one total
	// update rather than a thousand. The in-process fold and the SQL fold are the
	// same function, so the grouping cannot change the answer.
	groups := groupEntries(accepted)
	for i := range groups {
		group := &groups[i]

		query, args := s.tables.buildUpsertTotal(
			s.dialect, group.subject, group.meter, group.aggregation,
			group.bounds, group.quantity, group.lastOccurredAt, at,
		)

		if _, err := q.ExecContext(ctx, query, args...); err != nil {
			return RecordResult{}, op.Error(err, "folding metering usage into its total")
		}
	}

	return result, nil
}

// insertEvent writes one ledger row, reporting whether it was new.
// eventExists reports whether this entry's (meter, idempotency_key) is already
// in the ledger.
func (s *SQLStore) eventExists(
	ctx context.Context,
	q database.SQLQueryExecutor,
	entry *Entry,
) (bool, error) {
	query, args := s.tables.buildEventExists(s.dialect, entry.Meter, entry.IdempotencyKey)

	var found int
	switch err := q.QueryRowContext(ctx, query, args...).Scan(&found); {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

func (s *SQLStore) insertEvent(
	ctx context.Context,
	q database.SQLQueryExecutor,
	entry *Entry,
	at time.Time,
) (bool, error) {
	dimensions, err := encodeDimensions(entry.Dimensions)
	if err != nil {
		return false, err
	}

	query, args := s.tables.buildInsertEvent(s.dialect, entry, dimensions, at)

	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}

	return affected > 0, nil
}

func (s *SQLStore) Total(ctx context.Context, subject, meter string, bounds Bounds) (*Total, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		subjectKey:     subject,
		meterKey:       meter,
		periodStartKey: bounds.Start,
		periodEndKey:   bounds.End,
	}))
	defer op.End()

	query, args := s.tables.buildSelectTotal(s.dialect, subject, meter, bounds.Start, false)

	total, err := scanTotal(s.client.Reader().QueryRowContext(ctx, query, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// An absent row is a number, not a missing value: nothing recorded
			// means nothing used. Returning an error here would make every read
			// path branch on the ordinary case of a period that has just begun.
			return &Total{
				Subject:     subject,
				Meter:       meter,
				PeriodStart: bounds.Start.UTC(),
				PeriodEnd:   bounds.End.UTC(),
			}, nil
		}

		return nil, op.Error(err, "reading metering total")
	}

	op.Set(usedKey, total.Quantity)

	return total, nil
}

//nolint:gocritic // hugeParam: Entry is taken by value to match Store.Consume's interface
func (s *SQLStore) Consume(
	ctx context.Context,
	entry Entry,
	limit int64,
	behavior QuotaBehavior,
	at time.Time,
) (*Decision, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		subjectKey:     entry.Subject,
		meterKey:       entry.Meter,
		quantityKey:    entry.Quantity,
		limitKey:       limit,
		behaviorKey:    string(behavior),
		periodStartKey: entry.Bounds.Start,
		periodEndKey:   entry.Bounds.End,
		aggregationKey: string(entry.Aggregation),
	}))
	defer op.End()

	var decision *Decision

	err := s.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		var txErr error
		decision, txErr = s.consume(ctx, op, q, &entry, limit, behavior, at)

		return txErr
	})
	if err != nil {
		return nil, err
	}

	op.SetValues(map[string]any{
		allowedKey:   decision.Allowed,
		usedKey:      decision.Used,
		overageKey:   decision.Overage,
		duplicateKey: decision.Duplicate,
	})

	return decision, nil
}

// consume is the serialized decide-then-record that makes Enforcer.Consume
// exact.
//
// The order is load-bearing. The zero row is inserted first so there is something
// to lock; the lock is taken before the total is read, so the number decided
// against is the committed one; the decision is made before the ledger row is
// written, so a refused consume does not burn its idempotency key on usage it
// never recorded; and the total is only folded once the ledger row proves the
// usage is new.
func (s *SQLStore) consume(
	ctx context.Context,
	op observability.Operation,
	q database.SQLQueryExecutor,
	entry *Entry,
	limit int64,
	behavior QuotaBehavior,
	at time.Time,
) (*Decision, error) {
	zeroQuery, zeroArgs := s.tables.buildInsertZeroTotal(
		s.dialect, entry.Subject, entry.Meter, entry.Aggregation, entry.Bounds, at,
	)

	if _, err := q.ExecContext(ctx, zeroQuery, zeroArgs...); err != nil {
		return nil, op.Error(err, "opening metering total")
	}

	selectQuery, selectArgs := s.tables.buildSelectTotal(
		s.dialect, entry.Subject, entry.Meter, entry.Bounds.Start, true,
	)

	total, err := scanTotal(q.QueryRowContext(ctx, selectQuery, selectArgs...))
	if err != nil {
		return nil, op.Error(err, "locking metering total")
	}

	newer := !entry.OccurredAt.Before(total.LastOccurredAt)
	projected := entry.Aggregation.Fold(total.Quantity, entry.Quantity, newer)

	decision := newDecision(entry.Meter, behavior, projected, limit, entry.Bounds.End)

	if !decision.Allowed && !behavior.records() {
		// About to refuse — but first, is this a retry of a consume that already
		// succeeded? The projection above added this entry's quantity to a total
		// that already includes it, so a retry near the limit projects over it and
		// is refused, telling the caller their already-counted usage was denied.
		//
		// The probe is read-only: the refusal path must still write nothing, since
		// burning the idempotency key on a consume that recorded nothing would make
		// the caller's next retry look like a duplicate and be answered with a
		// total that never included their usage.
		counted, probeErr := s.eventExists(ctx, q, entry)
		if probeErr != nil {
			return nil, op.Error(probeErr, "probing metering dedupe")
		}

		if counted {
			decision.Duplicate = true
			decision.Allowed = true
			decision.Used = total.Quantity
			decision.Overage = overageOf(total.Quantity, limit)

			return decision, nil
		}

		decision.Used = total.Quantity
		decision.Overage = overageOf(total.Quantity, limit)

		return decision, nil
	}

	inserted, err := s.insertEvent(ctx, q, entry, at)
	if err != nil {
		return nil, op.Error(err, "recording metering usage event")
	}

	if !inserted {
		// Already counted under this key. The decision reports the true current
		// total rather than the projected one, which is what a retried request
		// should see: its usage is in there already.
		decision.Duplicate = true
		decision.Used = total.Quantity
		decision.Overage = overageOf(total.Quantity, limit)

		return decision, nil
	}

	applyQuery, applyArgs := s.tables.buildApplyConsume(
		s.dialect, entry.Subject, entry.Meter, entry.Bounds.Start, projected, entry.OccurredAt, at,
	)

	if _, err = q.ExecContext(ctx, applyQuery, applyArgs...); err != nil {
		return nil, op.Error(err, "applying metering consume")
	}

	return decision, nil
}

func (s *SQLStore) ClaimFlushable(
	ctx context.Context,
	now time.Time,
	limit, maxAttempts int,
	leaseUntil time.Time,
) ([]*Total, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	if limit <= 0 {
		return nil, nil
	}

	var (
		claimed  []*Total
		selected int
	)

	// The select and the update run in one transaction so that FOR UPDATE SKIP
	// LOCKED means anything. Without it the lock is released before the update,
	// and two flushers select the same totals.
	err := s.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		selectQuery, selectArgs := s.tables.buildSelectFlushable(s.dialect, now, limit, maxAttempts, true)

		keys, keyErr := scanTotalKeys(ctx, q, selectQuery, selectArgs)
		if keyErr != nil {
			return op.Error(keyErr, "selecting flushable metering totals")
		}

		selected = len(keys)

		if selected == 0 {
			return nil
		}

		claimQuery, claimArgs := s.tables.buildClaimFlushable(s.dialect, keys, leaseUntil)
		if _, execErr := q.ExecContext(ctx, claimQuery, claimArgs...); execErr != nil {
			return op.Error(execErr, "claiming flushable metering totals")
		}

		// Re-read rather than project from the select, so the attempt counts the
		// flusher sees are the ones the claim just wrote. A flusher deciding
		// whether it has exhausted its budget from a pre-increment count would
		// grant every total one attempt more than configured.
		fetchQuery, fetchArgs := s.tables.buildFetchTotalsByKey(s.dialect, keys)

		var fetchErr error
		if claimed, fetchErr = scanTotals(ctx, q, fetchQuery, fetchArgs); fetchErr != nil {
			return op.Error(fetchErr, "reading claimed metering totals")
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	op.Set(resultCountKey, len(claimed))

	// A flush pass that keeps coming back full is a pass that is not keeping up,
	// and what it is failing to post is revenue.
	if len(claimed) == limit {
		op.Logger().WithValue(limitKey, limit).
			Info("metering flush filled its batch; usage may be accumulating faster than it is flushed")
	}

	return claimed, nil
}

func (s *SQLStore) MarkFlushed(ctx context.Context, total *Total, flushed int64, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if total == nil {
		return op.Error(platformerrors.ErrNilInputParameter, "marking metering total flushed")
	}

	op.SetValues(map[string]any{
		subjectKey:     total.Subject,
		meterKey:       total.Meter,
		sequenceKey:    total.FlushSequence,
		flushedKey:     flushed,
		periodStartKey: total.PeriodStart,
		periodEndKey:   total.PeriodEnd,
		aggregationKey: string(total.Aggregation),
	})

	query, args := s.tables.buildMarkFlushed(s.dialect, total, flushed, at)

	return s.guard.Exec(ctx, op, s.client.Writer(), query, args, total.Meter, "mark_flushed",
		"marking metering total flushed")
}

func (s *SQLStore) ReleaseFlush(ctx context.Context, total *Total, lastErr string, nextFlush time.Time) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if total == nil {
		return op.Error(platformerrors.ErrNilInputParameter, "releasing metering flush lease")
	}

	op.SetValues(map[string]any{
		subjectKey:     total.Subject,
		meterKey:       total.Meter,
		sequenceKey:    total.FlushSequence,
		periodStartKey: total.PeriodStart,
		periodEndKey:   total.PeriodEnd,
		aggregationKey: string(total.Aggregation),
	})

	query, args := s.tables.buildReleaseFlush(s.dialect, total, lastErr, nextFlush, nextFlush)

	return s.guard.Exec(ctx, op, s.client.Writer(), query, args, total.Meter, "release_flush",
		"releasing metering flush lease")
}

func (s *SQLStore) ReapEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	if limit <= 0 {
		return 0, nil
	}

	query, args := s.tables.buildReapEvents(s.dialect, before, limit)

	result, err := s.client.Writer().ExecContext(ctx, query, args...)
	if err != nil {
		return 0, op.Error(err, "reaping metering usage events")
	}

	reaped, err := result.RowsAffected()
	if err != nil {
		return 0, op.Error(err, "reading reaped metering usage event count")
	}

	op.Set(reapedKey, reaped)

	return reaped, nil
}

// WithTransaction delegates to the client, which begins its own span for the
// transaction. Wrapping it here would nest a second span around the first and say
// nothing the client's does not.
func (s *SQLStore) WithTransaction(ctx context.Context, fn func(q database.SQLQueryExecutor) error) error {
	return s.client.WithTransaction(ctx, fn)
}

// entryGroup is one period's worth of accepted records for one subject and meter,
// already folded down to a single contribution.
type entryGroup struct {
	bounds         Bounds
	lastOccurredAt time.Time
	subject        string
	meter          string
	aggregation    Aggregation
	quantity       int64
}

// groupEntries folds accepted records down to one contribution per subject,
// meter, and period, preserving the order in which each group was first seen.
//
// Order matters only for reproducibility: the statements a batch issues are the
// same on every run, which is what makes a failing batch debuggable and a query
// test assertable.
func groupEntries(entries []Entry) []entryGroup {
	var (
		order  []string
		groups = map[string]*entryGroup{}
	)

	for i := range entries {
		e := &entries[i]

		key := e.Subject + "\x00" + e.Meter + "\x00" + e.Bounds.Start.UTC().Format(time.RFC3339Nano)

		group, ok := groups[key]
		if !ok {
			group = &entryGroup{
				subject:     e.Subject,
				meter:       e.Meter,
				aggregation: e.Aggregation,
				bounds:      e.Bounds,
				// Seeded at the window's start rather than the zero time, so a
				// last-aggregation meter's first record is always "newer" and the
				// column never holds a year-one timestamp for GREATEST to compare
				// against.
				lastOccurredAt: e.Bounds.Start,
			}
			groups[key] = group
			order = append(order, key)
		}

		newer := !e.OccurredAt.Before(group.lastOccurredAt)
		group.quantity = e.Aggregation.Fold(group.quantity, e.Quantity, newer)

		if newer {
			group.lastOccurredAt = e.OccurredAt
		}
	}

	grouped := make([]entryGroup, 0, len(order))
	for _, key := range order {
		grouped = append(grouped, *groups[key])
	}

	return grouped
}

// newDecision assembles the answer to a quota question from a projected total.
func newDecision(meter string, behavior QuotaBehavior, projected, limit int64, resetsAt time.Time) *Decision {
	return &Decision{
		Meter:    meter,
		Behavior: behavior,
		Used:     projected,
		Limit:    limit,
		Overage:  overageOf(projected, limit),
		ResetsAt: resetsAt.UTC(),
		// Over the limit is refused only under BehaviorBlock. Warn and
		// AllowOverage both let it through, and differ in whether the caller is
		// meant to do anything about it — which is what Decision.Overage and
		// Decision.Behavior are for.
		Allowed: projected <= limit || behavior.records(),
	}
}

// overageOf is how far a total is past a limit, or zero when it is not.
func overageOf(used, limit int64) int64 {
	return max(0, used-limit)
}

// scanTotalKeys drains a composite-key projection.
func scanTotalKeys(ctx context.Context, q database.SQLQueryExecutor, query string, args []any) ([]totalKey, error) {
	return database.ScanAll(ctx, q, "metering total key", query, args, func(scanner database.Scanner) (totalKey, error) {
		var k totalKey
		if err := scanner.Scan(&k.subject, &k.meter, &k.periodStart); err != nil {
			return totalKey{}, err
		}

		k.periodStart = k.periodStart.UTC()

		return k, nil
	})
}

// scanTotals drains a total projection.
func scanTotals(ctx context.Context, q database.SQLQueryExecutor, query string, args []any) ([]*Total, error) {
	return database.ScanAll(ctx, q, "metering total", query, args, scanTotal)
}

// scanTotal reads one row of totalColumns.
func scanTotal(scanner database.Scanner) (*Total, error) {
	var (
		total       Total
		aggregation string
		lastErr     sql.NullString
	)

	if err := scanner.Scan(
		&total.Subject, &total.Meter, &total.PeriodStart, &total.PeriodEnd, &aggregation,
		&total.Quantity, &total.LastOccurredAt, &total.FlushedQuantity,
		&total.FlushSequence, &total.FlushAttempts, &total.NextFlush, &lastErr,
	); err != nil {
		return nil, err
	}

	total.Aggregation = Aggregation(aggregation)
	total.PeriodStart = total.PeriodStart.UTC()
	total.PeriodEnd = total.PeriodEnd.UTC()
	total.LastOccurredAt = total.LastOccurredAt.UTC()
	total.NextFlush = total.NextFlush.UTC()
	total.LastError = database.StringFromNullString(lastErr)

	return &total, nil
}

// encodeDimensions renders a usage record's dimensions for storage, or nil for an
// empty set. Nil and empty collapse deliberately: they say the same thing, and
// storing two renderings would make a round trip depend on which call site wrote
// the row.
func encodeDimensions(dimensions map[string]string) ([]byte, error) {
	if len(dimensions) == 0 {
		return nil, nil
	}

	encoded, err := json.Marshal(dimensions)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding metering usage dimensions")
	}

	return encoded, nil
}
