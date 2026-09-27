/*
Package queries is the series schema described as data: the canonical table
names, their columns in the order every read projects them, and the sixteen
statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through the
querier sqlc-gen-unison generates from those files.

# The sixteen statements

  - CreateSeries, GetSeries and ListSeries write, read and page the rules.
  - EndSeries writes a rule's end date; SkipSeriesFrom is its sweep over the
    occurrences the rule put on or after it.
  - AdvanceSeries records how far a rule has been written out, never moving it
    back; DueSeries is the horizon worker's read, and the one statement that
    names no scope.
  - MaterializeOccurrence writes one slot unless it already has a row.
  - CreateOccurrence writes a replacement, and LinkReplacement points the
    skipped occurrence at it.
  - GetOccurrence, ListOccurrences and ListSeriesOccurrences read occurrences,
    the last two over a window of time.
  - SkipOccurrence and MoveOccurrence are the guarded state changes, and
    SkipWindow is the closure.

A window is (after, through] in the statements and [from, to) in the store's
API; see window in queries.go for why the two agree.

The rendered .sql files beside this one are the generator's output — see [Render]
and series/internal/queriesgen. They exist so `sqlc compile` can check these
statements against the schema migrations renders, at build time, with no
database running, and so the drift gate can pin the committed text byte for
byte against the renderer.
*/
package queries
