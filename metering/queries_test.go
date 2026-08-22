package metering

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is every dialect the builders render for.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// numberedPlaceholder matches Postgres's $N.
var numberedPlaceholder = regexp.MustCompile(`\$(\d+)`)

// assertBindsCleanly checks the one invariant every builder in this file has to
// hold: the query binds exactly as many values as it was handed, in a contiguous
// run starting at one.
//
// It exists because the failure it catches is invisible at the call site and
// silent at runtime on at least one driver. A builder that hands over one value
// too many shifts every subsequent binding by one, so an UPDATE's WHERE clause
// compares a subject against a timestamp — and matches nothing, forever, without
// erroring. That bug shipped into this file once during development and was found
// by a behavioral test three layers up, which is three layers further than it
// should have had to travel.
func assertBindsCleanly(t *testing.T, d dialect.Dialect, query string, args []any) {
	t.Helper()

	if d == dialect.Postgres {
		matches := numberedPlaceholder.FindAllStringSubmatch(query, -1)

		seen := map[int]bool{}
		for _, match := range matches {
			n, err := strconv.Atoi(match[1])
			must.NoError(t, err)
			seen[n] = true
		}

		for n := 1; n <= len(args); n++ {
			test.True(t, seen[n], test.Sprintf("query %q is missing placeholder $%d", query, n))
		}

		test.EqOp(t, len(args), len(seen),
			test.Sprintf("query %q binds %d distinct placeholders for %d args", query, len(seen), len(args)))

		return
	}

	test.EqOp(t, len(args), strings.Count(query, "?"),
		test.Sprintf("query %q binds %d markers for %d args", query, strings.Count(query, "?"), len(args)))
}

func TestQueries_BindCleanly(T *testing.T) {
	T.Parallel()

	tbl := newTables("mtr")
	total := &Total{
		Subject: testSubject, Meter: testMeter,
		PeriodStart: monthBounds.Start, PeriodEnd: monthBounds.End,
		Quantity: 100, FlushedQuantity: 40, FlushSequence: 3,
	}
	keys := []totalKey{
		{subject: "a", meter: testMeter, periodStart: monthBounds.Start},
		{subject: "b", meter: testMeter, periodStart: monthBounds.Start},
	}

	for _, d := range allDialects {
		builders := map[string]func() (string, []any){
			"insert_event": func() (string, []any) {
				entry := newEntry("req-1", 5, AggregationSum)

				return tbl.buildInsertEvent(d, &entry, nil, baseTime)
			},
			"insert_zero_total": func() (string, []any) {
				return tbl.buildInsertZeroTotal(d, testSubject, testMeter, AggregationSum, monthBounds, baseTime)
			},
			"select_total": func() (string, []any) {
				return tbl.buildSelectTotal(d, testSubject, testMeter, monthBounds.Start, true)
			},
			"apply_consume": func() (string, []any) {
				return tbl.buildApplyConsume(d, testSubject, testMeter, monthBounds.Start, 10, baseTime, baseTime)
			},
			"select_flushable": func() (string, []any) {
				return tbl.buildSelectFlushable(d, baseTime, 10, 5, true)
			},
			"claim_flushable": func() (string, []any) {
				return tbl.buildClaimFlushable(d, keys, baseTime)
			},
			"fetch_totals": func() (string, []any) {
				return tbl.buildFetchTotalsByKey(d, keys)
			},
			"mark_flushed": func() (string, []any) {
				return tbl.buildMarkFlushed(d, total, 100, baseTime)
			},
			"release_flush": func() (string, []any) {
				return tbl.buildReleaseFlush(d, total, "boom", baseTime, baseTime)
			},
			"reap_events": func() (string, []any) {
				return tbl.buildReapEvents(d, baseTime, 100)
			},
		}

		for _, aggregation := range []Aggregation{AggregationSum, AggregationMax, AggregationLast, AggregationUniqueCount, "median"} {
			builders["upsert_total_"+string(aggregation)] = func() (string, []any) {
				return tbl.buildUpsertTotal(d, testSubject, testMeter, aggregation, monthBounds, 5, baseTime, baseTime)
			}
		}

		for name, build := range builders {
			T.Run(string(d)+"/"+name, func(t *testing.T) {
				t.Parallel()

				query, args := build()

				must.StrNotEqFold(t, "", query)
				assertBindsCleanly(t, d, query, args)
				test.StrNotContains(t, query, "{{")
			})
		}
	}
}

func TestQueries_DialectSpelling(T *testing.T) {
	T.Parallel()

	tbl := newTables("mtr")

	T.Run("conflict-ignore is spelled per dialect", func(t *testing.T) {
		t.Parallel()

		entry := newEntry("req-1", 1, AggregationSum)

		pg, _ := tbl.buildInsertEvent(dialect.Postgres, &entry, nil, baseTime)
		test.StrContains(t, pg, "ON CONFLICT (meter, idempotency_key) DO NOTHING")
		test.StrNotContains(t, pg, "IGNORE")

		my, _ := tbl.buildInsertEvent(dialect.MySQL, &entry, nil, baseTime)
		test.StrContains(t, my, "INSERT IGNORE INTO")
		test.StrNotContains(t, my, "ON CONFLICT")

		lite, _ := tbl.buildInsertEvent(dialect.SQLite, &entry, nil, baseTime)
		test.StrContains(t, lite, "INSERT OR IGNORE INTO")
		test.StrNotContains(t, lite, "ON CONFLICT")
	})

	T.Run("the zero-total insert ignores a conflict in every dialect", func(t *testing.T) {
		t.Parallel()

		// Two writers can reach this insert for the same period at once, and the
		// row they would both create is a zero — so the loser has to be ignored
		// rather than reported. Postgres needs the trailing clause spelled out
		// because its ignore verb is not in the INSERT itself; the other two
		// already said it in the prefix, and repeating it there would be a
		// syntax error rather than a redundancy.
		pg, _ := tbl.buildInsertZeroTotal(dialect.Postgres, testSubject, testMeter,
			AggregationSum, monthBounds, baseTime)
		test.StrContains(t, pg, "ON CONFLICT ("+totalKeyColumns+") DO NOTHING")

		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite} {
			query, _ := tbl.buildInsertZeroTotal(d, testSubject, testMeter,
				AggregationSum, monthBounds, baseTime)

			test.StrContains(t, query, "IGNORE INTO")
			test.StrNotContains(t, query, "ON CONFLICT")
		}
	})

	T.Run("an unknown dialect renders no ignore verb", func(t *testing.T) {
		t.Parallel()

		// Unreachable through the constructor, which vets the dialect. Asserted so
		// that adding a dialect cannot silently produce an INSERT with no conflict
		// handling, which would fail loudly on the first retry rather than dedupe.
		test.EqOp(t, "", ignorePrefix("oracle"))
	})

	T.Run("upsert is spelled per dialect", func(t *testing.T) {
		t.Parallel()

		pg, _ := tbl.buildUpsertTotal(dialect.Postgres, testSubject, testMeter, AggregationSum, monthBounds, 5, baseTime, baseTime)
		test.StrContains(t, pg, "ON CONFLICT (subject, meter, period_start) DO UPDATE SET")
		test.StrContains(t, pg, "mtr_metering_totals.quantity + excluded.quantity")

		my, _ := tbl.buildUpsertTotal(dialect.MySQL, testSubject, testMeter, AggregationSum, monthBounds, 5, baseTime, baseTime)
		test.StrContains(t, my, "ON DUPLICATE KEY UPDATE")
		test.StrContains(t, my, "quantity = quantity + VALUES(quantity)")

		lite, _ := tbl.buildUpsertTotal(dialect.SQLite, testSubject, testMeter, AggregationSum, monthBounds, 5, baseTime, baseTime)
		test.StrContains(t, lite, "ON CONFLICT (subject, meter, period_start) DO UPDATE SET")
	})

	T.Run("two-argument maximum is spelled per dialect", func(t *testing.T) {
		t.Parallel()

		// SQLite's scalar MAX is the same function the other two call GREATEST;
		// SQLite's one-argument MAX is the aggregate, and is not what this is.
		test.EqOp(t, "GREATEST", greatestFunc(dialect.Postgres))
		test.EqOp(t, "GREATEST", greatestFunc(dialect.MySQL))
		test.EqOp(t, "MAX", greatestFunc(dialect.SQLite))
	})

	T.Run("row locks only where they exist", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL} {
			query, _ := tbl.buildSelectTotal(d, testSubject, testMeter, monthBounds.Start, true)
			test.StrHasSuffix(t, " FOR UPDATE", query, test.Sprintf("dialect %q", d))
			test.True(t, supportsRowLock(d), test.Sprintf("dialect %q", d))
		}

		// SQLite has no FOR UPDATE and needs none: it admits one writer at a time
		// by construction.
		lite, _ := tbl.buildSelectTotal(dialect.SQLite, testSubject, testMeter, monthBounds.Start, true)
		test.StrNotContains(t, lite, "FOR UPDATE")
		test.False(t, supportsRowLock(dialect.SQLite))

		unlocked, _ := tbl.buildSelectTotal(dialect.Postgres, testSubject, testMeter, monthBounds.Start, false)
		test.StrNotContains(t, unlocked, "FOR UPDATE")
	})

	T.Run("skip locked only where it exists", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL} {
			query, _ := tbl.buildSelectFlushable(d, baseTime, 10, 5, true)
			test.StrHasSuffix(t, " FOR UPDATE SKIP LOCKED", query, test.Sprintf("dialect %q", d))
		}

		lite, _ := tbl.buildSelectFlushable(dialect.SQLite, baseTime, 10, 5, true)
		test.StrNotContains(t, lite, "FOR UPDATE")

		unlocked, _ := tbl.buildSelectFlushable(dialect.Postgres, baseTime, 10, 5, false)
		test.StrNotContains(t, unlocked, "FOR UPDATE")
	})

	T.Run("MySQL materializes a self-referential subquery", func(t *testing.T) {
		t.Parallel()

		// MySQL refuses a subquery that reads the table being modified
		// (ER_UPDATE_TABLE_USED) and accepts it once materialized through a
		// derived table. SQLite accepts either form, so only a real MySQL — or
		// this — can tell us.
		my, _ := tbl.buildReapEvents(dialect.MySQL, baseTime, 100)
		test.StrContains(t, my, "AS doomed")

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			query, _ := tbl.buildReapEvents(d, baseTime, 100)
			test.StrNotContains(t, query, "AS doomed", test.Sprintf("dialect %q", d))
		}
	})
}

func TestQueries_Guards(T *testing.T) {
	T.Parallel()

	tbl := newTables("mtr")
	total := &Total{
		Subject: testSubject, Meter: testMeter,
		PeriodStart: monthBounds.Start, FlushSequence: 3,
	}

	T.Run("settling is guarded on the sequence", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			marked, markArgs := tbl.buildMarkFlushed(d, total, 100, baseTime)
			test.StrContains(t, marked, "flush_sequence = "+d.Placeholder(7), test.Sprintf("dialect %q", d))
			test.EqOp(t, 3, markArgs[len(markArgs)-1], test.Sprintf("dialect %q", d))

			released, releaseArgs := tbl.buildReleaseFlush(d, total, "boom", baseTime, baseTime)
			test.StrContains(t, released, "flush_sequence = "+d.Placeholder(7), test.Sprintf("dialect %q", d))
			test.EqOp(t, 3, releaseArgs[len(releaseArgs)-1], test.Sprintf("dialect %q", d))
		}
	})

	T.Run("a release leaves the flushed quantity alone", func(t *testing.T) {
		t.Parallel()

		// The post may have reached the provider and failed on the way back, so
		// the next attempt has to carry the same delta under the same sequence.
		for _, d := range allDialects {
			query, _ := tbl.buildReleaseFlush(d, total, "boom", baseTime, baseTime)
			test.StrNotContains(t, query, "flushed_quantity", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, query, "flush_sequence = flush_sequence", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("claiming repeats the flushable guard", func(t *testing.T) {
		t.Parallel()

		// The rows were selected as flushable, but between the SELECT and this
		// UPDATE another flusher's settle may have moved them.
		for _, d := range allDialects {
			query, _ := tbl.buildClaimFlushable(d, []totalKey{
				{subject: "a", meter: testMeter, periodStart: monthBounds.Start},
			}, baseTime)

			test.StrContains(t, query, "quantity > flushed_quantity", test.Sprintf("dialect %q", d))
			test.StrContains(t, query, "flush_attempts = flush_attempts + 1", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("reaping spares an unflushed period", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, _ := tbl.buildReapEvents(d, baseTime, 100)

			test.StrContains(t, query, "NOT EXISTS", test.Sprintf("dialect %q", d))
			test.StrContains(t, query, "t.quantity > t.flushed_quantity", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("the last aggregation guards on the event time", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, _ := tbl.buildUpsertTotal(d, testSubject, testMeter, AggregationLast, monthBounds, 5, baseTime, baseTime)

			test.StrContains(t, query, "CASE WHEN", test.Sprintf("dialect %q", d))
			test.StrContains(t, query, "last_occurred_at", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("last_occurred_at only moves forward", func(t *testing.T) {
		t.Parallel()

		for _, aggregation := range []Aggregation{AggregationSum, AggregationMax, AggregationLast} {
			for _, d := range allDialects {
				query, _ := tbl.buildUpsertTotal(d, testSubject, testMeter, aggregation, monthBounds, 5, baseTime, baseTime)

				test.StrContains(t, query, "last_occurred_at = "+greatestFunc(d)+"(",
					test.Sprintf("dialect %q aggregation %q", d, aggregation))
			}
		}
	})
}

func TestTables(T *testing.T) {
	T.Parallel()

	tbl := newTables("custom")

	// Derived from one prefix, so adding a third table later cannot introduce an
	// inconsistently named one.
	test.EqOp(T, "custom", tbl.prefix())
	test.EqOp(T, "custom_metering_events", tbl.events)
	test.EqOp(T, "custom_metering_totals", tbl.totals)
}

// The arithmetic in the args slices' capacity hints is not asserted here and
// cannot be: a capacity is a hint to the allocator, and a builder that sized its
// slice wrongly produces the same query and the same arguments after one more
// growth. Mutation reports naming those expressions are naming equivalent
// mutants; what this file asserts instead is the rendering and the arguments,
// which is everything a caller and a driver can observe.
func TestKeyTuples(T *testing.T) {
	T.Parallel()

	keys := []totalKey{
		{subject: "a", meter: "m", periodStart: monthBounds.Start},
		{subject: "b", meter: "m", periodStart: monthBounds.Start},
	}

	T.Run("renders row values from the given offset", func(t *testing.T) {
		t.Parallel()

		tuples, args := keyTuples(dialect.Postgres, keys, 4)

		test.EqOp(t, "($4, $5, $6), ($7, $8, $9)", tuples)
		test.SliceLen(t, 6, args)
		test.Eq(t, []any{"a", "m", monthBounds.Start}, args[:3])
	})

	T.Run("renders positional markers for the others", func(t *testing.T) {
		t.Parallel()

		tuples, args := keyTuples(dialect.SQLite, keys, 1)

		test.EqOp(t, "(?, ?, ?), (?, ?, ?)", tuples)
		test.SliceLen(t, 6, args)
	})

	T.Run("handles an empty set", func(t *testing.T) {
		t.Parallel()

		tuples, args := keyTuples(dialect.Postgres, nil, 1)

		test.EqOp(t, "", tuples)
		test.SliceEmpty(t, args)
	})
}

func TestBlobOrNilAndEncodeDimensions(T *testing.T) {
	T.Parallel()

	// Nil and empty collapse deliberately: they say the same thing, and storing
	// two renderings would make the round trip depend on which call site wrote
	// the row.
	test.Nil(T, database.BlobOrNil(nil))
	test.Nil(T, database.BlobOrNil([]byte{}))
	test.NotNil(T, database.BlobOrNil([]byte(`{"a":"b"}`)))

	empty, err := encodeDimensions(nil)
	must.NoError(T, err)
	test.Nil(T, empty)

	emptyMap, err := encodeDimensions(map[string]string{})
	must.NoError(T, err)
	test.Nil(T, emptyMap)

	encoded, err := encodeDimensions(map[string]string{"model": "opus"})
	must.NoError(T, err)
	test.EqOp(T, `{"model":"opus"}`, string(encoded))
}

func TestGroupEntries(T *testing.T) {
	T.Parallel()

	T.Run("folds one group per subject, meter, and period", func(t *testing.T) {
		t.Parallel()

		other := newEntry("c", 100, AggregationSum)
		other.Subject = "account-2"

		groups := groupEntries([]Entry{
			newEntry("a", 3, AggregationSum),
			other,
			newEntry("b", 4, AggregationSum),
		})

		must.SliceLen(t, 2, groups)

		// First-seen order, so the statements a batch issues are the same on
		// every run — which is what makes a failing batch debuggable.
		test.EqOp(t, testSubject, groups[0].subject)
		test.EqOp(t, int64(7), groups[0].quantity)
		test.EqOp(t, "account-2", groups[1].subject)
		test.EqOp(t, int64(100), groups[1].quantity)
	})

	T.Run("folds by aggregation", func(t *testing.T) {
		t.Parallel()

		maxed := groupEntries([]Entry{
			newEntryAt("a", 10, AggregationMax, baseTime),
			newEntryAt("b", 4, AggregationMax, baseTime.Add(time.Hour)),
		})
		must.SliceLen(t, 1, maxed)
		test.EqOp(t, int64(10), maxed[0].quantity)

		last := groupEntries([]Entry{
			newEntryAt("a", 10, AggregationLast, baseTime.Add(time.Hour)),
			newEntryAt("b", 4, AggregationLast, baseTime),
		})
		must.SliceLen(t, 1, last)
		// The out-of-order record does not displace the newer one.
		test.EqOp(t, int64(10), last[0].quantity)
		test.EqOp(t, baseTime.Add(time.Hour), last[0].lastOccurredAt)
	})

	T.Run("seeds last_occurred_at at the window start", func(t *testing.T) {
		t.Parallel()

		// Seeded at the window's start rather than the zero time, so a
		// last-aggregation meter's first record is always newer and the column
		// never holds a year-one timestamp for GREATEST to compare against.
		groups := groupEntries([]Entry{newEntryAt("a", 5, AggregationLast, baseTime)})

		must.SliceLen(t, 1, groups)
		test.EqOp(t, baseTime, groups[0].lastOccurredAt)
		test.EqOp(t, int64(5), groups[0].quantity)
	})

	T.Run("handles an empty set", func(t *testing.T) {
		t.Parallel()

		test.SliceEmpty(t, groupEntries(nil))
	})
}
