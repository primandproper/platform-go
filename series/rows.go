package series

import (
	"time"

	"github.com/primandproper/platform-go/v15/series/internal/seriesdb"
)

// The typed seam between the generated package and the domain types.
//
// Every conversion is a struct literal, so a renamed or retyped column is a
// compile failure here rather than a scan error at run time. The row structs are
// nominal per statement; the ones that project the same columns in the same
// order convert to GetSeriesRow and GetOccurrenceRow, and that conversion is
// where their agreement is asserted.

// seriesFromRow converts a series row. It fails only on a date the table holds
// that is not one, which nothing this package writes can produce.
func seriesFromRow(r *seriesdb.GetSeriesRow) (*Series, error) {
	startsOn, err := ParseDate(r.StartsOn)
	if err != nil {
		return nil, err
	}

	var endsOn Date
	if r.EndsOn != "" {
		if endsOn, err = ParseDate(r.EndsOn); err != nil {
			return nil, err
		}
	}

	return &Series{
		CreatedAt:         r.CreatedAt.UTC(),
		MaterializedUntil: r.MaterializedUntil.UTC(),
		LastUpdatedAt:     utcPtr(r.LastUpdatedAt),
		ExhaustedAt:       utcPtr(r.ExhaustedAt),
		ID:                r.ID,
		Scope:             r.Scope,
		Rule: Rule{
			StartsOn:      startsOn,
			EndsOn:        endsOn,
			TimeZone:      r.TimeZone,
			Weekday:       time.Weekday(r.Weekday),
			StartMinute:   int(r.StartMinute),
			IntervalWeeks: int(r.IntervalWeeks),
		},
	}, nil
}

// occurrenceFromRow converts an occurrence row.
func occurrenceFromRow(r *seriesdb.GetOccurrenceRow) *Occurrence {
	return &Occurrence{
		CreatedAt:     r.CreatedAt.UTC(),
		ScheduledAt:   r.ScheduledAt.UTC(),
		SlotAt:        utcPtr(r.SlotAt),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ID:            r.ID,
		Scope:         r.Scope,
		SeriesID:      r.SeriesID,
		State:         State(r.State),
		ReplacedBy:    r.ReplacedBy,
		Reason:        r.Reason,
	}
}

// utcPtr normalizes an optional timestamp to UTC, preserving absence. Postgres
// hands back a time in the session's zone, MySQL in the server's, and SQLite
// whatever the string parsed as.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}
