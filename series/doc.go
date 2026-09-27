/*
Package series stores standing appointments: a weekly rule ("Tuesdays at 4pm,
every week, from September 2"), the concrete occurrences it implies, and the
exceptions those occurrences accumulate — each one skipped, moved or replaced
without touching the rule.

# What an occurrence is for is not here

The platform half knows nothing about what an occurrence is for — the same
bargain comments makes with an opaque target and timers makes with an opaque
payload. A lesson's teacher, its students and its price are the consumer's, in
the consumer's own table keyed by occurrence id; so is a per-occurrence override
(a cover teacher on one Wednesday), and so is what happened when it occurred —
attended, a no-show, cancelled late. [State] is only about existence:
scheduled, skipped or moved.

That is also why this is not timers. A timer fires and is done; an occurrence is
a row that persists, is edited, and is what a consumer's rows hang from.

# Two layers

  - A [Series] is a [Rule] with no meaning: scope, time zone, weekday, time of
    day, interval in weeks, start date, optional end date. No RRULE — weekly
    with an interval is what a standing appointment is, and an RFC 5545 parser
    owns no table, so it would be a primitive if it is ever wanted.
  - An [Occurrence] is one row per instance: the series, the instant it
    happens, the instant the rule put it at, its state, an optional pointer to
    the occurrence that replaced it, and a free-text reason.

# Five commands

Each is a store write in the caller's transaction, one guarded action with one
audience, because a consumer that offers one button for all five has its
software guessing which was meant:

  - [Store.SkipOccurrence] skips one.
  - [Store.SkipWindow] skips a date range across every series in the scope — a
    closure, for a holiday or a studio that is shut for a week.
  - [Store.EndSeries] ends a series from a date.
  - [Store.MoveOccurrence] moves one to a new instant, keeping its id so the
    consumer's rows move with it.
  - [Store.AddReplacement] adds an occurrence against a skipped one — a make-up.

"Cancel one lesson" is therefore a skip here and an outcome in the consumer's
table, written in one transaction:

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		if _, err := store.SkipOccurrence(ctx, tx, scope, occurrenceID, "teacher ill"); err != nil {
			return err
		}

		return lessons.RecordOutcome(ctx, tx, scope, occurrenceID, lessons.CancelledByStudio)
	})

# The horizon

Occurrences are written ahead, not computed on read: a week view is a read of
rows, and a skip, a move or a consumer's attendee list needs a row to attach
to. [Worker] writes every series out to [WorkerConfig].Horizon on a timer, under
a distributed lock rather than SKIP LOCKED so all three dialects can serve it,
and [Store.Materialize] is what it calls — idempotently, since the unique index
on (series_id, slot_at) turns a slot already written into a write that does
nothing. A consumer that wants a new series' rows at once calls Materialize
itself, in the transaction that created it.

No write reaches further past now than [MaxWriteAhead] — not a Materialize,
not a closure, not the worker's Horizon — and a rule's dates fall in [MinYear]
to [MaxYear]. The bounds are there so a typo'd year is [ErrTooFarAhead] or
[ErrInvalidRule] rather than one insert per week for every week it names.

# Time

A rule is wall-clock time in its own zone, so a 4pm lesson is at 4pm on both
sides of a daylight-saving change, and every instant this package stores is
that wall-clock time resolved to UTC. Instants are stored in whole seconds on
every dialect, because SQLite keeps no more than that; a [Window] is [From, To)
and reads the same everywhere because of it.

# Privacy

Neither table has a subject. A series is a rule in a tenant, and an occurrence
is an instance of one; who attends, teaches or pays is in the consumer's table,
keyed by occurrence id, and is the consumer's to export and erase. There is no
series/privacy, and the reason column is the consumer's own operational text: a
consumer that writes a person's details into it has put personal data in a
column this package cannot find by person.

# The tables are yours to create

series/migrations renders the DDL for a dialect and prefix. Nothing here creates
a table on its own.

# Where the SQL comes from

Every statement this package executes is generated. The tables' facts are
spelled once, in internal/queries; `make generate` renders them through
database/querygen into canonical .sql files; `make sqlc_compile` checks them
against the DDL on all three dialects; sqlc-gen-unison emits internal/seriesdb
from the same files, and that is what the store executes.
*/
package series

//go:generate go run ./internal/queriesgen
