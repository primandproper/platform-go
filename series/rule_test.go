package series

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
	_ "time/tzdata" // the zones these tests name, whatever the machine running them has installed

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()

	loc, err := time.LoadLocation(name)
	must.NoError(t, err)

	return loc
}

// tuesdaysAtFour is the issue's own example: Tuesdays at 4pm, every week, from
// September 2.
func tuesdaysAtFour() *Rule {
	return &Rule{
		StartsOn:      Date{Year: 2025, Month: time.September, Day: 2},
		TimeZone:      "America/Chicago",
		Weekday:       time.Tuesday,
		StartMinute:   16 * 60,
		IntervalWeeks: 1,
	}
}

func TestDate(T *testing.T) {
	T.Parallel()

	T.Run("round-trips through its text form", func(t *testing.T) {
		t.Parallel()

		d := Date{Year: 2026, Month: time.February, Day: 28}

		parsed, err := ParseDate(d.String())
		must.NoError(t, err)
		test.EqOp(t, d, parsed)
		test.EqOp(t, "2026-02-28", d.String())
	})

	T.Run("the zero date is the empty string", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", Date{}.String())
		test.True(t, Date{}.IsZero())
	})

	T.Run("marshals as text", func(t *testing.T) {
		t.Parallel()

		encoded, err := json.Marshal(Rule{StartsOn: Date{Year: 2026, Month: time.March, Day: 1}})
		must.NoError(t, err)
		test.StrContains(t, string(encoded), `"startsOn":"2026-03-01"`)
		test.StrNotContains(t, string(encoded), "endsOn")

		var decoded Rule
		must.NoError(t, json.Unmarshal(encoded, &decoded))
		test.EqOp(t, Date{Year: 2026, Month: time.March, Day: 1}, decoded.StartsOn)
		test.True(t, decoded.EndsOn.IsZero())
	})

	T.Run("refuses a day that does not exist", func(t *testing.T) {
		t.Parallel()

		_, err := ParseDate("2026-02-30")
		test.ErrorIs(t, err, ErrInvalidRule)
		test.False(t, Date{Year: 2026, Month: time.February, Day: 30}.valid())
	})

	T.Run("adds days across a year", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, Date{Year: 2027, Month: time.January, Day: 3}, Date{Year: 2026, Month: time.December, Day: 27}.AddDays(7))
	})
}

func TestRule_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts the issue's example", func(t *testing.T) {
		t.Parallel()

		loc, err := tuesdaysAtFour().validate()
		must.NoError(t, err)
		test.EqOp(t, "America/Chicago", loc.String())
	})

	cases := map[string]struct {
		change func(*Rule)
		want   error
	}{
		"a weekday past Saturday":   {func(r *Rule) { r.Weekday = 7 }, ErrInvalidRule},
		"a negative time":           {func(r *Rule) { r.StartMinute = -1 }, ErrInvalidRule},
		"midnight of the next day":  {func(r *Rule) { r.StartMinute = 24 * 60 }, ErrInvalidRule},
		"no interval":               {func(r *Rule) { r.IntervalWeeks = 0 }, ErrInvalidRule},
		"an interval past a year":   {func(r *Rule) { r.IntervalWeeks = MaxIntervalWeeks + 1 }, ErrInvalidRule},
		"no start":                  {func(r *Rule) { r.StartsOn = Date{} }, ErrInvalidRule},
		"a start that is not a day": {func(r *Rule) { r.StartsOn.Day = 31; r.StartsOn.Month = time.September }, ErrInvalidRule},
		"an end on the start":       {func(r *Rule) { r.EndsOn = r.StartsOn }, ErrInvalidRule},
		"an end before the start":   {func(r *Rule) { r.EndsOn = r.StartsOn.AddDays(-1) }, ErrInvalidRule},
		"a start before MinYear":    {func(r *Rule) { r.StartsOn.Year = MinYear - 1 }, ErrInvalidRule},
		"a start past MaxYear":      {func(r *Rule) { r.StartsOn.Year = MaxYear + 1 }, ErrInvalidRule},
		"an end past MaxYear":       {func(r *Rule) { r.EndsOn = Date{Year: MaxYear + 1, Month: time.January, Day: 1} }, ErrInvalidRule},
		"no zone":                   {func(r *Rule) { r.TimeZone = "" }, ErrUnknownTimeZone},
		"the process's own zone":    {func(r *Rule) { r.TimeZone = "Local" }, ErrUnknownTimeZone},
		"a zone that is not one":    {func(r *Rule) { r.TimeZone = "Mars/Olympus_Mons" }, ErrUnknownTimeZone},
		"a zone past its column":    {func(r *Rule) { r.TimeZone = string(make([]byte, MaxTimeZoneLength+1)) }, ErrValueTooLong},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			rule := tuesdaysAtFour()
			tc.change(rule)

			_, err := rule.validate()
			test.ErrorIs(t, err, tc.want)
		})
	}

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		var rule *Rule

		_, err := rule.validate()
		test.ErrorIs(t, err, ErrNilRule)
	})
}

func TestRule_Slots(T *testing.T) {
	T.Parallel()

	collect := func(r *Rule, loc *time.Location, from, to time.Time) []time.Time {
		return slices.Collect(r.slots(loc, from, to))
	}

	T.Run("the first slot is the first weekday on or after the start", func(t *testing.T) {
		t.Parallel()

		loc := mustZone(t, "America/Chicago")
		rule := tuesdaysAtFour()
		// September 1 2025 is a Monday, so starting then still lands on the 2nd.
		rule.StartsOn = Date{Year: 2025, Month: time.September, Day: 1}

		got := collect(rule, loc, time.Time{}, time.Date(2025, time.September, 17, 0, 0, 0, 0, time.UTC))

		test.Eq(t, []time.Time{
			time.Date(2025, time.September, 2, 21, 0, 0, 0, time.UTC),
			time.Date(2025, time.September, 9, 21, 0, 0, 0, time.UTC),
			time.Date(2025, time.September, 16, 21, 0, 0, 0, time.UTC),
		}, got)
	})

	// 4pm is 4pm on both sides of a daylight-saving change, which is what a
	// lesson means, and so the UTC instant moves by an hour.
	T.Run("wall-clock time holds across a daylight-saving change", func(t *testing.T) {
		t.Parallel()

		loc := mustZone(t, "America/Chicago")
		rule := tuesdaysAtFour()
		rule.StartsOn = Date{Year: 2025, Month: time.October, Day: 28}

		got := collect(rule, loc, time.Time{}, time.Date(2025, time.November, 5, 0, 0, 0, 0, time.UTC))
		must.SliceLen(t, 2, got)

		test.EqOp(t, 16, got[0].In(loc).Hour())
		test.EqOp(t, 16, got[1].In(loc).Hour())
		test.EqOp(t, 7*24*time.Hour+time.Hour, got[1].Sub(got[0]))
	})

	T.Run("an interval skips the weeks between", func(t *testing.T) {
		t.Parallel()

		loc := mustZone(t, "UTC")
		rule := tuesdaysAtFour()
		rule.TimeZone = "UTC"
		rule.IntervalWeeks = 2

		got := collect(rule, loc, time.Time{}, time.Date(2025, time.October, 1, 0, 0, 0, 0, time.UTC))

		test.Eq(t, []time.Time{
			time.Date(2025, time.September, 2, 16, 0, 0, 0, time.UTC),
			time.Date(2025, time.September, 16, 16, 0, 0, 0, time.UTC),
			time.Date(2025, time.September, 30, 16, 0, 0, 0, time.UTC),
		}, got)
	})

	T.Run("the end date is exclusive", func(t *testing.T) {
		t.Parallel()

		loc := mustZone(t, "UTC")
		rule := tuesdaysAtFour()
		rule.TimeZone = "UTC"
		rule.EndsOn = Date{Year: 2025, Month: time.September, Day: 16}

		got := collect(rule, loc, time.Time{}, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))

		test.Eq(t, []time.Time{
			time.Date(2025, time.September, 2, 16, 0, 0, 0, time.UTC),
			time.Date(2025, time.September, 9, 16, 0, 0, 0, time.UTC),
		}, got)
	})

	T.Run("from and to bound the slots, from inclusive and to not", func(t *testing.T) {
		t.Parallel()

		loc := mustZone(t, "UTC")
		rule := tuesdaysAtFour()
		rule.TimeZone = "UTC"

		from := time.Date(2025, time.September, 9, 16, 0, 0, 0, time.UTC)
		to := time.Date(2025, time.September, 23, 16, 0, 0, 0, time.UTC)

		test.Eq(t, []time.Time{from, from.AddDate(0, 0, 7)}, collect(rule, loc, from, to))
	})

	// A rule that started years ago jumps to the window rather than walking
	// there, and the jump must land on the rule's own stride.
	T.Run("a window years after the start lands on the stride", func(t *testing.T) {
		t.Parallel()

		loc := mustZone(t, "UTC")
		rule := tuesdaysAtFour()
		rule.TimeZone = "UTC"
		rule.IntervalWeeks = 3

		from := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
		got := collect(rule, loc, from, from.AddDate(0, 0, 21))
		must.SliceLen(t, 1, got)

		test.EqOp(t, time.Tuesday, got[0].Weekday())
		days := int(got[0].Sub(time.Date(2025, time.September, 2, 16, 0, 0, 0, time.UTC)).Hours() / 24)
		test.EqOp(t, 0, days%21)
	})

	T.Run("an empty range yields nothing", func(t *testing.T) {
		t.Parallel()

		loc := mustZone(t, "UTC")
		rule := tuesdaysAtFour()
		rule.TimeZone = "UTC"

		at := time.Date(2025, time.September, 3, 0, 0, 0, 0, time.UTC)
		test.SliceEmpty(t, collect(rule, loc, at, at))
	})
}

func TestWindow_Bounds(T *testing.T) {
	T.Parallel()

	T.Run("is (from - 1s, to - 1s] over whole seconds", func(t *testing.T) {
		t.Parallel()

		from := time.Date(2025, time.September, 1, 0, 0, 0, 0, time.UTC)
		to := from.AddDate(0, 0, 7)

		after, through, err := Window{From: from, To: to}.bounds()
		must.NoError(t, err)
		test.EqOp(t, from.Add(-time.Second), after)
		test.EqOp(t, to.Add(-time.Second), through)
	})

	T.Run("rounds a fractional bound up before stepping back", func(t *testing.T) {
		t.Parallel()

		from := time.Date(2025, time.September, 1, 0, 0, 0, 500, time.UTC)

		after, _, err := Window{From: from, To: from.Add(time.Hour)}.bounds()
		must.NoError(t, err)
		// A stored instant at exactly midnight is before From, and so is not in
		// the window; the next whole second is.
		test.EqOp(t, time.Date(2025, time.September, 1, 0, 0, 0, 0, time.UTC), after)
	})

	T.Run("refuses a window that does not end after it starts", func(t *testing.T) {
		t.Parallel()

		at := time.Date(2025, time.September, 1, 0, 0, 0, 0, time.UTC)

		for _, w := range []Window{{From: at, To: at}, {From: at, To: at.Add(-time.Hour)}, {To: at}, {From: at}} {
			_, _, err := w.bounds()
			test.ErrorIs(t, err, ErrInvalidWindow)
		}
	})
}

func TestValidateReason(t *testing.T) {
	t.Parallel()

	test.NoError(t, validateReason(""))
	test.NoError(t, validateReason(string(make([]rune, MaxReasonLength))))
	test.ErrorIs(t, validateReason(string(make([]rune, MaxReasonLength+1))), ErrValueTooLong)
}
