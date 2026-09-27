package series

import (
	"iter"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

const (
	// MaxIntervalWeeks bounds how far apart two occurrences of one rule may
	// be. A year is the widest rule a weekly grid has any use for, and a bound
	// is what keeps a typo from becoming a rule that never recurs.
	MaxIntervalWeeks = 52
	// MaxTimeZoneLength bounds the IANA zone name, which the MySQL schema
	// stores as VARCHAR(64). The longest name in the database is 32.
	MaxTimeZoneLength = 64

	minutesPerDay = 24 * 60
	daysPerWeek   = 7
	dateLayout    = time.DateOnly
)

// Date is a calendar date with no time and no zone: the day a rule starts on or
// ends on, read in the rule's own time zone.
//
// It is its own type rather than a time.Time because a time.Time is an instant,
// and "from September 2" is not one — it is a different instant in every zone,
// and which one is decided by the rule it belongs to. The zero Date is "no
// date", which is how a rule with no end says so.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateOf is the calendar date t falls on in t's own location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()

	return Date{Year: y, Month: m, Day: d}
}

// ParseDate reads a date in the YYYY-MM-DD form String writes.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, platformerrors.Wrapf(ErrInvalidRule, "date %q is not YYYY-MM-DD", s)
	}

	return DateOf(t), nil
}

// String renders the date as YYYY-MM-DD, and the zero Date as the empty string —
// which is how the schema stores a rule with no end.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}

	return d.civil().Format(dateLayout)
}

// MarshalText renders the date as String does, so a Date is "2026-09-02" in
// JSON and YAML rather than an object of three numbers.
func (d Date) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalText reads what MarshalText writes, the empty string included.
func (d *Date) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*d = Date{}

		return nil
	}

	parsed, err := ParseDate(string(text))
	if err != nil {
		return err
	}

	*d = parsed

	return nil
}

// IsZero reports whether d is the zero Date.
func (d Date) IsZero() bool {
	return d == Date{}
}

// Before reports whether d falls on an earlier day than other.
func (d Date) Before(other Date) bool {
	return d.civil().Before(other.civil())
}

// AddDays is d moved by n days, across month and year boundaries.
func (d Date) AddDays(n int) Date {
	return DateOf(d.civil().AddDate(0, 0, n))
}

// Weekday is the day of the week d falls on.
func (d Date) Weekday() time.Weekday {
	return d.civil().Weekday()
}

// valid reports whether d names a day that exists. time.Date normalizes
// February 30th into March, so a date is real exactly when it survives the round
// trip unchanged.
func (d Date) valid() bool {
	return DateOf(d.civil()) == d
}

// civil is d as midnight UTC, which is the one zone where calendar arithmetic
// never meets a day that is not 24 hours long.
func (d Date) civil() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

// daysUntil is how many days after d other falls, negative when it is before.
func (d Date) daysUntil(other Date) int {
	return int(other.civil().Sub(d.civil()).Hours() / 24) //nolint:mnd // hours in a UTC day
}

// Rule is a weekly recurrence: a weekday and a wall-clock time in a zone, every
// IntervalWeeks weeks, from StartsOn until EndsOn.
//
// It is deliberately not RFC 5545. Weekly-with-an-interval is what a standing
// appointment is, and an RRULE parser owns no table — it is a primitive if it
// is ever wanted, and adding one later is adding a column rather than
// redesigning these.
type Rule struct {
	// TimeZone is the IANA zone the weekday, the time and both dates are read
	// in. "UTC" is a zone like any other; the empty string is refused rather
	// than read as UTC, because a studio in Oaxaca that forgot to say so would
	// otherwise find every lesson moving by an hour twice a year — or, since
	// Mexico stopped observing daylight saving time, never, which is the
	// point: that is the zone database's to know.
	TimeZone string `json:"timeZone"`
	// StartsOn is the first date an occurrence may fall on. The first
	// occurrence is the first Weekday on or after it.
	StartsOn Date `json:"startsOn"`
	// EndsOn is the first date no occurrence falls on: the rule's dates are
	// [StartsOn, EndsOn). The zero Date is a rule with no end.
	EndsOn Date `json:"endsOn,omitzero"`
	// Weekday is the day of the week occurrences fall on.
	Weekday time.Weekday `json:"weekday"`
	// StartMinute is the wall-clock time occurrences start at, as minutes past
	// local midnight: 16:00 is 960.
	StartMinute int `json:"startMinute"`
	// IntervalWeeks is how many weeks apart occurrences are: 1 is weekly, 2 is
	// fortnightly.
	IntervalWeeks int `json:"intervalWeeks"`
}

// validate checks a rule before it is stored, and answers with the zone it
// named.
func (r *Rule) validate() (*time.Location, error) {
	if r == nil {
		return nil, ErrNilRule
	}

	if r.Weekday < time.Sunday || r.Weekday > time.Saturday {
		return nil, platformerrors.Wrapf(ErrInvalidRule, "weekday %d is not a day of the week", r.Weekday)
	}

	if r.StartMinute < 0 || r.StartMinute >= minutesPerDay {
		return nil, platformerrors.Wrapf(ErrInvalidRule, "start minute %d is not a time of day", r.StartMinute)
	}

	if r.IntervalWeeks < 1 || r.IntervalWeeks > MaxIntervalWeeks {
		return nil, platformerrors.Wrapf(ErrInvalidRule, "interval of %d weeks is outside 1 to %d", r.IntervalWeeks, MaxIntervalWeeks)
	}

	if r.StartsOn.IsZero() || !r.StartsOn.valid() {
		return nil, platformerrors.Wrapf(ErrInvalidRule, "start date %+v is not a date", r.StartsOn)
	}

	if !r.EndsOn.IsZero() {
		if !r.EndsOn.valid() {
			return nil, platformerrors.Wrapf(ErrInvalidRule, "end date %+v is not a date", r.EndsOn)
		}

		if !r.StartsOn.Before(r.EndsOn) {
			return nil, platformerrors.Wrapf(ErrInvalidRule, "end date %s is not after start date %s", r.EndsOn, r.StartsOn)
		}
	}

	return loadZone(r.TimeZone)
}

// loadZone resolves an IANA zone name, refusing the empty string and the Local
// pseudo-zone, which would read a rule in whatever zone the process happened to
// start in.
func loadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, platformerrors.Wrapf(ErrUnknownTimeZone, "%q", name)
	}

	if len(name) > MaxTimeZoneLength {
		return nil, platformerrors.Wrapf(ErrValueTooLong, "time zone is %d bytes, over %d", len(name), MaxTimeZoneLength)
	}

	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, platformerrors.Wrapf(ErrUnknownTimeZone, "%q", name)
	}

	return loc, nil
}

// slots are the instants the rule implies in [from, to), in order, in UTC and
// whole seconds. loc is the rule's zone, resolved.
//
// A wall-clock time the zone skips — 2:30 on a spring-forward night — is
// whatever time.Date normalizes it to, which is the hour after. A time the zone
// repeats is its first occurrence. A rule that meets either has asked for a
// time that is ambiguous where it lives, and the zone database decides.
func (r *Rule) slots(loc *time.Location, from, to time.Time) iter.Seq[time.Time] {
	return func(yield func(time.Time) bool) {
		first := r.StartsOn.AddDays((int(r.Weekday) - int(r.StartsOn.Weekday()) + daysPerWeek) % daysPerWeek)
		stride := r.IntervalWeeks * daysPerWeek

		// Jump to the stride just before from rather than walking there, so a
		// rule that started years ago costs what a rule that started last week
		// does. One stride of margin covers a zone offset moving from's date
		// across midnight.
		k := 0
		if skip := first.daysUntil(DateOf(from.In(loc))); skip > stride {
			k = skip/stride - 1
		}

		for ; ; k++ {
			day := first.AddDays(k * stride)
			if !r.EndsOn.IsZero() && !day.Before(r.EndsOn) {
				return
			}

			slot := r.instant(loc, day)
			if !slot.Before(to) {
				return
			}

			if !slot.Before(from) && !yield(slot) {
				return
			}
		}
	}
}

// instant is the rule's wall-clock time on day, in its zone, as a UTC instant.
func (r *Rule) instant(loc *time.Location, day Date) time.Time {
	return time.Date(day.Year, day.Month, day.Day, r.StartMinute/60, r.StartMinute%60, 0, 0, loc).UTC() //nolint:mnd // minutes in an hour
}

// midnight is the instant day begins in loc, in UTC.
func midnight(loc *time.Location, day Date) time.Time {
	return time.Date(day.Year, day.Month, day.Day, 0, 0, 0, 0, loc).UTC()
}
