package metering

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/metering/internal/meteringdb"
	"github.com/primandproper/platform-go/v14/metering/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlguard"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
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
	// client is not what Record, Consume or Total run on — those are handed the
	// caller's transaction or executor. It is here for the three things a
	// Client answers that a caller's Tx cannot: the dialect the generated
	// statements are rendered for, read once at construction; the executor the
	// flush settlements and the reaper run on, which belong to nobody's request
	// and so can join nobody's transaction; and the transaction ClaimFlushable
	// opens for itself, which has to commit before the provider is posted to.
	client database.Client
	q      meteringdb.Querier
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

	// prefix is the namespace the table names carry. It is kept because the
	// generated querier was built from it and the migrations are validated
	// against it, not because anything here renders a name from it any more.
	prefix string

	// resolution is the finest interval an instant survives a round trip
	// through this dialect at. See storedResolution.
	resolution time.Duration
}

// NewSQLStore builds a Store over the given database.
//
// The client is not what the consumer-facing methods execute on. Record and
// Consume take the caller's database.Tx and Total takes an executor; what this
// one supplies is the dialect, the connection the flush loop and the reaper run
// on, and the transaction ClaimFlushable opens for itself. See SQLStore's
// fields, and Store for why those four take no executor.
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
		client:     client,
		prefix:     DefaultTablePrefix,
		resolution: storedResolution(d),
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
	// substitution; see metering/internal/queries.
	qd, err := meteringdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := meteringdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the metering querier")
	}

	s.q = q

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One counter, and only one. The Recorder, Enforcer, and Flusher own the
	// business totals — recorded, allowed, denied, flushed — and a second name for
	// the same event is how two dashboards come to disagree. What no caller can
	// count is this: a guarded write that matched no row. That is not a database
	// error, and above this layer it is indistinguishable from one.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

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

// meteringdbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSQLStore has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is
// a construction failure like any other, and it names the dialect, rather than
// panicking or leaning on meteringdb.New refusing the empty string.
func meteringdbDialect(d dialect.Dialect) (meteringdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return meteringdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return meteringdb.DialectMySQL, nil
	case dialect.SQLite:
		return meteringdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "metering dialect %q", d)
	}
}

func (s *SQLStore) Record(
	ctx context.Context,
	tx database.Tx,
	entries []Entry,
	at time.Time,
) (RecordResult, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(batchSizeKey, len(entries)))
	defer op.End()

	if tx == nil {
		return RecordResult{}, op.Error(ErrNilExecutor, "recording metering usage")
	}

	if len(entries) == 0 {
		return RecordResult{}, nil
	}

	result, err := s.record(ctx, op, tx, entries, at)
	if err != nil {
		return RecordResult{}, err
	}

	op.Set(acceptedKey, result.Accepted).Set(duplicateKey, result.Duplicates)

	return result, nil
}

// record dedupes every entry against the ledger, then folds what survived into
// its period's total.
//
// It runs entirely inside the transaction it is given. The ledger row and the
// aggregate it feeds are one fact, and one transaction for the batch is what
// keeps a crash from leaving half-counted events or a total folded from records
// whose ledger rows never landed. That transaction is the caller's, so it also
// covers whatever else they are writing.
func (s *SQLStore) record(
	ctx context.Context,
	op observability.Operation,
	q meteringdb.DBTX,
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
	groups := groupEntries(accepted, s.resolution)
	for i := range groups {
		if err := s.fold(ctx, q, &groups[i], at); err != nil {
			return RecordResult{}, op.Error(err, "folding metering usage into its total")
		}
	}

	return result, nil
}

// fold folds one group's contribution into its period's total: open the row,
// then let the server do the arithmetic against whatever it holds.
//
// The two halves are what makes concurrent ingest safe without a lock. Two
// recorders folding into the same period at the same instant would otherwise
// both read the total, both add their own quantity to it, and between them lose
// one — silently, and in the direction that under-bills. The seed skips a row
// that is already there, and the fold is an UPDATE the server evaluates when it
// gets there.
func (s *SQLStore) fold(ctx context.Context, q meteringdb.DBTX, group *entryGroup, at time.Time) error {
	if err := s.openTotal(ctx, q, group.subject, group.meter, group.aggregation, group.bounds, at); err != nil {
		return err
	}

	stamped := at.UTC()

	params := meteringdb.FoldMeteringTotalSumParams{
		Quantity:       group.quantity,
		LastOccurredAt: group.lastOccurredAt.UTC(),
		LastUpdatedAt:  &stamped,
		Subject:        group.subject,
		Meter:          group.meter,
		PeriodStart:    group.bounds.Start.UTC(),
	}

	// One generated method per aggregation, chosen here. The parameter structs
	// are three shapes of the same six values, so the conversion is a
	// relabelling rather than three assemblies — and an aggregation with no
	// statement is this switch's default rather than a write that leaves the
	// total where it found it.
	switch group.aggregation {
	case AggregationSum:
		_, err := s.q.FoldMeteringTotalSum(ctx, q, params)

		return err
	case AggregationMax:
		_, err := s.q.FoldMeteringTotalMax(ctx, q, meteringdb.FoldMeteringTotalMaxParams(params))

		return err
	case AggregationLast:
		_, err := s.q.FoldMeteringTotalLast(ctx, q, meteringdb.FoldMeteringTotalLastParams{
			LastOccurredAt: params.LastOccurredAt,
			Quantity:       params.Quantity,
			LastUpdatedAt:  params.LastUpdatedAt,
			Subject:        params.Subject,
			Meter:          params.Meter,
			PeriodStart:    params.PeriodStart,
		})

		return err
	case AggregationUniqueCount:
		// Named and refused. Registration declines it above this layer, so
		// reaching here means something bypassed the registry.
		return platformerrors.Wrapf(ErrUnsupportedAggregation, "meter %q aggregation %q", group.meter, group.aggregation)
	default:
		return platformerrors.Wrapf(ErrUnsupportedAggregation, "meter %q aggregation %q", group.meter, group.aggregation)
	}
}

// openTotal writes the period's total if nothing has yet, and leaves the one
// that is there alone.
//
// It is what gives every other write to the table a row to work against: the
// folds fold into it, and Consume locks it. Two callers opening the same period
// at once is a race the conflict-ignore settles — the loser simply proceeds,
// because what it wanted was for the row to exist.
func (s *SQLStore) openTotal(
	ctx context.Context,
	q meteringdb.DBTX,
	subject, meter string,
	aggregation Aggregation,
	bounds Bounds,
	at time.Time,
) error {
	_, err := s.q.InsertMeteringTotal(ctx, q, meteringdb.InsertMeteringTotalParams{
		Subject:        subject,
		Meter:          meter,
		PeriodStart:    bounds.Start.UTC(),
		PeriodEnd:      bounds.End.UTC(),
		Aggregation:    string(aggregation),
		Quantity:       0,
		LastOccurredAt: seedLastOccurredAt(bounds),
		NextFlush:      at.UTC(),
		// Supplied rather than defaulted, because one of the three dialects
		// takes no default on the column — see metering/internal/queries.
		LastError: "",
		CreatedAt: at.UTC(),
	})

	return err
}

// eventExists reports whether this entry's (meter, idempotency_key) is already
// in the ledger.
func (s *SQLStore) eventExists(ctx context.Context, q meteringdb.DBTX, entry *Entry) (bool, error) {
	row, err := s.q.MeteringEventExists(ctx, q, meteringdb.MeteringEventExistsParams{
		Meter:          entry.Meter,
		IdempotencyKey: entry.IdempotencyKey,
	})
	if err != nil {
		return false, err
	}

	return row.Exists, nil
}

// insertEvent writes one ledger row, reporting whether it was new.
//
// The count is the dedupe. A key already in the table takes no row and reports
// zero, which is how the caller learns the usage was already counted — decided
// by the database, in one round trip, and durable for as long as the row is
// retained.
func (s *SQLStore) insertEvent(
	ctx context.Context,
	q meteringdb.DBTX,
	entry *Entry,
	at time.Time,
) (bool, error) {
	dimensions, err := encodeDimensions(entry.Dimensions)
	if err != nil {
		return false, err
	}

	affected, err := s.q.InsertMeteringEvent(ctx, q, meteringdb.InsertMeteringEventParams{
		IdempotencyKey: entry.IdempotencyKey,
		Subject:        entry.Subject,
		Meter:          entry.Meter,
		Quantity:       entry.Quantity,
		OccurredAt:     entry.OccurredAt.UTC(),
		RecordedAt:     at.UTC(),
		PeriodStart:    entry.Bounds.Start.UTC(),
		Dimensions:     dimensions,
	})
	if err != nil {
		return false, err
	}

	return affected > 0, nil
}

func (s *SQLStore) Total(
	ctx context.Context,
	q database.SQLQueryExecutor,
	subject, meter string,
	bounds Bounds,
) (*Total, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		subjectKey:     subject,
		meterKey:       meter,
		periodStartKey: bounds.Start,
		periodEndKey:   bounds.End,
	}))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading metering total")
	}

	row, err := s.q.GetMeteringTotal(ctx, q, meteringdb.GetMeteringTotalParams{
		Subject:     subject,
		Meter:       meter,
		PeriodStart: bounds.Start.UTC(),
	})
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

	total := totalFrom((*meteringdb.SelectFlushableMeteringTotalsRow)(&row))

	op.Set(usedKey, total.Quantity)

	return total, nil
}

//nolint:gocritic // hugeParam: Entry is taken by value to match Store.Consume's interface
func (s *SQLStore) Consume(
	ctx context.Context,
	tx database.Tx,
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

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "consuming metering quota")
	}

	decision, err := s.consume(ctx, op, tx, &entry, limit, behavior, at)
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
// The lock it takes lives as long as the transaction it is given, which is the
// caller's. That is what makes the decision a reservation rather than an
// observation: nothing else can consume that subject's meter between this
// decision and the commit of the work it authorized.
//
// The order is load-bearing. The row is opened first so there is something to
// lock; the lock is taken before the total is read, so the number decided
// against is the committed one; the decision is made before the ledger row is
// written, so a refused consume does not burn its idempotency key on usage it
// never recorded; and the total is only written once the ledger row proves the
// usage is new.
func (s *SQLStore) consume(
	ctx context.Context,
	op observability.Operation,
	q meteringdb.DBTX,
	entry *Entry,
	limit int64,
	behavior QuotaBehavior,
	at time.Time,
) (*Decision, error) {
	if err := s.openTotal(ctx, q, entry.Subject, entry.Meter, entry.Aggregation, entry.Bounds, at); err != nil {
		return nil, op.Error(err, "opening metering total")
	}

	row, err := s.q.GetMeteringTotalForUpdate(ctx, q, meteringdb.GetMeteringTotalForUpdateParams{
		Subject:     entry.Subject,
		Meter:       entry.Meter,
		PeriodStart: entry.Bounds.Start.UTC(),
	})
	if err != nil {
		return nil, op.Error(err, "locking metering total")
	}

	total := totalFrom((*meteringdb.SelectFlushableMeteringTotalsRow)(&row))

	// The instant this row will hold, which is the entry's brought down to what
	// the dialect stores. The comparison below is against a value that has
	// already been through that, so comparing the entry's own reading against it
	// would be comparing two resolutions: on SQLite a record stamped a hundred
	// milliseconds into a second the row already holds would read as strictly
	// newer than a record stamped nine hundred, which is the redelivery that
	// resets a gauge backwards.
	occurred := entry.OccurredAt.UTC().Truncate(s.resolution)

	// Strictly after, which is the comparison the fold statement makes against
	// the column — see metering/internal/queries. This path decides in Go
	// because it holds the lock, so the two have to agree about which record a
	// tie goes to.
	newer := occurred.After(total.LastOccurredAt)
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

	stamped := at.UTC()

	// Assigned rather than folded, and the event time maximized here rather
	// than in the statement, because this write runs against a row this
	// transaction holds the lock on: the arithmetic was done above against the
	// committed value, and nothing can have moved it since. Every other write
	// to this table takes the other reading, because none of them holds a lock.
	if _, err = s.q.ApplyMeteringConsume(ctx, q, meteringdb.ApplyMeteringConsumeParams{
		Quantity:       projected,
		LastOccurredAt: laterOf(total.LastOccurredAt, occurred),
		LastUpdatedAt:  &stamped,
		Subject:        entry.Subject,
		Meter:          entry.Meter,
		PeriodStart:    entry.Bounds.Start.UTC(),
	}); err != nil {
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

	var claimed []*Total

	// The select and the leases run in one transaction so that FOR UPDATE SKIP
	// LOCKED means anything. Without it the lock is released before the first
	// lease, and two flushers select the same totals.
	err := s.client.WithTransaction(ctx, func(q database.Tx) error {
		var claimErr error
		claimed, claimErr = s.claim(ctx, op, q, now, limit, maxAttempts, leaseUntil)

		return claimErr
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

// claim reads the batch and leases it a row at a time, returning the totals
// this flusher now holds.
//
// A lease per row rather than one over the batch. The row-value IN list the
// batch form needed has no static arity — its shape is the caller's cardinality
// — so there is nothing for sqlc to check; and the re-read it needed to see the
// attempt counts carried no guard, so a total another flusher settled in
// between came back as one this flusher held. Keyed per row, the count of each
// lease is the answer to both: the total either qualified and is held, or it did
// not and is not in the batch.
//
// The attempt count each returned total carries is the one that was read plus
// the one this lease just added. That is exact rather than optimistic — the
// lease matched, and the row is held for the rest of this transaction — and it
// is what a flusher deciding whether it has exhausted its budget has to see.
func (s *SQLStore) claim(
	ctx context.Context,
	op observability.Operation,
	q meteringdb.DBTX,
	now time.Time,
	limit, maxAttempts int,
	leaseUntil time.Time,
) ([]*Total, error) {
	expiredBy := now.UTC()

	rows, err := s.q.SelectFlushableMeteringTotals(ctx, q, meteringdb.SelectFlushableMeteringTotalsParams{
		DueAt:          expiredBy,
		MaxAttempts:    int64(maxAttempts),
		LeaseExpiredBy: &expiredBy,
		ResultLimit:    int64(limit),
	})
	if err != nil {
		return nil, op.Error(err, "selecting flushable metering totals")
	}

	leased := leaseUntil.UTC()

	claimed := make([]*Total, 0, len(rows))

	for i := range rows {
		total := totalFrom(&rows[i])

		affected, leaseErr := s.q.ClaimMeteringTotal(ctx, q, meteringdb.ClaimMeteringTotalParams{
			ClaimedUntil: &leased,
			Subject:      total.Subject,
			Meter:        total.Meter,
			PeriodStart:  total.PeriodStart,
		})
		if leaseErr != nil {
			return nil, op.Error(leaseErr, "claiming flushable metering total")
		}

		if affected == 0 {
			// Settled by another flusher between the read and this lease. It
			// owes the provider nothing now, so it is not this pass's to post.
			continue
		}

		total.FlushAttempts++

		claimed = append(claimed, total)
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

	stamped := at.UTC()

	affected, err := s.q.MarkMeteringTotalFlushed(ctx, s.client.Writer(), meteringdb.MarkMeteringTotalFlushedParams{
		FlushedQuantity: flushed,
		NextFlush:       at.UTC(),
		LastUpdatedAt:   &stamped,
		Subject:         total.Subject,
		Meter:           total.Meter,
		PeriodStart:     total.PeriodStart.UTC(),
		FlushSequence:   int64(total.FlushSequence),
	})

	return s.guard.Count(ctx, op, affected, err, total.Meter, "mark_flushed",
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

	stamped := nextFlush.UTC()

	// The lease is handed back by binding NULL, and flushed_quantity is not
	// assigned at all: the post may have reached the provider and failed on the
	// way back, so the next attempt has to carry the same delta under the same
	// sequence.
	affected, err := s.q.ReleaseMeteringFlush(ctx, s.client.Writer(), meteringdb.ReleaseMeteringFlushParams{
		NextFlush:     stamped,
		LastError:     lastErr,
		ClaimedUntil:  nil,
		LastUpdatedAt: &stamped,
		Subject:       total.Subject,
		Meter:         total.Meter,
		PeriodStart:   total.PeriodStart.UTC(),
		FlushSequence: int64(total.FlushSequence),
	})

	return s.guard.Count(ctx, op, affected, err, total.Meter, "release_flush",
		"releasing metering flush lease")
}

func (s *SQLStore) ReapEvents(ctx context.Context, horizon time.Time, limit int) (int64, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	if limit <= 0 {
		return 0, nil
	}

	reaped, err := s.q.PruneMeteringEvents(ctx, s.client.Writer(), meteringdb.PruneMeteringEventsParams{
		Horizon:     horizon.UTC(),
		ResultLimit: int64(limit),
	})
	if err != nil {
		return 0, op.Error(err, "reaping metering usage events")
	}

	op.Set(reapedKey, reaped)

	return reaped, nil
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
//
// resolution is what the dialect stores an instant at, and it is an argument
// rather than a constant because this fold has to answer what folding the same
// records one at a time through the statements would — see storedResolution.
func groupEntries(entries []Entry, resolution time.Duration) []entryGroup {
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
				// The seed the statement uses, so that folding a batch in
				// process and folding it a record at a time are the same
				// function down to which record a tie goes to.
				lastOccurredAt: seedLastOccurredAt(e.Bounds),
			}
			groups[key] = group
			order = append(order, key)
		}

		// Brought down to what the dialect stores before it is compared,
		// because that is what the row will hold and what the statement folding
		// these one at a time would compare. Two records a batch can tell apart
		// and the store cannot are records the store cannot tell apart.
		occurred := e.OccurredAt.UTC().Truncate(resolution)

		newer := occurred.After(group.lastOccurredAt)
		group.quantity = e.Aggregation.Fold(group.quantity, e.Quantity, newer)

		if newer {
			group.lastOccurredAt = occurred
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

// storedResolution is the finest interval an instant survives a round trip
// through one of these engines at.
//
// SQLite has no date type: a DATETIME column holds text, and the generated
// bindings write a bound time in the shape SQLite's own CURRENT_TIMESTAMP
// writes, which is whole seconds. Postgres and MySQL keep microseconds — the
// schema asks MySQL for DATETIME(6) rather than taking its second-granular
// default, for exactly this reason.
//
// It is the one dialect fact this package holds outside its DDL, and it is here
// because Consume is the one comparison this package makes in Go against a
// value a server handed back. Every other one is the server's own, between two
// values it truncated the same way — see metering/internal/queries.
func storedResolution(d dialect.Dialect) time.Duration {
	if d == dialect.SQLite {
		return time.Second
	}

	return time.Microsecond
}

// seedLastOccurredAt is what a period's total holds before anything has been
// folded into it: a floor, not a reading.
//
// It is a whole second below the window rather than at it, because every fold's
// guard is strict and the seed therefore has to be strictly earlier than any
// record the window can hold. On SQLite a bound time is stored truncated to the
// second, so a record arriving in the window's first second is stored at the
// window's start exactly; a second is that engine's resolution and so the
// smallest step still strictly earlier on all three. Near the window rather
// than at the zero time, so a total nothing has been recorded into does not
// read back as year one. See metering/internal/queries.
//
// One home for it because two callers seed: the statement, through openTotal,
// and the in-process fold a batch is grouped by. The two are the same function
// only while they start from the same value.
func seedLastOccurredAt(bounds Bounds) time.Time {
	return bounds.Start.UTC().Add(-time.Second)
}

// laterOf is the newer of two instants, in UTC.
//
// It is what Consume's write puts in last_occurred_at, and it is in Go rather
// than in the statement because that write runs against a locked row: the
// column cannot have moved since it was read, so the comparison here and one in
// SQL have the same answer. See metering/internal/queries.
func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b.UTC()
	}

	return a.UTC()
}

// totalFrom converts a generated row into this package's Total.
//
// It is a struct literal on purpose, and it is the whole of what this package
// does with the generated types. A renamed or retyped column changes the
// generated struct and this function stops compiling; the scan-by-position
// pairing it replaced reported the same mistake as a runtime scan error, or
// worse, as two same-typed columns silently transposed.
//
// It takes the claim read's row type because every read of this table projects
// the same twelve columns in the same order, so the three generated row structs
// are one shape under three names and the two single-row reads convert to this
// one. That conversion is checked: a projection that drifted would change one
// struct and not the others, and the conversion would stop compiling.
//
// The four timestamps are normalized to UTC because every one this package
// writes is UTC and so every one it hands back should be: Postgres returns a
// time in the session's zone, MySQL in the server's, and SQLite whatever the
// stored text parsed as, so a caller comparing two of those, or rendering one
// into JSON, would get an answer that depends on where the row was read.
func totalFrom(row *meteringdb.SelectFlushableMeteringTotalsRow) *Total {
	return &Total{
		PeriodStart:     row.PeriodStart.UTC(),
		PeriodEnd:       row.PeriodEnd.UTC(),
		LastOccurredAt:  row.LastOccurredAt.UTC(),
		NextFlush:       row.NextFlush.UTC(),
		Subject:         row.Subject,
		Meter:           row.Meter,
		LastError:       row.LastError,
		Aggregation:     Aggregation(row.Aggregation),
		Quantity:        row.Quantity,
		FlushedQuantity: row.FlushedQuantity,
		FlushSequence:   int(row.FlushSequence),
		FlushAttempts:   int(row.FlushAttempts),
	}
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
