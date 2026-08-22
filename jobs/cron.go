package jobs

import (
	"fmt"
	"strings"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/robfig/cron/v3"
)

// cronTZPrefix is the spelling of the leading timezone declaration that Cron
// writes back out. crontab and robfig/cron accept "TZ=" as well, and Cron reads
// both — see cronTZPrefixes.
const cronTZPrefix = "CRON_TZ="

// cronTZPrefixes are the two spellings of the leading timezone declaration that
// crontab and robfig/cron both accept.
var cronTZPrefixes = []string{cronTZPrefix, "TZ="}

// Schedule decides when a job fires next.
//
// It is the seam between the Scheduler and the calendar. Cron covers standard
// crontab expressions, and anything else that can answer "when after this
// instant" is one method away: a schedule read from a table, one that stops
// after a date, one that skips holidays. Any robfig/cron Schedule satisfies
// this as-is, so a caller that wants seconds-resolution specs or the
// non-standard field extensions can bring its own parser and pass the result
// straight to Job.Schedule.
//
// Next reports the first fire time strictly after `after`, or the zero time to
// say the schedule will never fire again. It is called on the goroutine that
// owns the job, between runs rather than during them, and must not block.
type Schedule interface {
	Next(after time.Time) time.Time
}

// cronSchedule is a parsed cron expression that remembers the fields it was
// parsed from and the zone it reads them in, so a span can report the schedule
// a job is on instead of an opaque struct address.
type cronSchedule struct {
	cron.Schedule

	// loc is the zone the fields are read in, and nil for a schedule that has
	// no wall-clock time to read — "@every 30m" is a delay, not an hour.
	loc *time.Location

	// spec is the expression with any timezone prefix stripped, because the
	// zone is tracked in loc and may yet be replaced.
	spec string

	// pinned records that loc came from something specific — the spec's own
	// prefix, or a CronIn argument — rather than from a default waiting to be
	// filled in. A pinned schedule ignores SchedulerConfig.Timezone.
	pinned bool
}

// String returns the expression, always naming the zone it will actually be
// read in. That is not always the text the caller wrote: a spec that named no
// zone is reported with the one it ended up in, because a span that says
// "0 3 * * *" leaves the only interesting question unanswered.
//
// The result parses back through Cron to a schedule with these same fire times.
func (c cronSchedule) String() string {
	if c.loc == nil {
		return c.spec
	}

	return cronTZPrefix + c.loc.String() + " " + c.spec
}

// relocate returns the schedule as it reads in loc, leaving it alone if its
// location is already pinned — both the spec's own prefix and a CronIn argument
// are more specific than anything filling in a default afterward.
//
// The location is replaced on the parsed schedule rather than by re-parsing a
// rewritten spec, because a *time.Location need not have a name that
// time.LoadLocation can find its way back from — time.FixedZone produces one
// that does not.
func (c cronSchedule) relocate(loc *time.Location, pin bool) cronSchedule {
	if c.pinned || loc == nil {
		return c
	}

	// Only a wall-clock schedule has a zone to be in. A ConstantDelaySchedule
	// from "@every 30m" is half an hour no matter where it is read, so there is
	// nothing here for a location to change.
	spec, ok := c.Schedule.(*cron.SpecSchedule)
	if !ok {
		return c
	}

	relocated := *spec
	relocated.Location = loc

	return cronSchedule{Schedule: &relocated, loc: loc, spec: c.spec, pinned: pin}
}

// Cron parses a standard five-field crontab expression — minute, hour, day of
// month, month, day of week — into a Schedule for Job.Schedule. The usual
// descriptors (@hourly, @daily, @weekly, @monthly, @yearly, @every 30m) are
// accepted too.
//
//	jobs.Cron("*/15 * * * *")                        // every quarter hour
//	jobs.Cron("0 3 * * *")                           // 03:00
//	jobs.Cron("CRON_TZ=America/Chicago 0 3 * * *")   // 03:00 in Chicago
//
// # Which zone
//
// Four things can decide it, and the most specific wins:
//
//  1. A CRON_TZ= or TZ= prefix on the spec itself.
//  2. The location passed to CronIn.
//  3. SchedulerConfig.Timezone, applied to the schedules of jobs registered
//     with that Scheduler that did not settle the question themselves.
//  4. UTC.
//
// UTC rather than the host's local time, because the underlying parser defaults
// to time.Local and Go builds time.Local from the process's TZ environment
// variable. That makes the same expression mean different instants on a laptop
// and in a container, and lets one replica of a service disagree with another
// about when 03:00 is, without either of them saying anything about it.
//
// Any zone other than UTC needs the zoneinfo database at runtime. Scratch and
// distroless images generally do not have it, and the lookup fails there rather
// than silently choosing a different zone; `import _ "time/tzdata"` in the
// binary's main package embeds it.
//
// # Daylight saving
//
// A wall-clock schedule in a zone that observes it has two days a year where
// "it runs at 03:00" is not the whole answer. An hour that does not exist on
// the spring-forward day does not run; an hour that happens twice on the
// fall-back day runs twice, an hour apart — and the lease does not prevent the
// second run, having been released an hour earlier. A job that must not miss or
// repeat wants UTC.
//
// # Catch-up
//
// There is none. A fire time that passes while the process is down, or while a
// previous run of the same job is still going, is skipped rather than queued —
// see the Scheduler docs for why, and Job.RunOnStart for the escape hatch when
// a job must not be skipped by a deploy.
func Cron(spec string) (Schedule, error) {
	schedule, err := parseCron(spec)
	if err != nil {
		return nil, err
	}

	return schedule, nil
}

// CronIn is Cron for a spec that should be read in a particular zone without
// saying so in its own text, for a caller holding a *time.Location rather than
// a name — one loaded once at startup, or one that time.LoadLocation could not
// produce, such as a time.FixedZone.
//
// The location loses to a spec that names its own, and beats
// SchedulerConfig.Timezone.
func CronIn(loc *time.Location, spec string) (Schedule, error) {
	if loc == nil {
		return nil, platformerrors.Wrap(ErrInvalidCronSpec, "nil location")
	}

	schedule, err := parseCron(spec)
	if err != nil {
		return nil, err
	}

	return schedule.relocate(loc, true), nil
}

// MustCron is Cron for a spec that is a constant in the program, and panics
// rather than returning an error. Package-level schedule variables and literal
// specs inside a Register call are what it is for; a spec that came from
// configuration or a database wants Cron.
func MustCron(spec string) Schedule {
	schedule, err := Cron(spec)
	if err != nil {
		panic(err)
	}

	return schedule
}

// MustCronIn is CronIn for a spec and a location that are constants in the
// program, and panics rather than returning an error.
func MustCronIn(loc *time.Location, spec string) Schedule {
	schedule, err := CronIn(loc, spec)
	if err != nil {
		panic(err)
	}

	return schedule
}

// parseCron does the work behind Cron and CronIn, keeping the concrete type so
// the location can still be replaced.
func parseCron(spec string) (cronSchedule, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return cronSchedule{}, platformerrors.Wrap(ErrInvalidCronSpec, "empty cron spec")
	}

	fields, ok := splitTimezone(trimmed)
	if !ok {
		return cronSchedule{}, platformerrors.Wrapf(ErrInvalidCronSpec, "cron spec %q declares a timezone and no schedule", spec)
	}

	// Parsed with an explicit zone either way, so that the parser's time.Local
	// default is never the answer. Whether the caller chose it is what pinned
	// records.
	pinned := hasTimezone(trimmed)

	located := trimmed
	if !pinned {
		located = cronTZPrefix + "UTC " + trimmed
	}

	schedule, err := cron.ParseStandard(located)
	if err != nil {
		return cronSchedule{}, platformerrors.Wrapf(platformerrors.Join(ErrInvalidCronSpec, err), "parsing cron spec %q", spec)
	}

	// Only a wall-clock schedule carries a location; "@every 30m" parses to a
	// plain delay, which has none and needs none.
	var loc *time.Location
	if wallClock, isWallClock := schedule.(*cron.SpecSchedule); isWallClock {
		loc = wallClock.Location
	}

	return cronSchedule{Schedule: schedule, loc: loc, spec: fields, pinned: pinned}, nil
}

// hasTimezone reports whether the spec opens with a timezone declaration.
func hasTimezone(spec string) bool {
	for _, prefix := range cronTZPrefixes {
		if strings.HasPrefix(spec, prefix) {
			return true
		}
	}

	return false
}

// splitTimezone separates a leading timezone declaration from the fields that
// follow it, keeping only the fields — the zone is tracked separately because
// it may still be replaced, and because String writes it back in one spelling
// rather than whichever was used.
//
// ok is false for a spec that declares a timezone and stops. That is worth a
// return value rather than falling through to the parser, which indexes to the
// first space without checking there is one and panics on it.
func splitTimezone(spec string) (fields string, ok bool) {
	if !hasTimezone(spec) {
		return spec, true
	}

	_, rest, found := strings.Cut(spec, " ")
	if !found {
		return "", false
	}

	return strings.TrimSpace(rest), true
}

// describeSchedule names a Schedule for telemetry. Cron's own schedules report
// their expression and zone; a caller's implementation is described by its type
// unless it implements fmt.Stringer, since the alternative is a pointer address
// that says nothing about when the job runs.
func describeSchedule(schedule Schedule) string {
	if stringer, ok := schedule.(interface{ String() string }); ok {
		return stringer.String()
	}

	return fmt.Sprintf("%T", schedule)
}
