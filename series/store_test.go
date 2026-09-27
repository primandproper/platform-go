package series

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore_SQLite runs the behavioral suite against SQLite, which needs no
// container. The same suite runs against real servers in containers_test.go.
func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is every behavior this store promises, against whatever database
// the environment holds. It is one function because it runs on three dialects,
// and a case outside it would be checked on one of them.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	runSeriesCases(t, env)
	runMaterializeCases(t, env)
	runOccurrenceCases(t, env)
	runEndCases(t, env)
	runClosureCases(t, env)
	runRefusalCases(t, env)
}

func runSeriesCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("create answers with the row it wrote, written out to nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		rule := weeklyUTC()
		rule.EndsOn = Date{Year: 2026, Month: time.June, Day: 1}

		created := env.mustCreate(t, store, testScope, rule)

		test.NotEqOp(t, "", created.ID)
		test.EqOp(t, testScope, created.Scope)
		test.EqOp(t, *rule, created.Rule)
		test.EqOp(t, time.Date(2025, time.September, 2, 0, 0, 0, 0, time.UTC), created.MaterializedUntil)
		test.Nil(t, created.ExhaustedAt)
		test.False(t, created.CreatedAt.IsZero())
		test.SliceEmpty(t, env.occurrences(t, store, testScope, created.ID))
	})

	// The first day starts at midnight in the rule's zone, not in UTC.
	t.Run("a series is born written out to midnight in its own zone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		rule := weeklyUTC()
		rule.TimeZone = "America/Mexico_City"

		created := env.mustCreate(t, store, testScope, rule)
		test.EqOp(t, time.Date(2025, time.September, 2, 6, 0, 0, 0, time.UTC), created.MaterializedUntil)
	})

	t.Run("a series in another scope reads as absent", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())

		got, err := store.GetSeries(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, created.ID, got.ID)

		_, err = store.GetSeries(t.Context(), env.reader(), otherScope, created.ID)
		test.ErrorIs(t, err, ErrSeriesNotFound)

		_, err = store.GetSeries(t.Context(), env.reader(), testScope, "")
		test.ErrorIs(t, err, ErrSeriesNotFound)
	})

	t.Run("list pages a scope's series in id order", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		ids := map[string]bool{}
		for range 3 {
			ids[env.mustCreate(t, store, testScope, weeklyUTC()).ID] = true
		}

		env.mustCreate(t, store, otherScope, weeklyUTC())

		first, err := store.ListSeries(t.Context(), env.reader(), testScope, "", 2)
		must.NoError(t, err)
		must.SliceLen(t, 2, first)
		test.True(t, first[0].ID < first[1].ID)

		rest, err := store.ListSeries(t.Context(), env.reader(), testScope, first[1].ID, 2)
		must.NoError(t, err)
		must.SliceLen(t, 1, rest)

		for _, s := range append(first, rest...) {
			test.True(t, ids[s.ID], test.Sprintf("series %s is not testScope's", s.ID))
		}

		_, err = store.ListSeries(t.Context(), env.reader(), testScope, "", 0)
		test.ErrorIs(t, err, ErrInvalidPageSize)

		_, err = store.ListSeries(t.Context(), env.reader(), testScope, "", MaxSeriesPerPage+1)
		test.ErrorIs(t, err, ErrInvalidPageSize)
	})
}

func runMaterializeCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("materialize writes every slot before through, scheduled at its slot", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())

		test.EqOp(t, int64(3), env.mustMaterialize(t, store, testScope, created.ID, tuesday(3)))

		got := env.occurrences(t, store, testScope, created.ID)
		must.SliceLen(t, 3, got)

		for i, o := range got {
			test.EqOp(t, tuesday(i), o.ScheduledAt)
			must.NotNil(t, o.SlotAt)
			test.EqOp(t, tuesday(i), *o.SlotAt)
			test.EqOp(t, StateScheduled, o.State)
			test.EqOp(t, created.ID, o.SeriesID)
			test.EqOp(t, testScope, o.Scope)
		}

		after, err := store.GetSeries(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, tuesday(3), after.MaterializedUntil)
	})

	t.Run("materialize is idempotent, and never moves backwards", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())

		env.mustMaterialize(t, store, testScope, created.ID, tuesday(4))
		test.EqOp(t, int64(0), env.mustMaterialize(t, store, testScope, created.ID, tuesday(4)))
		test.EqOp(t, int64(0), env.mustMaterialize(t, store, testScope, created.ID, tuesday(2)))
		test.EqOp(t, int64(1), env.mustMaterialize(t, store, testScope, created.ID, tuesday(5)))

		test.SliceLen(t, 5, env.occurrences(t, store, testScope, created.ID))
	})

	// The unique index is the backstop, not the materialized_until bookkeeping:
	// a slot that already has a row is left alone even when the series has no
	// record of having written it.
	t.Run("a slot that already has a row is not written twice", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())

		env.mustMaterialize(t, store, testScope, created.ID, tuesday(2))

		// Past the store: materialized_until back where it started, as if a
		// second pass had read the series before the first committed.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			found, err := store.readSeries(t.Context(), tx, testScope, created.ID)
			if err != nil {
				return err
			}

			found.MaterializedUntil = created.MaterializedUntil

			written, err := store.materialize(t.Context(), tx, found, tuesday(3))
			test.EqOp(t, int64(1), written)

			return err
		}))

		test.SliceLen(t, 3, env.occurrences(t, store, testScope, created.ID))
	})

	t.Run("a series written to its end is exhausted and leaves the due list", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		rule := weeklyUTC()
		rule.EndsOn = Date{Year: 2025, Month: time.September, Day: 16}

		created := env.mustCreate(t, store, testScope, rule)
		open := env.mustCreate(t, store, otherScope, weeklyUTC())

		due, err := store.DueSeries(t.Context(), env.reader(), tuesday(10), 10)
		must.NoError(t, err)
		test.SliceLen(t, 2, due)

		test.EqOp(t, int64(2), env.mustMaterialize(t, store, testScope, created.ID, tuesday(10)))

		after, err := store.GetSeries(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.NotNil(t, after.ExhaustedAt)

		due, err = store.DueSeries(t.Context(), env.reader(), tuesday(20), 10)
		must.NoError(t, err)
		must.SliceLen(t, 1, due)
		test.EqOp(t, open.ID, due[0].ID)
		test.EqOp(t, otherScope, due[0].Scope)
	})

	t.Run("a series written past the horizon is not due", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())

		env.mustMaterialize(t, store, testScope, created.ID, tuesday(8))

		due, err := store.DueSeries(t.Context(), env.reader(), tuesday(7), 10)
		must.NoError(t, err)
		test.SliceEmpty(t, due)

		due, err = store.DueSeries(t.Context(), env.reader(), tuesday(8), 10)
		must.NoError(t, err)
		test.SliceLen(t, 1, due)
	})
}

func runOccurrenceCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("skip marks one occurrence skipped with its reason", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())
		env.mustMaterialize(t, store, testScope, created.ID, tuesday(2))

		first := env.occurrences(t, store, testScope, created.ID)[0]

		var skipped *Occurrence

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			skipped, err = store.SkipOccurrence(t.Context(), tx, testScope, first.ID, "teacher ill")

			return err
		}))

		test.EqOp(t, StateSkipped, skipped.State)
		test.EqOp(t, "teacher ill", skipped.Reason)
		test.EqOp(t, first.ScheduledAt, skipped.ScheduledAt)

		err := env.inTx(t, func(tx database.Tx) error {
			_, skipErr := store.SkipOccurrence(t.Context(), tx, testScope, first.ID, "again")

			return skipErr
		})
		test.ErrorIs(t, err, ErrOccurrenceSkipped)

		err = env.inTx(t, func(tx database.Tx) error {
			_, skipErr := store.SkipOccurrence(t.Context(), tx, otherScope, first.ID, "")

			return skipErr
		})
		test.ErrorIs(t, err, ErrOccurrenceNotFound)
	})

	t.Run("move keeps the id and the slot, so the slot is not written again", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())
		env.mustMaterialize(t, store, testScope, created.ID, tuesday(2))

		first := env.occurrences(t, store, testScope, created.ID)[0]
		to := tuesday(0).Add(26*time.Hour + 500*time.Millisecond)

		var moved *Occurrence

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			moved, err = store.MoveOccurrence(t.Context(), tx, testScope, first.ID, to, "gym booked")

			return err
		}))

		test.EqOp(t, first.ID, moved.ID)
		test.EqOp(t, StateMoved, moved.State)
		test.EqOp(t, "gym booked", moved.Reason)
		test.EqOp(t, to.Truncate(time.Second), moved.ScheduledAt)
		must.NotNil(t, moved.SlotAt)
		test.EqOp(t, tuesday(0), *moved.SlotAt)

		// Written again from the start: the moved slot is still occupied.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			found, err := store.readSeries(t.Context(), tx, testScope, created.ID)
			if err != nil {
				return err
			}

			found.MaterializedUntil = created.MaterializedUntil

			written, err := store.materialize(t.Context(), tx, found, tuesday(2))
			test.EqOp(t, int64(0), written)

			return err
		}))

		test.SliceLen(t, 2, env.occurrences(t, store, testScope, created.ID))

		// And a skipped one does not move.
		err := env.inTx(t, func(tx database.Tx) error {
			if _, skipErr := store.SkipOccurrence(t.Context(), tx, testScope, first.ID, ""); skipErr != nil {
				return skipErr
			}

			_, moveErr := store.MoveOccurrence(t.Context(), tx, testScope, first.ID, to, "")

			return moveErr
		})
		test.ErrorIs(t, err, ErrOccurrenceSkipped)
	})

	t.Run("a replacement is added against a skip once", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())
		env.mustMaterialize(t, store, testScope, created.ID, tuesday(2))

		got := env.occurrences(t, store, testScope, created.ID)
		first, second := got[0], got[1]
		makeUp := tuesday(0).Add(48 * time.Hour)

		err := env.inTx(t, func(tx database.Tx) error {
			_, addErr := store.AddReplacement(t.Context(), tx, testScope, first.ID, makeUp)

			return addErr
		})
		test.ErrorIs(t, err, ErrOccurrenceNotSkipped)

		var replacement *Occurrence

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if _, skipErr := store.SkipOccurrence(t.Context(), tx, testScope, first.ID, "teacher ill"); skipErr != nil {
				return skipErr
			}

			var addErr error
			replacement, addErr = store.AddReplacement(t.Context(), tx, testScope, first.ID, makeUp)

			return addErr
		}))

		test.NotEqOp(t, first.ID, replacement.ID)
		test.EqOp(t, created.ID, replacement.SeriesID)
		test.EqOp(t, StateScheduled, replacement.State)
		test.EqOp(t, makeUp, replacement.ScheduledAt)
		test.Nil(t, replacement.SlotAt)

		skipped, err := store.GetOccurrence(t.Context(), env.reader(), testScope, first.ID)
		must.NoError(t, err)
		test.EqOp(t, replacement.ID, skipped.ReplacedBy)

		err = env.inTx(t, func(tx database.Tx) error {
			_, addErr := store.AddReplacement(t.Context(), tx, testScope, first.ID, makeUp.Add(time.Hour))

			return addErr
		})
		test.ErrorIs(t, err, ErrOccurrenceReplaced)

		// The week view has the three rows in the order they happen.
		week, err := store.ListOccurrences(t.Context(), env.reader(), testScope, everything)
		must.NoError(t, err)
		must.SliceLen(t, 3, week)
		test.EqOp(t, first.ID, week[0].ID)
		test.EqOp(t, replacement.ID, week[1].ID)
		test.EqOp(t, second.ID, week[2].ID)
	})

	t.Run("a refused command leaves nothing behind", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())
		env.mustMaterialize(t, store, testScope, created.ID, tuesday(1))

		first := env.occurrences(t, store, testScope, created.ID)[0]

		// The skip succeeds and the replacement is refused, and the caller's
		// transaction takes the skip back with it.
		err := env.inTx(t, func(tx database.Tx) error {
			if _, skipErr := store.SkipOccurrence(t.Context(), tx, testScope, first.ID, ""); skipErr != nil {
				return skipErr
			}

			_, addErr := store.AddReplacement(t.Context(), tx, testScope, first.ID, time.Time{})

			return addErr
		})
		test.ErrorIs(t, err, ErrNoInstant)

		again, err := store.GetOccurrence(t.Context(), env.reader(), testScope, first.ID)
		must.NoError(t, err)
		test.EqOp(t, StateScheduled, again.State)
	})

	t.Run("a window is from-inclusive and to-exclusive, and scoped", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mine := env.mustCreate(t, store, testScope, weeklyUTC())
		theirs := env.mustCreate(t, store, otherScope, weeklyUTC())

		env.mustMaterialize(t, store, testScope, mine.ID, tuesday(4))
		env.mustMaterialize(t, store, otherScope, theirs.ID, tuesday(4))

		got, err := store.ListOccurrences(t.Context(), env.reader(), testScope, Window{From: tuesday(1), To: tuesday(3)})
		must.NoError(t, err)
		must.SliceLen(t, 2, got)
		test.EqOp(t, tuesday(1), got[0].ScheduledAt)
		test.EqOp(t, tuesday(2), got[1].ScheduledAt)

		for _, o := range got {
			test.EqOp(t, testScope, o.Scope)
		}

		_, err = store.ListOccurrences(t.Context(), env.reader(), testScope, Window{From: tuesday(3), To: tuesday(1)})
		test.ErrorIs(t, err, ErrInvalidWindow)
	})
}

func runEndCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("end skips what the rule put on or after the date, and nothing before", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())
		env.mustMaterialize(t, store, testScope, created.ID, tuesday(6))

		got := env.occurrences(t, store, testScope, created.ID)

		var ended *Series

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			// Tuesday 3 is moved to before the end and still goes, because the
			// rule put it after; Tuesday 1 is moved to after and stays.
			if _, err := store.MoveOccurrence(t.Context(), tx, testScope, got[3].ID, tuesday(2).Add(time.Hour), ""); err != nil {
				return err
			}

			if _, err := store.MoveOccurrence(t.Context(), tx, testScope, got[1].ID, tuesday(5).Add(time.Hour), ""); err != nil {
				return err
			}

			// A make-up after the end is not the rule's to remove.
			if _, err := store.SkipOccurrence(t.Context(), tx, testScope, got[0].ID, ""); err != nil {
				return err
			}

			if _, err := store.AddReplacement(t.Context(), tx, testScope, got[0].ID, tuesday(5).Add(2*time.Hour)); err != nil {
				return err
			}

			var err error
			ended, err = store.EndSeries(t.Context(), tx, testScope, created.ID, DateOf(tuesday(3)), "student moved away")

			return err
		}))

		test.EqOp(t, DateOf(tuesday(3)), ended.Rule.EndsOn)

		states := map[string]State{}
		reasons := map[string]string{}

		for _, o := range env.occurrences(t, store, testScope, created.ID) {
			states[o.ID] = o.State
			reasons[o.ID] = o.Reason
		}

		test.EqOp(t, StateSkipped, states[got[0].ID])
		test.EqOp(t, StateMoved, states[got[1].ID])
		test.EqOp(t, StateScheduled, states[got[2].ID])
		test.EqOp(t, StateSkipped, states[got[3].ID])
		test.EqOp(t, StateSkipped, states[got[4].ID])
		test.EqOp(t, StateSkipped, states[got[5].ID])
		test.EqOp(t, "student moved away", reasons[got[5].ID])
		test.EqOp(t, 2, countState(states, StateScheduled), test.Sprintf("Tuesday 2 and the make-up stay scheduled: %v", states))

		// Written further: nothing past the end, and the series is exhausted.
		test.EqOp(t, int64(0), env.mustMaterialize(t, store, testScope, created.ID, tuesday(20)))

		after, err := store.GetSeries(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.NotNil(t, after.ExhaustedAt)
	})

	t.Run("an end only brings the end nearer", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		rule := weeklyUTC()
		rule.EndsOn = DateOf(tuesday(4))

		created := env.mustCreate(t, store, testScope, rule)

		for _, from := range []Date{DateOf(tuesday(4)), DateOf(tuesday(6))} {
			err := env.inTx(t, func(tx database.Tx) error {
				_, endErr := store.EndSeries(t.Context(), tx, testScope, created.ID, from, "")

				return endErr
			})
			test.ErrorIs(t, err, ErrSeriesEnded)
		}

		err := env.inTx(t, func(tx database.Tx) error {
			_, endErr := store.EndSeries(t.Context(), tx, otherScope, created.ID, DateOf(tuesday(2)), "")

			return endErr
		})
		test.ErrorIs(t, err, ErrSeriesNotFound)
	})
}

func runClosureCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a closure past the horizon skips what the rules imply there", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		weekly := env.mustCreate(t, store, testScope, weeklyUTC())

		thursdays := weeklyUTC()
		thursdays.Weekday = time.Thursday
		fortnightly := env.mustCreate(t, store, testScope, thursdays)

		neighbor := env.mustCreate(t, store, otherScope, weeklyUTC())

		// Written out two weeks; the closure is weeks ten and eleven.
		env.mustMaterialize(t, store, testScope, weekly.ID, tuesday(2))
		closure := Window{From: DateOf(tuesday(10)).civil(), To: DateOf(tuesday(12)).civil()}

		var skipped int64

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			skipped, err = store.SkipWindow(t.Context(), tx, testScope, closure, "winter break")

			return err
		}))

		test.EqOp(t, int64(4), skipped)

		inside, err := store.ListOccurrences(t.Context(), env.reader(), testScope, closure)
		must.NoError(t, err)
		must.SliceLen(t, 4, inside)

		for _, o := range inside {
			test.EqOp(t, StateSkipped, o.State)
			test.EqOp(t, "winter break", o.Reason)
		}

		// The worker, reaching the closure later, leaves it skipped.
		env.mustMaterialize(t, store, testScope, weekly.ID, tuesday(14))
		env.mustMaterialize(t, store, testScope, fortnightly.ID, tuesday(14))

		inside, err = store.ListOccurrences(t.Context(), env.reader(), testScope, closure)
		must.NoError(t, err)
		must.SliceLen(t, 4, inside)

		for _, o := range inside {
			test.EqOp(t, StateSkipped, o.State)
		}

		// Outside the window, and in the neighbor's scope, nothing is skipped.
		before, err := store.ListOccurrences(t.Context(), env.reader(), testScope, Window{From: everything.From, To: closure.From})
		must.NoError(t, err)
		test.SliceNotEmpty(t, before)

		for _, o := range before {
			test.EqOp(t, StateScheduled, o.State)
		}

		test.SliceEmpty(t, env.occurrences(t, store, otherScope, neighbor.ID))
	})

	t.Run("a closure leaves an earlier skip's reason alone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())
		env.mustMaterialize(t, store, testScope, created.ID, tuesday(2))

		first := env.occurrences(t, store, testScope, created.ID)[0]

		var skipped int64

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if _, err := store.SkipOccurrence(t.Context(), tx, testScope, first.ID, "teacher ill"); err != nil {
				return err
			}

			var err error
			skipped, err = store.SkipWindow(t.Context(), tx, testScope, Window{From: tuesday(0), To: tuesday(2)}, "flood")

			return err
		}))

		test.EqOp(t, int64(1), skipped)

		again, err := store.GetOccurrence(t.Context(), env.reader(), testScope, first.ID)
		must.NoError(t, err)
		test.EqOp(t, "teacher ill", again.Reason)
	})
}

func runRefusalCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("nil executors are refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		ctx := t.Context()

		_, err := store.CreateSeries(ctx, nil, testScope, weeklyUTC())
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetSeries(ctx, nil, testScope, "x")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListSeries(ctx, nil, testScope, "", 1)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.EndSeries(ctx, nil, testScope, "x", DateOf(tuesday(1)), "")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.Materialize(ctx, nil, testScope, "x", tuesday(1))
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetOccurrence(ctx, nil, testScope, "x")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListOccurrences(ctx, nil, testScope, everything)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListSeriesOccurrences(ctx, nil, testScope, "x", everything)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.SkipOccurrence(ctx, nil, testScope, "x", "")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.SkipWindow(ctx, nil, testScope, everything, "")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.MoveOccurrence(ctx, nil, testScope, "x", tuesday(1), "")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.AddReplacement(ctx, nil, testScope, "x", tuesday(1))
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.DueSeries(ctx, nil, tuesday(1), 1)
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	t.Run("an invalid rule, reason or scope writes nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		err := env.inTx(t, func(tx database.Tx) error {
			_, createErr := store.CreateSeries(t.Context(), tx, testScope, nil)

			return createErr
		})
		test.ErrorIs(t, err, ErrNilRule)

		err = env.inTx(t, func(tx database.Tx) error {
			rule := weeklyUTC()
			rule.IntervalWeeks = 0

			_, createErr := store.CreateSeries(t.Context(), tx, testScope, rule)

			return createErr
		})
		test.ErrorIs(t, err, ErrInvalidRule)

		err = env.inTx(t, func(tx database.Tx) error {
			_, createErr := store.CreateSeries(t.Context(), tx, tenancy.Scope{}, weeklyUTC())

			return createErr
		})
		test.Error(t, err)

		created := env.mustCreate(t, store, testScope, weeklyUTC())
		env.mustMaterialize(t, store, testScope, created.ID, tuesday(1))

		first := env.occurrences(t, store, testScope, created.ID)[0]

		err = env.inTx(t, func(tx database.Tx) error {
			_, skipErr := store.SkipOccurrence(t.Context(), tx, testScope, first.ID, string(make([]rune, MaxReasonLength+1)))

			return skipErr
		})
		test.ErrorIs(t, err, ErrValueTooLong)

		err = env.inTx(t, func(tx database.Tx) error {
			_, matErr := store.Materialize(t.Context(), tx, testScope, created.ID, time.Time{})

			return matErr
		})
		test.ErrorIs(t, err, ErrNoInstant)
	})

	t.Run("a write past the write-ahead limit writes nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		created := env.mustCreate(t, store, testScope, weeklyUTC())
		tooFar := time.Now().Add(MaxWriteAhead + 7*24*time.Hour)

		err := env.inTx(t, func(tx database.Tx) error {
			_, matErr := store.Materialize(t.Context(), tx, testScope, created.ID, tooFar)

			return matErr
		})
		test.ErrorIs(t, err, ErrTooFarAhead)

		err = env.inTx(t, func(tx database.Tx) error {
			_, skipErr := store.SkipWindow(t.Context(), tx, testScope, Window{From: tuesday(0), To: tooFar}, "forever")

			return skipErr
		})
		test.ErrorIs(t, err, ErrTooFarAhead)

		test.SliceEmpty(t, env.occurrences(t, store, testScope, created.ID))
	})
}

func countState(states map[string]State, want State) int {
	n := 0

	for _, s := range states {
		if s == want {
			n++
		}
	}

	return n
}
