package jobs_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/jobs"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// chicago is the zone the DST cases below are expressed in. It observes both
// transitions, and 2026's fall on dates far enough from any month boundary that
// the assertions read plainly.
func chicago(t *testing.T) *time.Location {
	t.Helper()

	loc, err := time.LoadLocation("America/Chicago")
	must.NoError(t, err)

	return loc
}

func TestCron(T *testing.T) {
	T.Parallel()

	// noon on a Saturday, in UTC, well away from any transition.
	from := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)

	T.Run("standard five-field specs", func(t *testing.T) {
		t.Parallel()

		cases := map[string]time.Time{
			"* * * * *":    time.Date(2026, 3, 7, 12, 1, 0, 0, time.UTC),
			"*/15 * * * *": time.Date(2026, 3, 7, 12, 15, 0, 0, time.UTC),
			"0 3 * * *":    time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC),
			"30 13 * * *":  time.Date(2026, 3, 7, 13, 30, 0, 0, time.UTC),
			// Saturday the 7th, so the next weekday 09:00 is Monday the 9th.
			"0 9 * * 1-5": time.Date(2026, 3, 9, 9, 0, 0, 0, time.UTC),
			"0 0 1 * *":   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		}

		for spec, want := range cases {
			t.Run(spec, func(t *testing.T) {
				t.Parallel()

				schedule, err := jobs.Cron(spec)
				must.NoError(t, err)
				test.EqOp(t, want, schedule.Next(from).UTC())
			})
		}
	})

	T.Run("descriptors", func(t *testing.T) {
		t.Parallel()

		cases := map[string]time.Time{
			"@hourly":      time.Date(2026, 3, 7, 13, 0, 0, 0, time.UTC),
			"@daily":       time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
			"@midnight":    time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
			"@weekly":      time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
			"@monthly":     time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			"@yearly":      time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
			"@every 30m":   time.Date(2026, 3, 7, 12, 30, 0, 0, time.UTC),
			"@every 1h30m": time.Date(2026, 3, 7, 13, 30, 0, 0, time.UTC),
		}

		for spec, want := range cases {
			t.Run(spec, func(t *testing.T) {
				t.Parallel()

				schedule, err := jobs.Cron(spec)
				must.NoError(t, err)
				test.EqOp(t, want, schedule.Next(from).UTC())
			})
		}
	})

	T.Run("a spec with no zone means UTC, not the host's local time", func(t *testing.T) {
		t.Parallel()

		schedule, err := jobs.Cron("0 3 * * *")
		must.NoError(t, err)

		// Asked in Chicago, answered in UTC: 03:00 UTC is 21:00 the evening
		// before, Chicago time. This is the whole point of the default — the
		// same expression has to mean the same instant on every replica, no
		// matter what each container thinks local time is.
		next := schedule.Next(time.Date(2026, 3, 7, 12, 0, 0, 0, chicago(t)))

		test.EqOp(t, time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC), next.UTC())
	})

	T.Run("a spec may name its own zone", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"CRON_TZ=", "TZ="} {
			t.Run(prefix, func(t *testing.T) {
				t.Parallel()

				// One location for both sides of the comparison: LoadLocation
				// hands back a fresh *Location every call, and time.Time's ==
				// compares that pointer.
				loc := chicago(t)

				schedule, err := jobs.Cron(prefix + "America/Chicago 0 3 * * *")
				must.NoError(t, err)

				next := schedule.Next(from).In(loc)

				test.EqOp(t, 3, next.Hour())
				test.EqOp(t, time.Date(2026, 3, 8, 3, 0, 0, 0, loc), next)
			})
		}
	})

	T.Run("surrounding whitespace is not part of the spec", func(t *testing.T) {
		t.Parallel()

		schedule, err := jobs.Cron("  0 3 * * *\t")
		must.NoError(t, err)

		test.EqOp(t, time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC), schedule.Next(from).UTC())
		test.EqOp(t, "CRON_TZ=UTC 0 3 * * *", describe(t, schedule))
	})

	T.Run("reports the expression and the zone it will be read in", func(t *testing.T) {
		t.Parallel()

		// Telemetry reads this, so it has to be the expression a reader would
		// recognize — and it names the zone even when the caller did not,
		// because "0 3 * * *" on a span leaves the only interesting question
		// unanswered.
		cases := map[string]string{
			"0 3 * * *":                         "CRON_TZ=UTC 0 3 * * *",
			"CRON_TZ=America/Chicago 0 3 * * *": "CRON_TZ=America/Chicago 0 3 * * *",
			// Normalized to one spelling on the way out.
			"TZ=America/Chicago 0 3 * * *": "CRON_TZ=America/Chicago 0 3 * * *",
			// A delay rather than a wall-clock time, so there is no zone to
			// name and claiming one would be a lie.
			"@every 30m": "@every 30m",
			"@daily":     "CRON_TZ=UTC @daily",
		}

		for spec, want := range cases {
			t.Run(spec, func(t *testing.T) {
				t.Parallel()

				schedule, err := jobs.Cron(spec)
				must.NoError(t, err)

				test.EqOp(t, want, describe(t, schedule))

				// What it reports parses back to the same fire times, so a spec
				// copied off a span means what the span said it meant.
				reparsed, err := jobs.Cron(describe(t, schedule))
				must.NoError(t, err)
				test.EqOp(t, schedule.Next(from), reparsed.Next(from))
			})
		}
	})

	T.Run("invalid specs", func(t *testing.T) {
		t.Parallel()

		cases := map[string]string{
			"empty":                   "",
			"only whitespace":         "   ",
			"too few fields":          "* * * *",
			"too many fields":         "* * * * * *",
			"not a spec at all":       "every tuesday please",
			"out of range":            "0 25 * * *",
			"unknown zone":            "CRON_TZ=Mars/Olympus 0 3 * * *",
			"seconds are not a field": "*/30 * * * * *",
			// A prefix and nothing else: the underlying parser indexes to the
			// first space without checking there is one, and panics on this.
			"zone with no schedule":      "CRON_TZ=UTC",
			"tz zone with no schedule":   "TZ=UTC",
			"zone trailed by whitespace": "CRON_TZ=UTC \t",
			// The parser splits the zone off at the first space specifically, so
			// a tab after the prefix folds the zone and the minute field into
			// one unloadable location name rather than reaching the fields.
			"zone separated by a tab": "CRON_TZ=UTC\t0 3 * * *",
		}

		for name, spec := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				schedule, err := jobs.Cron(spec)

				test.ErrorIs(t, err, jobs.ErrInvalidCronSpec)
				test.Nil(t, schedule)
			})
		}
	})

	T.Run("a spec that parses but never comes true", func(t *testing.T) {
		t.Parallel()

		// The 30th of February. Nothing about it is a parse error, and the zero
		// time is how a Schedule says it will never fire again.
		schedule, err := jobs.Cron("0 0 30 2 *")
		must.NoError(t, err)

		test.True(t, schedule.Next(from).IsZero())
	})
}

func TestMustCron(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		schedule := jobs.MustCron("0 3 * * *")
		must.NotNil(t, schedule)

		test.EqOp(t, time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC),
			schedule.Next(time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)).UTC())
	})

	T.Run("panics on a spec it cannot parse", func(t *testing.T) {
		t.Parallel()

		defer func() {
			test.NotNil(t, recover())
		}()

		jobs.MustCron("every tuesday please")

		t.Fatal("MustCron returned on an unparseable spec")
	})
}

func TestCronIn(T *testing.T) {
	T.Parallel()

	from := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)

	T.Run("reads a spec that names no zone in the given one", func(t *testing.T) {
		t.Parallel()

		loc := chicago(t)

		schedule, err := jobs.CronIn(loc, "0 3 * * *")
		must.NoError(t, err)

		test.EqOp(t, time.Date(2026, 3, 8, 3, 0, 0, 0, loc), schedule.Next(from).In(loc))
		test.EqOp(t, "CRON_TZ=America/Chicago 0 3 * * *", describe(t, schedule))
	})

	T.Run("a spec that names its own zone wins", func(t *testing.T) {
		t.Parallel()

		// The more specific instruction. A caller who wrote the zone into the
		// expression meant it, and a default handed in from outside should not
		// quietly move the job.
		schedule, err := jobs.CronIn(chicago(t), "CRON_TZ=UTC 0 3 * * *")
		must.NoError(t, err)

		test.EqOp(t, time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC), schedule.Next(from).UTC())
		test.EqOp(t, "CRON_TZ=UTC 0 3 * * *", describe(t, schedule))
	})

	T.Run("a zone time.LoadLocation could not have produced", func(t *testing.T) {
		t.Parallel()

		// The reason the location is replaced on the parsed schedule rather
		// than by rewriting the spec: this one has no name to look up.
		loc := time.FixedZone("Nowhere", 5*60*60)

		schedule, err := jobs.CronIn(loc, "0 3 * * *")
		must.NoError(t, err)

		test.EqOp(t, time.Date(2026, 3, 8, 3, 0, 0, 0, loc), schedule.Next(from).In(loc))
	})

	T.Run("a delay has no zone to be read in", func(t *testing.T) {
		t.Parallel()

		// "@every 30m" is half an hour wherever it is read, so the location is
		// accepted and has nothing to do.
		schedule, err := jobs.CronIn(chicago(t), "@every 30m")
		must.NoError(t, err)

		test.EqOp(t, from.Add(30*time.Minute), schedule.Next(from).UTC())
		test.EqOp(t, "@every 30m", describe(t, schedule))
	})

	T.Run("with a nil location", func(t *testing.T) {
		t.Parallel()

		schedule, err := jobs.CronIn(nil, "0 3 * * *")

		test.ErrorIs(t, err, jobs.ErrInvalidCronSpec)
		test.Nil(t, schedule)
	})

	T.Run("with an unparseable spec", func(t *testing.T) {
		t.Parallel()

		schedule, err := jobs.CronIn(chicago(t), "every tuesday please")

		test.ErrorIs(t, err, jobs.ErrInvalidCronSpec)
		test.Nil(t, schedule)
	})
}

func TestMustCronIn(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		loc := chicago(t)
		schedule := jobs.MustCronIn(loc, "0 3 * * *")
		must.NotNil(t, schedule)

		test.EqOp(t, time.Date(2026, 3, 8, 3, 0, 0, 0, loc),
			schedule.Next(time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)).In(loc))
	})

	T.Run("panics on a spec it cannot parse", func(t *testing.T) {
		t.Parallel()

		defer func() {
			test.NotNil(t, recover())
		}()

		jobs.MustCronIn(chicago(t), "every tuesday please")

		t.Fatal("MustCronIn returned on an unparseable spec")
	})
}

// TestCron_DaylightSaving pins what a wall-clock schedule does across the two
// transitions, because "it runs at 03:00" stops being a complete answer on the
// two days a year when a local hour is missing or repeated. These are the
// documented semantics, not incidental behavior.
func TestCron_DaylightSaving(T *testing.T) {
	T.Parallel()

	// 2026-03-08: 02:00 CST becomes 03:00 CDT, so 02:00–02:59 does not exist.
	// 2026-11-01: 02:00 CDT becomes 01:00 CST, so 01:00–01:59 happens twice.
	T.Run("an hour that exists on both sides of the spring transition still fires", func(t *testing.T) {
		t.Parallel()

		loc := chicago(t)
		schedule := jobs.MustCron("CRON_TZ=America/Chicago 0 3 * * *")

		fires := nextN(schedule, time.Date(2026, 3, 6, 12, 0, 0, 0, loc), 3, loc)

		test.Eq(t, []time.Time{
			time.Date(2026, 3, 7, 3, 0, 0, 0, loc),
			time.Date(2026, 3, 8, 3, 0, 0, 0, loc),
			time.Date(2026, 3, 9, 3, 0, 0, 0, loc),
		}, fires)
	})

	T.Run("a wall-clock time that does not exist on the spring-forward day is skipped", func(t *testing.T) {
		t.Parallel()

		loc := chicago(t)
		schedule := jobs.MustCron("CRON_TZ=America/Chicago 30 2 * * *")

		fires := nextN(schedule, time.Date(2026, 3, 6, 12, 0, 0, 0, loc), 3, loc)

		// The 8th is absent: 02:30 never happens that day, so the job does not
		// run at all. A job that must not be skipped wants an hour that exists
		// on every day of the year, or UTC.
		test.Eq(t, []time.Time{
			time.Date(2026, 3, 7, 2, 30, 0, 0, loc),
			time.Date(2026, 3, 9, 2, 30, 0, 0, loc),
			time.Date(2026, 3, 10, 2, 30, 0, 0, loc),
		}, fires)
	})

	T.Run("a wall-clock time that happens twice on the fall-back day fires twice", func(t *testing.T) {
		t.Parallel()

		loc := chicago(t)
		schedule := jobs.MustCron("CRON_TZ=America/Chicago 30 1 * * *")

		fires := nextN(schedule, time.Date(2026, 10, 31, 12, 0, 0, 0, loc), 3, loc)

		must.SliceLen(t, 3, fires)

		// Both fires are 01:30 on 2026-11-01, an hour apart: the first in CDT,
		// the second in CST. The lease does not save a job from this — it was
		// released an hour earlier — so a job that must not run twice a year
		// wants UTC.
		test.EqOp(t, time.Date(2026, 11, 1, 1, 30, 0, 0, loc), fires[0])
		test.EqOp(t, "CDT", zoneOf(t, fires[0]))
		test.EqOp(t, "CST", zoneOf(t, fires[1]))
		test.EqOp(t, time.Hour, fires[1].Sub(fires[0]))
		test.EqOp(t, time.Date(2026, 11, 2, 1, 30, 0, 0, loc), fires[2])
	})

	T.Run("a UTC schedule has neither problem", func(t *testing.T) {
		t.Parallel()

		schedule := jobs.MustCron("30 1 * * *")

		fires := nextN(schedule, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), 3, time.UTC)

		test.Eq(t, []time.Time{
			time.Date(2026, 11, 1, 1, 30, 0, 0, time.UTC),
			time.Date(2026, 11, 2, 1, 30, 0, 0, time.UTC),
			time.Date(2026, 11, 3, 1, 30, 0, 0, time.UTC),
		}, fires)
	})
}

// nextN walks a Schedule forward n fires from t, reporting each in loc.
func nextN(schedule jobs.Schedule, from time.Time, n int, loc *time.Location) []time.Time {
	fires := make([]time.Time, 0, n)

	at := from
	for range n {
		at = schedule.Next(at)
		fires = append(fires, at.In(loc))
	}

	return fires
}

// zoneOf reports the abbreviation of the offset a time is expressed in, which
// is what distinguishes the two 01:30s on a fall-back day.
func zoneOf(t *testing.T, at time.Time) string {
	t.Helper()

	name, _ := at.Zone()

	return name
}

// describe reads back the text a Schedule reports itself as, which is what the
// Scheduler puts on a span.
func describe(t *testing.T, schedule jobs.Schedule) string {
	t.Helper()

	stringer, ok := schedule.(interface{ String() string })
	must.True(t, ok)

	return stringer.String()
}
