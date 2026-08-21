package filtering

import (
	"database/sql"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestBind(T *testing.T) {
	T.Parallel()

	exampleTime := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		filter := &QueryFilter{
			CreatedAfter:    new(exampleTime),
			CreatedBefore:   new(exampleTime.Add(time.Hour)),
			UpdatedAfter:    new(exampleTime.Add(2 * time.Hour)),
			UpdatedBefore:   new(exampleTime.Add(3 * time.Hour)),
			Cursor:          new("cursor_001"),
			MaxResponseSize: new(uint16(25)),
			IncludeArchived: new(true),
		}

		args := Bind(filter)

		test.EqOp(t, sql.NullTime{Time: *filter.CreatedAfter, Valid: true}, args.CreatedAfter)
		test.EqOp(t, sql.NullTime{Time: *filter.CreatedBefore, Valid: true}, args.CreatedBefore)
		test.EqOp(t, sql.NullTime{Time: *filter.UpdatedAfter, Valid: true}, args.UpdatedAfter)
		test.EqOp(t, sql.NullTime{Time: *filter.UpdatedBefore, Valid: true}, args.UpdatedBefore)
		test.EqOp(t, sql.NullString{String: "cursor_001", Valid: true}, args.Cursor)
		test.EqOp(t, sql.NullInt32{Int32: 25, Valid: true}, args.ResultLimit)
		test.EqOp(t, sql.NullBool{Bool: true, Valid: true}, args.IncludeArchived)
	})

	T.Run("with nil filter", func(t *testing.T) {
		t.Parallel()

		args := Bind(nil)

		// A nil filter is the default filter, which sets nothing but the page
		// size — every window is open and the first page is the one asked for.
		test.EqOp(t, sql.NullTime{}, args.CreatedAfter)
		test.EqOp(t, sql.NullTime{}, args.CreatedBefore)
		test.EqOp(t, sql.NullTime{}, args.UpdatedAfter)
		test.EqOp(t, sql.NullTime{}, args.UpdatedBefore)
		test.EqOp(t, sql.NullString{}, args.Cursor)
		test.EqOp(t, sql.NullBool{}, args.IncludeArchived)
		test.EqOp(t, sql.NullInt32{Int32: DefaultQueryFilterLimit, Valid: true}, args.ResultLimit)
	})

	T.Run("with empty filter", func(t *testing.T) {
		t.Parallel()

		// Distinct from the nil case: this filter exists and set nothing, which
		// still has to reach the driver as a usable page size rather than as a
		// NULL the one dialect that cannot coalesce would answer with no rows.
		args := Bind(&QueryFilter{})

		test.EqOp(t, sql.NullInt32{Int32: DefaultQueryFilterLimit, Valid: true}, args.ResultLimit)
		test.EqOp(t, sql.NullBool{}, args.IncludeArchived)
	})

	T.Run("clamping an over-large page size", func(t *testing.T) {
		t.Parallel()

		// The clamp lands before the narrowing to int32, which is the ordering
		// that matters: a value narrowed first is a legible-looking wrong
		// answer.
		args := Bind(&QueryFilter{MaxResponseSize: new(uint16(60_000))})

		test.EqOp(t, sql.NullInt32{Int32: MaxQueryFilterLimit, Valid: true}, args.ResultLimit)
	})

	T.Run("with a page size at the ceiling", func(t *testing.T) {
		t.Parallel()

		args := Bind(&QueryFilter{MaxResponseSize: new(uint16(MaxQueryFilterLimit))})

		test.EqOp(t, sql.NullInt32{Int32: MaxQueryFilterLimit, Valid: true}, args.ResultLimit)
	})

	T.Run("preserving an explicit zero page size", func(t *testing.T) {
		t.Parallel()

		// A zero a caller set is loud — no rows — rather than quietly becoming a
		// page of some other size. Only absence is defaulted here.
		args := Bind(&QueryFilter{MaxResponseSize: new(uint16(0))})

		test.EqOp(t, sql.NullInt32{Int32: 0, Valid: true}, args.ResultLimit)
	})

	T.Run("with archived rows explicitly excluded", func(t *testing.T) {
		t.Parallel()

		// False and absent are different values here, and the difference is the
		// one a hand-written params literal loses by omitting the field.
		args := Bind(&QueryFilter{IncludeArchived: new(false)})

		test.EqOp(t, sql.NullBool{Bool: false, Valid: true}, args.IncludeArchived)
	})
}

func TestBindValues(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		filter := &QueryFilter{Cursor: new("cursor_001"), MaxResponseSize: new(uint16(10))}

		args := Bind(filter)
		values := BindValues(filter)

		test.EqOp(t, any(args.CreatedAfter), values[ArgCreatedAfter])
		test.EqOp(t, any(args.CreatedBefore), values[ArgCreatedBefore])
		test.EqOp(t, any(args.UpdatedAfter), values[ArgUpdatedAfter])
		test.EqOp(t, any(args.UpdatedBefore), values[ArgUpdatedBefore])
		test.EqOp(t, any(args.Cursor), values[ArgCursor])
		test.EqOp(t, any(args.ResultLimit), values[ArgResultLimit])
		test.EqOp(t, any(args.IncludeArchived), values[ArgIncludeArchived])
	})

	T.Run("naming every argument and no others", func(t *testing.T) {
		t.Parallel()

		expected := []string{
			ArgCreatedAfter,
			ArgCreatedBefore,
			ArgCursor,
			ArgIncludeArchived,
			ArgResultLimit,
			ArgUpdatedAfter,
			ArgUpdatedBefore,
		}

		got := slices.Sorted(maps.Keys(BindValues(nil)))

		test.Eq(t, expected, got)
	})

	T.Run("handing over a copy", func(t *testing.T) {
		t.Parallel()

		// A keyed read's own match columns go into this map beside the filter's
		// arguments, so a caller that edits one must not be editing everyone's.
		values := BindValues(nil)
		values["referenced_id"] = "whatever"

		test.MapNotContainsKey(t, BindValues(nil), "referenced_id")
	})
}

// drainRow is a stand-in for the row struct a query generator emits: the
// columns, plus the two windowed counts riding along on every row.
type drainRow struct {
	id                        string
	filteredCount, totalCount int64
}

func drainCounts(r drainRow) (filtered, total int64) { return r.filteredCount, r.totalCount }

func drainConvert(r drainRow) *string { return &r.id }

func drainID(s *string) string { return *s }

func TestDrain(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		rows := []drainRow{
			{id: "a", filteredCount: 2, totalCount: 7},
			{id: "b", filteredCount: 2, totalCount: 7},
		}

		filter := &QueryFilter{Cursor: new("start"), MaxResponseSize: new(uint16(2))}

		actual := Drain(rows, drainConvert, drainCounts, drainID, filter)

		test.Eq(t, []*string{new("a"), new("b")}, actual.Data)

		filtered, total, known := actual.Counts()
		test.True(t, known)
		test.EqOp(t, uint64(2), filtered)
		test.EqOp(t, uint64(7), total)

		// The cursor reaching the next page is the last row's identifier; the
		// one that reached this page is echoed back.
		test.EqOp(t, "b", actual.Cursor)
		test.EqOp(t, "start", actual.PreviousCursor)
		test.EqOp(t, filter, actual.AppliedQueryFilter)
	})

	T.Run("taking the counts from the first row", func(t *testing.T) {
		t.Parallel()

		// Every row carries the same windowed count, so a loop that reassigns
		// per row is right by accident. Rows that disagree are the only way to
		// tell which end of the page was read, and the first is the one that
		// exists on a page of one.
		rows := []drainRow{
			{id: "a", filteredCount: 3, totalCount: 9},
			{id: "b", filteredCount: 99, totalCount: 99},
		}

		filtered, total, known := Drain(rows, drainConvert, drainCounts, drainID, nil).Counts()

		test.True(t, known)
		test.EqOp(t, uint64(3), filtered)
		test.EqOp(t, uint64(9), total)
	})

	T.Run("with no rows", func(t *testing.T) {
		t.Parallel()

		actual := Drain(nil, drainConvert, drainCounts, drainID, nil)

		// An empty page has no row to read a count off, so it reports unknown
		// rather than a zero that reads as "nothing matched".
		filtered, total, known := actual.Counts()
		test.False(t, known)
		test.EqOp(t, uint64(0), filtered)
		test.EqOp(t, uint64(0), total)

		// The empty page is an empty slice rather than a nil one, so the JSON
		// shape of an empty page does not depend on which store answered.
		must.NotNil(t, actual.Data)
		test.SliceEmpty(t, actual.Data)
		test.EqOp(t, "", actual.Cursor)
	})

	T.Run("with no counts on the rows", func(t *testing.T) {
		t.Parallel()

		rows := []drainRow{{id: "a"}}

		actual := Drain(rows, drainConvert, nil, drainID, nil)

		_, _, known := actual.Counts()
		test.False(t, known)
		test.Eq(t, []*string{new("a")}, actual.Data)
		test.EqOp(t, "a", actual.Cursor)
	})

	T.Run("with a count no database could have produced", func(t *testing.T) {
		t.Parallel()

		// Reporting zero for a negative count is the smaller lie: the
		// conversion to the unsigned pair would otherwise report more rows than
		// a 64-bit address space holds.
		rows := []drainRow{{id: "a", filteredCount: -1, totalCount: -1}}

		filtered, total, known := Drain(rows, drainConvert, drainCounts, drainID, nil).Counts()

		test.True(t, known)
		test.EqOp(t, uint64(0), filtered)
		test.EqOp(t, uint64(0), total)
	})

	T.Run("with a nil filter", func(t *testing.T) {
		t.Parallel()

		actual := Drain([]drainRow{{id: "a", filteredCount: 1, totalCount: 1}}, drainConvert, drainCounts, drainID, nil)

		// A nil filter reads as the default filter everywhere else in this
		// package, and the page it answers says so.
		test.EqOp(t, uint16(DefaultQueryFilterLimit), actual.MaxResponseSize)
		test.Nil(t, actual.AppliedQueryFilter)
	})
}
