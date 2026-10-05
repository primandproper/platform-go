package series

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v15/series/internal/seriesdb"
	"github.com/primandproper/platform-go/v15/series/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "series"

	scopeKey      = serviceName + ".scope"
	seriesIDKey   = serviceName + ".series_id"
	occurrenceKey = serviceName + ".occurrence_id"
	countKey      = serviceName + ".count"
)

// DefaultTablePrefix is the namespace the two tables carry when none is
// configured, which is none — rendering series and series_occurrences. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans, logger, and instruments.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema series/migrations
// renders.
type SQLStore struct {
	q           seriesdb.Querier
	now         func() time.Time
	o11y        observability.Observer
	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer and the instruments
	// are built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	prefix          string
}

// NewSQLStore builds a Store over the given database.
//
// The client supplies the dialect and the clock an exhausted series is stamped
// from, and is otherwise not used: every statement runs on the executor a
// caller hands over.
//
// Observability is optional and defaults to nothing.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "series store dialect %q", d)
	}

	s := &SQLStore{prefix: DefaultTablePrefix, now: client.CurrentTime}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.prefix); err != nil {
		return nil, err
	}

	qd, err := seriesdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := seriesdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the series querier")
	}

	s.q = q

	if s.instruments, err = metrics.NewOperationSet(s.metricsProvider, storeName); err != nil {
		return nil, err
	}

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	return s, nil
}

// seriesdbDialect maps this module's dialect names onto the generated package's.
func seriesdbDialect(d dialect.Dialect) (seriesdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return seriesdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return seriesdb.DialectMySQL, nil
	case dialect.SQLite:
		return seriesdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated series queries for dialect %q", d)
	}
}

// failed counts a failed operation and hands the error back unchanged.
func (s *SQLStore) failed(ctx context.Context, err error) error {
	s.instruments.Failed(ctx)

	return err
}

// CreateSeries stores a rule. See Store.CreateSeries.
//
// The series is born written out to the start of its first day, which is the
// instant before any occurrence it could imply: nothing is materialized yet,
// and the horizon worker's first pass writes from there.
func (s *SQLStore) CreateSeries(ctx context.Context, tx database.Tx, scope tenancy.Scope, rule *Rule) (*Series, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "creating series"))
	}

	loc, err := rule.validate()
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "creating series"))
	}

	if err = withinWriteBehind(midnight(loc, rule.StartsOn), s.now()); err != nil {
		return nil, s.failed(ctx, op.Error(err, "creating series"))
	}

	if err = scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "creating series"))
	}

	id := identifiers.New()
	op.Set(seriesIDKey, id)

	if err = s.q.CreateSeries(ctx, tx, seriesdb.CreateSeriesParams{
		ID:                id,
		Scope:             scope,
		TimeZone:          rule.TimeZone,
		Weekday:           int64(rule.Weekday),
		StartMinute:       int64(rule.StartMinute),
		IntervalWeeks:     int64(rule.IntervalWeeks),
		StartsOn:          rule.StartsOn.String(),
		EndsOn:            rule.EndsOn.String(),
		MaterializedUntil: midnight(loc, rule.StartsOn),
		ExhaustedAt:       nil,
	}); err != nil {
		return nil, s.failed(ctx, op.Error(err, "writing the series row"))
	}

	created, err := s.readSeries(ctx, tx, scope, id)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the created series"))
	}

	return created, nil
}

// GetSeries reads one live series. See Store.GetSeries.
func (s *SQLStore) GetSeries(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, id string) (*Series, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()), observability.WithValue(seriesIDKey, id))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "reading series"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading series"))
	}

	found, err := s.readSeries(ctx, q, scope, id)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading series"))
	}

	return found, nil
}

// ListSeries reads a page of a scope's series. See Store.ListSeries.
func (s *SQLStore) ListSeries(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	afterID string,
	limit int,
) ([]*Series, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing series"))
	}

	if limit < 1 || limit > MaxSeriesPerPage {
		return nil, s.failed(ctx, op.Error(platformerrors.Wrapf(ErrInvalidPageSize, "%d is outside 1 to %d", limit, MaxSeriesPerPage), "listing series"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing series"))
	}

	page, err := s.listSeries(ctx, q, scope, afterID, limit)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing series"))
	}

	op.SpanOnly(countKey, len(page))

	return page, nil
}

// listSeries is one page of the scope's series, unvalidated: ListSeries and a
// closure's walk both read through it.
func (s *SQLStore) listSeries(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	afterID string,
	limit int,
) ([]*Series, error) {
	rows, err := s.q.ListSeries(ctx, q, seriesdb.ListSeriesParams{
		Scope:       scope,
		AfterID:     afterID,
		ResultLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}

	page := make([]*Series, 0, len(rows))

	for i := range rows {
		row := seriesdb.GetSeriesRow(rows[i])

		converted, convErr := seriesFromRow(&row)
		if convErr != nil {
			return nil, convErr
		}

		page = append(page, converted)
	}

	return page, nil
}

// EndSeries ends a series from a date. See Store.EndSeries.
//
// A read, a write of the date, and then the sweep over the occurrences the rule
// put on or after it, a bounded batch at a time on the caller's transaction
// until a batch comes back short, and the read-back. The sweep's cutoff is
// midnight of the end date in the rule's zone; every slot the store writes is
// whole seconds, so "at or after midnight" is "after the second before it",
// which is the comparison the statement spells.
func (s *SQLStore) EndSeries(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	id string,
	from Date,
	reason string,
) (*Series, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()), observability.WithValue(seriesIDKey, id))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "ending series"))
	}

	if from.IsZero() || !from.valid() {
		return nil, s.failed(ctx, op.Error(platformerrors.Wrapf(ErrInvalidRule, "end date %+v is not a date", from), "ending series"))
	}

	if err := validateReason(reason); err != nil {
		return nil, s.failed(ctx, op.Error(err, "ending series"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "ending series"))
	}

	current, err := s.readSeries(ctx, tx, scope, id)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "ending series"))
	}

	if !current.Rule.EndsOn.IsZero() && !from.Before(current.Rule.EndsOn) {
		return nil, s.failed(ctx, op.Error(platformerrors.Wrapf(ErrSeriesEnded, "it ends on %s", current.Rule.EndsOn), "ending series"))
	}

	loc, err := loadZone(current.Rule.TimeZone)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "ending series"))
	}

	count, err := s.q.EndSeries(ctx, tx, seriesdb.EndSeriesParams{EndsOn: from.String(), ID: id, Scope: scope})
	if err = guardCount(count, err, ErrSeriesNotFound); err != nil {
		return nil, s.failed(ctx, op.Error(err, "writing the series' end"))
	}

	slotAfter := midnight(loc, from).Add(-time.Second)

	skipped, err := sweep(func() (int64, error) {
		return s.q.SkipSeriesFrom(ctx, tx, seriesdb.SkipSeriesFromParams{
			State:        string(StateSkipped),
			Reason:       reason,
			Scope:        scope,
			SeriesID:     id,
			SlotAfter:    &slotAfter,
			SkippedState: string(StateSkipped),
			ResultLimit:  sweepBatch,
		})
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "skipping the ended series' occurrences"))
	}

	op.Set(countKey, skipped)

	ended, err := s.readSeries(ctx, tx, scope, id)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the ended series"))
	}

	return ended, nil
}

// Materialize writes a series' occurrences before through. See
// Store.Materialize.
func (s *SQLStore) Materialize(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	seriesID string,
	through time.Time,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()), observability.WithValue(seriesIDKey, seriesID))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return 0, s.failed(ctx, op.Error(ErrNilExecutor, "materializing series"))
	}

	if through.IsZero() {
		return 0, s.failed(ctx, op.Error(ErrNoInstant, "materializing series"))
	}

	if err := withinWriteAhead(through, s.now()); err != nil {
		return 0, s.failed(ctx, op.Error(err, "materializing series"))
	}

	if err := scope.Validate(); err != nil {
		return 0, s.failed(ctx, op.Error(err, "materializing series"))
	}

	found, err := s.readSeries(ctx, tx, scope, seriesID)
	if err != nil {
		return 0, s.failed(ctx, op.Error(err, "materializing series"))
	}

	written, err := s.materialize(ctx, tx, found, through)
	if err != nil {
		return 0, s.failed(ctx, op.Error(err, "materializing series"))
	}

	op.Set(countKey, written)

	return written, nil
}

// materialize writes every slot the series implies in [MaterializedUntil,
// through), then records how far it got.
//
// through is rounded up to the second, because every slot is whole seconds:
// "before through" and "before through rounded up" are then the same set, and
// the rounded value is what the series records.
//
// Each slot is its own insert, ignored where the slot already has a row — a
// statement per slot rather than one with a VALUES list, because the multi-row
// form has no static text for sqlc to check. The count is a horizon's worth of
// weeks for a series the worker keeps up with.
func (s *SQLStore) materialize(ctx context.Context, tx database.Tx, found *Series, through time.Time) (int64, error) {
	loc, err := loadZone(found.Rule.TimeZone)
	if err != nil {
		return 0, err
	}

	through = ceilSecond(through)
	if !found.MaterializedUntil.Before(through) {
		return 0, nil
	}

	var written int64

	for slot := range found.Rule.slots(loc, found.MaterializedUntil, through) {
		n, insertErr := s.q.MaterializeOccurrence(ctx, tx, seriesdb.MaterializeOccurrenceParams{
			ID:          identifiers.New(),
			Scope:       found.Scope,
			SeriesID:    found.ID,
			ScheduledAt: slot,
			SlotAt:      &slot,
			State:       string(StateScheduled),
			ReplacedBy:  "",
			Reason:      "",
		})
		if insertErr != nil {
			return written, platformerrors.Wrapf(insertErr, "writing the slot at %s", slot)
		}

		written += n
	}

	var exhaustedAt *time.Time
	if !found.Rule.EndsOn.IsZero() && !through.Before(midnight(loc, found.Rule.EndsOn)) {
		now := stored(s.now())
		exhaustedAt = &now
	}

	// A zero count is another pass having recorded a further horizon first,
	// which is not a failure: the slots this pass wrote are written either
	// way, and the further horizon is the truer one.
	if _, err = s.q.AdvanceSeries(ctx, tx, seriesdb.AdvanceSeriesParams{
		MaterializedUntil: through,
		ExhaustedAt:       exhaustedAt,
		ID:                found.ID,
		Scope:             found.Scope,
		Through:           through,
	}); err != nil {
		return written, platformerrors.Wrap(err, "recording how far the series is written")
	}

	return written, nil
}

// GetOccurrence reads one live occurrence. See Store.GetOccurrence.
func (s *SQLStore) GetOccurrence(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, id string) (*Occurrence, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()), observability.WithValue(occurrenceKey, id))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "reading occurrence"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading occurrence"))
	}

	found, err := s.readOccurrence(ctx, q, scope, id)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading occurrence"))
	}

	return found, nil
}

// ListOccurrences reads a scope's occurrences in a window. See
// Store.ListOccurrences.
//
// It asks for one more row than it will answer with, which is how it tells a
// window that holds exactly the most it reads from one that holds more.
func (s *SQLStore) ListOccurrences(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, window Window) ([]*Occurrence, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing occurrences"))
	}

	after, through, err := window.bounds()
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing occurrences"))
	}

	if err = scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing occurrences"))
	}

	rows, err := s.q.ListOccurrences(ctx, q, seriesdb.ListOccurrencesParams{
		Scope:       scope,
		After:       after,
		Through:     through,
		ResultLimit: MaxOccurrencesPerRead + 1,
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing occurrences"))
	}

	if len(rows) > MaxOccurrencesPerRead {
		return nil, s.failed(ctx, op.Error(ErrWindowTooLarge, "listing occurrences"))
	}

	out := make([]*Occurrence, 0, len(rows))
	for i := range rows {
		row := seriesdb.GetOccurrenceRow(rows[i])
		out = append(out, occurrenceFromRow(&row))
	}

	op.SpanOnly(countKey, len(out))

	return out, nil
}

// ListSeriesOccurrences reads one series' occurrences in a window. See
// Store.ListSeriesOccurrences.
func (s *SQLStore) ListSeriesOccurrences(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	seriesID string,
	window Window,
) ([]*Occurrence, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()), observability.WithValue(seriesIDKey, seriesID))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing a series' occurrences"))
	}

	after, through, err := window.bounds()
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing a series' occurrences"))
	}

	if err = scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing a series' occurrences"))
	}

	rows, err := s.q.ListSeriesOccurrences(ctx, q, seriesdb.ListSeriesOccurrencesParams{
		Scope:       scope,
		SeriesID:    seriesID,
		After:       after,
		Through:     through,
		ResultLimit: MaxOccurrencesPerRead + 1,
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing a series' occurrences"))
	}

	if len(rows) > MaxOccurrencesPerRead {
		return nil, s.failed(ctx, op.Error(ErrWindowTooLarge, "listing a series' occurrences"))
	}

	out := make([]*Occurrence, 0, len(rows))
	for i := range rows {
		row := seriesdb.GetOccurrenceRow(rows[i])
		out = append(out, occurrenceFromRow(&row))
	}

	op.SpanOnly(countKey, len(out))

	return out, nil
}

// SkipOccurrence marks one occurrence skipped. See Store.SkipOccurrence.
//
// The read tells an absent occurrence from a skipped one, which the guarded
// write alone cannot: both match nothing. The write's own guard is what counts
// when a second skip lands between the two.
func (s *SQLStore) SkipOccurrence(ctx context.Context, tx database.Tx, scope tenancy.Scope, id, reason string) (*Occurrence, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()), observability.WithValue(occurrenceKey, id))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	current, err := s.actionable(ctx, tx, scope, id, reason)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "skipping occurrence"))
	}

	op.Set(seriesIDKey, current.SeriesID)

	count, err := s.q.SkipOccurrence(ctx, tx, seriesdb.SkipOccurrenceParams{
		State:        string(StateSkipped),
		Reason:       reason,
		ID:           id,
		Scope:        scope,
		SkippedState: string(StateSkipped),
	})
	if err = guardCount(count, err, ErrOccurrenceSkipped); err != nil {
		return nil, s.failed(ctx, op.Error(err, "skipping occurrence"))
	}

	skipped, err := s.readOccurrence(ctx, tx, scope, id)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the skipped occurrence"))
	}

	return skipped, nil
}

// MoveOccurrence gives one occurrence a new instant. See Store.MoveOccurrence.
func (s *SQLStore) MoveOccurrence(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	id string,
	to time.Time,
	reason string,
) (*Occurrence, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()), observability.WithValue(occurrenceKey, id))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if to.IsZero() {
		return nil, s.failed(ctx, op.Error(ErrNoInstant, "moving occurrence"))
	}

	current, err := s.actionable(ctx, tx, scope, id, reason)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "moving occurrence"))
	}

	op.Set(seriesIDKey, current.SeriesID)

	count, err := s.q.MoveOccurrence(ctx, tx, seriesdb.MoveOccurrenceParams{
		ScheduledAt:  stored(to),
		State:        string(StateMoved),
		Reason:       reason,
		ID:           id,
		Scope:        scope,
		SkippedState: string(StateSkipped),
	})
	if err = guardCount(count, err, ErrOccurrenceSkipped); err != nil {
		return nil, s.failed(ctx, op.Error(err, "moving occurrence"))
	}

	moved, err := s.readOccurrence(ctx, tx, scope, id)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the moved occurrence"))
	}

	return moved, nil
}

// actionable is the check a skip and a move share: an executor, a reason that
// fits, a valid scope, and an occurrence that exists and is not skipped.
func (s *SQLStore) actionable(ctx context.Context, tx database.Tx, scope tenancy.Scope, id, reason string) (*Occurrence, error) {
	if tx == nil {
		return nil, ErrNilExecutor
	}

	if err := validateReason(reason); err != nil {
		return nil, err
	}

	if err := scope.Validate(); err != nil {
		return nil, err
	}

	current, err := s.readOccurrence(ctx, tx, scope, id)
	if err != nil {
		return nil, err
	}

	if current.State == StateSkipped {
		return nil, ErrOccurrenceSkipped
	}

	return current, nil
}

// AddReplacement adds an occurrence in place of a skipped one. See
// Store.AddReplacement.
//
// The link is written before the replacement, and is the guard: it requires
// the skipped occurrence still skipped and still unreplaced, so of two
// replacements racing for one skip the second matches nothing and inserts
// nothing. The id is minted first so the link can name the row it is about to
// be followed by, on the same transaction.
func (s *SQLStore) AddReplacement(ctx context.Context, tx database.Tx, scope tenancy.Scope, skippedID string, at time.Time) (*Occurrence, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()), observability.WithValue(occurrenceKey, skippedID))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "adding replacement"))
	}

	if at.IsZero() {
		return nil, s.failed(ctx, op.Error(ErrNoInstant, "adding replacement"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "adding replacement"))
	}

	skipped, err := s.readOccurrence(ctx, tx, scope, skippedID)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "adding replacement"))
	}

	switch {
	case skipped.State != StateSkipped:
		return nil, s.failed(ctx, op.Error(ErrOccurrenceNotSkipped, "adding replacement"))
	case skipped.ReplacedBy != "":
		return nil, s.failed(ctx, op.Error(ErrOccurrenceReplaced, "adding replacement"))
	}

	op.Set(seriesIDKey, skipped.SeriesID)

	replacementID := identifiers.New()

	count, err := s.q.LinkReplacement(ctx, tx, seriesdb.LinkReplacementParams{
		ReplacedBy:    replacementID,
		ID:            skippedID,
		Scope:         scope,
		ExpectedState: string(StateSkipped),
	})
	if err = guardCount(count, err, ErrOccurrenceReplaced); err != nil {
		return nil, s.failed(ctx, op.Error(err, "linking replacement"))
	}

	if err = s.q.CreateOccurrence(ctx, tx, seriesdb.CreateOccurrenceParams{
		ID:          replacementID,
		Scope:       scope,
		SeriesID:    skipped.SeriesID,
		ScheduledAt: stored(at),
		SlotAt:      nil,
		State:       string(StateScheduled),
		ReplacedBy:  "",
		Reason:      "",
	}); err != nil {
		return nil, s.failed(ctx, op.Error(err, "writing replacement"))
	}

	replacement, err := s.readOccurrence(ctx, tx, scope, replacementID)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the replacement"))
	}

	return replacement, nil
}

// SkipWindow closes a window across a scope. See Store.SkipWindow.
//
// Two phases on the caller's transaction. The first walks every series in the
// scope a page at a time and writes each out to the window's end, so the
// window's slots have rows to skip. The second sweeps the window a bounded
// batch at a time until a batch comes back short.
func (s *SQLStore) SkipWindow(ctx context.Context, tx database.Tx, scope tenancy.Scope, window Window, reason string) (int64, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return 0, s.failed(ctx, op.Error(ErrNilExecutor, "closing window"))
	}

	after, through, err := window.bounds()
	if err != nil {
		return 0, s.failed(ctx, op.Error(err, "closing window"))
	}

	if err = withinWriteAhead(window.To, s.now()); err != nil {
		return 0, s.failed(ctx, op.Error(err, "closing window"))
	}

	if err = withinWriteBehind(window.From, s.now()); err != nil {
		return 0, s.failed(ctx, op.Error(err, "closing window"))
	}

	if err = validateReason(reason); err != nil {
		return 0, s.failed(ctx, op.Error(err, "closing window"))
	}

	if err = scope.Validate(); err != nil {
		return 0, s.failed(ctx, op.Error(err, "closing window"))
	}

	for afterID := ""; ; {
		page, listErr := s.listSeries(ctx, tx, scope, afterID, MaxSeriesPerPage)
		if listErr != nil {
			return 0, s.failed(ctx, op.Error(listErr, "reading the series a window closes"))
		}

		for _, found := range page {
			if _, matErr := s.materialize(ctx, tx, found, window.To); matErr != nil {
				return 0, s.failed(ctx, op.Error(matErr, "writing out the series a window closes"))
			}
		}

		if len(page) < MaxSeriesPerPage {
			break
		}

		afterID = page[len(page)-1].ID
	}

	skipped, err := sweep(func() (int64, error) {
		return s.q.SkipWindow(ctx, tx, seriesdb.SkipWindowParams{
			State:        string(StateSkipped),
			Reason:       reason,
			Scope:        scope,
			After:        after,
			Through:      through,
			SkippedState: string(StateSkipped),
			ResultLimit:  sweepBatch,
		})
	})
	if err != nil {
		return 0, s.failed(ctx, op.Error(err, "closing window"))
	}

	op.Set(countKey, skipped)

	return skipped, nil
}

// DueSeries reads the series the horizon worker has to write forward. See
// Store.DueSeries.
func (s *SQLStore) DueSeries(ctx context.Context, q database.SQLQueryExecutor, horizon time.Time, limit int) ([]*Series, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "reading due series"))
	}

	if limit < 1 {
		return nil, s.failed(ctx, op.Error(platformerrors.Wrapf(ErrInvalidPageSize, "%d", limit), "reading due series"))
	}

	rows, err := s.q.DueSeries(ctx, q, seriesdb.DueSeriesParams{Horizon: stored(horizon), ResultLimit: int64(limit)})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading due series"))
	}

	due := make([]*Series, 0, len(rows))

	for i := range rows {
		row := seriesdb.GetSeriesRow(rows[i])

		converted, convErr := seriesFromRow(&row)
		if convErr != nil {
			return nil, s.failed(ctx, op.Error(convErr, "reading due series"))
		}

		due = append(due, converted)
	}

	op.SpanOnly(countKey, len(due))

	return due, nil
}

// readSeries reads one live series by id.
func (s *SQLStore) readSeries(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, id string) (*Series, error) {
	if id == "" {
		return nil, ErrSeriesNotFound
	}

	row, err := s.q.GetSeries(ctx, q, seriesdb.GetSeriesParams{ID: id, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrSeriesNotFound)
	}

	return seriesFromRow(&row)
}

// readOccurrence reads one live occurrence by id.
func (s *SQLStore) readOccurrence(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, id string) (*Occurrence, error) {
	if id == "" {
		return nil, ErrOccurrenceNotFound
	}

	row, err := s.q.GetOccurrence(ctx, q, seriesdb.GetOccurrenceParams{ID: id, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrOccurrenceNotFound)
	}

	return occurrenceFromRow(&row), nil
}

// sweep runs a bounded write until a pass comes back short of a full batch,
// and answers with how many rows the passes moved in all. Each pass's rows stop
// matching its predicate once written, so the next pass starts on the rows the
// last one did not reach.
func sweep(pass func() (int64, error)) (int64, error) {
	var total int64

	for {
		n, err := pass()
		if err != nil {
			return total, err
		}

		total += n

		if n < sweepBatch {
			return total, nil
		}
	}
}

// guardCount reads a guarded write's answer: an error is an error, and a write
// that moved nothing is the refusal sentinel rather than a success.
func guardCount(count int64, err, refused error) error {
	if err != nil {
		return err
	}

	if count == 0 {
		return refused
	}

	return nil
}

// notFound maps a driver's empty-result error onto the absent sentinel, leaving
// anything else alone — a read that found nothing and a read that failed are
// different answers.
func notFound(err, absent error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return absent
	}

	return err
}
