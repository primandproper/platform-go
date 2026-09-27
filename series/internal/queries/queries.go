package queries

import (
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// The two tables this package owns, at their canonical, unprefixed spelling —
// what the emitted .sql names, and what the store's own prefix rendering starts
// from.
const (
	// SeriesTable holds the rules.
	SeriesTable = "series"
	// OccurrencesTable holds the instances the rules imply, and the
	// replacements added against skipped ones.
	OccurrencesTable = "series_occurrences"
)

// TableNames is every table this package owns, in the order the DDL creates
// them.
var TableNames = []string{SeriesTable, OccurrencesTable}

// ScopeColumn is the tenancy dimension both tables carry and every consumer
// statement is keyed on. It is a column, not a convention: an unscoped consumer
// read of this schema is not expressible, because there is no such statement.
// The one statement that omits it is the horizon worker's, which is the
// component servicing itself — see [DueSeriesQuery].
const ScopeColumn = "scope"

// The series table's own columns. Exported because the store spells them too,
// and two spellings of one column is the drift this package exists to prevent.
const (
	// TimeZoneColumn is the IANA zone the rule's wall-clock facts are read in.
	TimeZoneColumn = "time_zone"
	// WeekdayColumn is the day of the week, as Go's time.Weekday counts.
	WeekdayColumn = "weekday"
	// StartMinuteColumn is minutes past local midnight.
	StartMinuteColumn = "start_minute"
	// IntervalWeeksColumn is how many weeks apart two occurrences are.
	IntervalWeeksColumn = "interval_weeks"
	// StartsOnColumn is the first date an occurrence may fall on.
	StartsOnColumn = "starts_on"
	// EndsOnColumn is the first date no occurrence falls on, empty for a rule
	// with no end.
	EndsOnColumn = "ends_on"
	// MaterializedUntilColumn is how far the rule has been written out.
	MaterializedUntilColumn = "materialized_until"
	// ExhaustedAtColumn is when a pass found nothing left to write.
	ExhaustedAtColumn = "exhausted_at"
)

// The occurrence table's own columns.
const (
	// SeriesIDColumn is the series an occurrence belongs to.
	SeriesIDColumn = "series_id"
	// ScheduledAtColumn is when the occurrence happens.
	ScheduledAtColumn = "scheduled_at"
	// SlotAtColumn is when the rule said it would, NULL for a replacement.
	SlotAtColumn = "slot_at"
	// StateColumn is scheduled, skipped or moved.
	StateColumn = "state"
	// ReplacedByColumn names the occurrence added in a skipped one's place.
	ReplacedByColumn = "replaced_by"
	// ReasonColumn is the consumer's free text for a skip, a move or a
	// closure.
	ReasonColumn = "reason"
)

// SeriesColumns is the series table's full shape, in the order the emitted
// SELECTs project it.
var SeriesColumns = []string{
	querygen.IDColumn,
	ScopeColumn,
	TimeZoneColumn,
	WeekdayColumn,
	StartMinuteColumn,
	IntervalWeeksColumn,
	StartsOnColumn,
	EndsOnColumn,
	MaterializedUntilColumn,
	ExhaustedAtColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
}

// OccurrenceColumns is the occurrence table's full shape, in the order the
// emitted SELECTs project it.
var OccurrenceColumns = []string{
	querygen.IDColumn,
	ScopeColumn,
	SeriesIDColumn,
	ScheduledAtColumn,
	SlotAtColumn,
	StateColumn,
	ReplacedByColumn,
	ReasonColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
}

// The query names the generated querier's methods are built from.
const (
	CreateSeriesQuery          = "CreateSeries"
	GetSeriesQuery             = "GetSeries"
	ListSeriesQuery            = "ListSeries"
	EndSeriesQuery             = "EndSeries"
	AdvanceSeriesQuery         = "AdvanceSeries"
	DueSeriesQuery             = "DueSeries"
	MaterializeOccurrenceQuery = "MaterializeOccurrence"
	CreateOccurrenceQuery      = "CreateOccurrence"
	GetOccurrenceQuery         = "GetOccurrence"
	ListOccurrencesQuery       = "ListOccurrences"
	ListSeriesOccurrencesQuery = "ListSeriesOccurrences"
	SkipOccurrenceQuery        = "SkipOccurrence"
	MoveOccurrenceQuery        = "MoveOccurrence"
	LinkReplacementQuery       = "LinkReplacement"
	SkipWindowQuery            = "SkipWindow"
	SkipSeriesFromQuery        = "SkipSeriesFrom"
)

// The arguments named apart from the column they bind against.
const (
	// AfterIDArg is the keyset a series listing resumes after.
	AfterIDArg = "after_id"
	// ThroughArg is the ceiling an advance never moves materialized_until
	// back past, and the inclusive end of a window read.
	ThroughArg = "through"
	// HorizonArg is the instant a series written out no further than is due.
	HorizonArg = "horizon"
	// AfterArg is the exclusive start of a window.
	AfterArg = "after"
	// SlotAfterArg is the exclusive lower bound on a slot an end skips from.
	SlotAfterArg = "slot_after"
	// SkippedStateArg is the state a guarded write refuses to act on. It is not
	// state because the same statements assign that column, and one argument
	// name would require the column to already hold the value being written.
	SkippedStateArg = "skipped_state"
	// ExpectedStateArg is the state a replacement's link requires the skipped
	// occurrence to hold.
	ExpectedStateArg = "expected_state"
)

// nullableSeries is the one series column a write assigns that may be absent: a
// rule not yet exhausted has no stamp.
var nullableSeries = []string{ExhaustedAtColumn}

// nullableOccurrence is the one occurrence column a write assigns that may be
// absent: a replacement has no slot.
var nullableOccurrence = []string{SlotAtColumn}

// window is the pair of predicates a window read and the closure key on:
// scheduled_at after one bound and at or before the other.
//
// It is (after, through] rather than [from, to) because those are the two
// comparands querygen's closed set can spell — a bound ceiling and its
// complement. The store translates: every instant it stores is whole seconds,
// so [from, to) over whole seconds is (from − 1s, to − 1s] once each bound is
// rounded up to the second. See the store's windowBounds.
func window() []querygen.Match {
	return []querygen.Match{
		{Column: ScheduledAtColumn, Arg: AfterArg, Against: querygen.AtMostArgument, Exclude: true},
		{Column: ScheduledAtColumn, Arg: ThroughArg, Against: querygen.AtMostArgument},
	}
}

// notSkipped is the guard every write that acts on an occurrence carries: a
// skipped one is not skipped again, moved, or swept into a closure a second
// time, and the zero the write reports is how the store learns it lost a race.
var notSkipped = querygen.Match{Column: StateColumn, Arg: SkippedStateArg, Exclude: true}

// byScheduledAt is the order occurrences are read and swept in: when they
// happen, the id breaking a tie.
var byScheduledAt = []querygen.Order{{Column: ScheduledAtColumn}, {Column: querygen.IDColumn}}

// Render returns the canonical sqlc input for d: the sixteen statements this
// store executes, in one file's worth of text.
//
// There is no StandardCRUD call. Neither table pages by the filter window — a
// series listing resumes after an id and an occurrence listing is a window of
// time — and every write here is narrower than the standard set's: the
// materializing insert ignores a slot already written, the state changes are
// guarded, and the closure and the end are bounded sweeps.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		createSeries(g),
		getSeries(g),
		listSeries(g),
		endSeries(g),
		advanceSeries(g),
		dueSeries(g),
		materializeOccurrence(g),
		createOccurrence(g),
		getOccurrence(g),
		listOccurrences(g),
		listSeriesOccurrences(g),
		skipOccurrence(g),
		moveOccurrence(g),
		linkReplacement(g),
		skipWindow(g),
		skipSeriesFrom(g),
	})
}

// createSeries writes a new rule.
func createSeries(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery(CreateSeriesQuery, SeriesTable, querygen.ForInsert(SeriesColumns), nullableSeries)
}

// getSeries is the keyed get: one live series of the scope, by id.
func getSeries(g *querygen.Generator) *querygen.Query {
	return g.GetQuery(GetSeriesQuery, SeriesTable, SeriesColumns,
		querygen.Match{Column: ScopeColumn})
}

// listSeries is a page of a scope's series in id order, resuming after the id a
// caller last saw. It is a bounded scan keyed on the id's complement rather than
// the filter-window listing, because nothing about a rule is a creation-time
// range a caller would narrow by, and a keyset over the id is the whole of what
// a caller walking every series needs.
func listSeries(g *querygen.Generator) *querygen.Query {
	return g.SweepQuery(ListSeriesQuery, SeriesTable, SeriesColumns,
		querygen.Sweep{Order: []querygen.Order{{Column: querygen.IDColumn}}},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: querygen.IDColumn, Arg: AfterIDArg, Against: querygen.AtMostArgument, Exclude: true},
	)
}

// endSeries writes a rule's end date.
func endSeries(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(EndSeriesQuery, SeriesTable, SeriesColumns, []string{EndsOnColumn}, nil,
		querygen.Match{Column: ScopeColumn})
}

// advanceSeries records how far a rule has been written out, and whether that
// was the last of it.
//
// It never moves materialized_until backwards: the predicate requires the
// column to be at or below the value being written, so a pass that computed a
// nearer horizon than one already committed writes nothing rather than
// re-opening slots the unique index would only refuse again.
func advanceSeries(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(AdvanceSeriesQuery, SeriesTable, SeriesColumns,
		[]string{MaterializedUntilColumn, ExhaustedAtColumn}, nullableSeries,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: MaterializedUntilColumn, Arg: ThroughArg, Against: querygen.AtMostArgument},
	)
}

// dueSeries is the horizon worker's read: live series not yet exhausted and not
// yet written out to the horizon, least far written first.
//
// It is the one statement here that names no scope, and deliberately. It is the
// component servicing itself — a worker writing every tenant's rules forward on
// a timer — not a consumer read, and each row it returns carries the scope the
// materializing write then binds.
func dueSeries(g *querygen.Generator) *querygen.Query {
	return g.SweepQuery(DueSeriesQuery, SeriesTable, SeriesColumns,
		querygen.Sweep{Order: []querygen.Order{{Column: MaterializedUntilColumn}, {Column: querygen.IDColumn}}},
		querygen.Match{Column: ExhaustedAtColumn, Against: querygen.NoValue},
		querygen.Match{Column: MaterializedUntilColumn, Arg: HorizonArg, Against: querygen.AtMostArgument},
	)
}

// materializeOccurrence writes one slot a rule implies, unless the slot already
// has a row. The unique index on (series_id, slot_at) is the conflict target, so
// a slot written twice — by two replicas, or by a closure and then the worker —
// is one row, and the count says which call wrote it.
func materializeOccurrence(g *querygen.Generator) *querygen.Query {
	return g.InsertIgnoreQuery(MaterializeOccurrenceQuery, OccurrencesTable,
		querygen.ForInsert(OccurrenceColumns), nullableOccurrence,
		querygen.Match{Column: SeriesIDColumn},
		querygen.Match{Column: SlotAtColumn},
	)
}

// createOccurrence writes a replacement: an occurrence no rule implies, so it
// has no slot and nothing for an insert to collide with.
func createOccurrence(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery(CreateOccurrenceQuery, OccurrencesTable, querygen.ForInsert(OccurrenceColumns), nullableOccurrence)
}

// getOccurrence is the keyed get: one live occurrence of the scope, by id.
func getOccurrence(g *querygen.Generator) *querygen.Query {
	return g.GetQuery(GetOccurrenceQuery, OccurrencesTable, OccurrenceColumns,
		querygen.Match{Column: ScopeColumn})
}

// listOccurrences is the week view: a scope's occurrences in a window of time,
// in the order they happen, every state included.
func listOccurrences(g *querygen.Generator) *querygen.Query {
	return g.SweepQuery(ListOccurrencesQuery, OccurrencesTable, OccurrenceColumns,
		querygen.Sweep{Order: byScheduledAt},
		append([]querygen.Match{{Column: ScopeColumn}}, window()...)...,
	)
}

// listSeriesOccurrences is one series' occurrences in a window of time.
func listSeriesOccurrences(g *querygen.Generator) *querygen.Query {
	return g.SweepQuery(ListSeriesOccurrencesQuery, OccurrencesTable, OccurrenceColumns,
		querygen.Sweep{Order: byScheduledAt},
		append([]querygen.Match{{Column: ScopeColumn}, {Column: SeriesIDColumn}}, window()...)...,
	)
}

// skipOccurrence marks one occurrence skipped, with the consumer's reason.
func skipOccurrence(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(SkipOccurrenceQuery, OccurrencesTable, OccurrenceColumns,
		[]string{StateColumn, ReasonColumn}, nil,
		querygen.Match{Column: ScopeColumn}, notSkipped)
}

// moveOccurrence gives one occurrence a new instant and marks it moved. slot_at
// is not assigned, which is what keeps the rule's slot occupied by the row that
// left it.
func moveOccurrence(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(MoveOccurrenceQuery, OccurrencesTable, OccurrenceColumns,
		[]string{ScheduledAtColumn, StateColumn, ReasonColumn}, nil,
		querygen.Match{Column: ScopeColumn}, notSkipped)
}

// linkReplacement points a skipped occurrence at the one added in its place,
// provided it is still skipped and has no replacement yet. Both halves are the
// guard: a second replacement against one skip is refused rather than
// overwriting the first one's link.
func linkReplacement(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(LinkReplacementQuery, OccurrencesTable, OccurrenceColumns,
		[]string{ReplacedByColumn}, nil,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: StateColumn, Arg: ExpectedStateArg},
		querygen.Match{Column: ReplacedByColumn, Against: querygen.EmptyString},
	)
}

// skipWindow is the closure: every occurrence of the scope happening in a window,
// skipped, a bounded batch at a time.
func skipWindow(g *querygen.Generator) *querygen.Query {
	return g.SweepUpdateQuery(SkipWindowQuery, OccurrencesTable, OccurrenceColumns,
		[]string{StateColumn, ReasonColumn}, nil, byScheduledAt,
		append(append([]querygen.Match{{Column: ScopeColumn}}, window()...), notSkipped)...,
	)
}

// skipSeriesFrom is an end's sweep: every occurrence a series' rule put at or
// after the end, skipped, a bounded batch at a time.
//
// It keys on slot_at rather than scheduled_at, because what an end removes is
// the rule's instances from a date on. An occurrence moved out of that range
// was still implied by the rule inside it and goes; one moved into it from
// before stays. A replacement has no slot, and NULL compares as nothing, so an
// end never removes a make-up somebody added by hand.
func skipSeriesFrom(g *querygen.Generator) *querygen.Query {
	return g.SweepUpdateQuery(SkipSeriesFromQuery, OccurrencesTable, OccurrenceColumns,
		[]string{StateColumn, ReasonColumn}, nil,
		[]querygen.Order{{Column: SlotAtColumn}, {Column: querygen.IDColumn}},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SeriesIDColumn},
		querygen.Match{Column: SlotAtColumn, Arg: SlotAfterArg, Against: querygen.AtMostArgument, Exclude: true},
		notSkipped,
	)
}

// FileName is the file one dialect's rendered queries are committed to.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}
