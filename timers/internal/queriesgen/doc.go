/*
Command queriesgen writes the canonical sqlc input for the timers schema, one
file per dialect it serves, from timers/internal/queries.

It is run by `make generate`, and the file it writes is checked in. Nothing
imports it and nothing executes it: it exists so that `sqlc compile` can check
the statements the timer set executes against the schema timers/migrations
renders, at build time, with no database running. What the set executes is the
querier sqlc-gen-unison generates from that same file — the same text with the
consumer's table prefix substituted for {{prefix}} and the argument references
rewritten into bind markers.

Three dialects and two statement sets. Postgres's file is the corpus unison.yaml
generates timers/internal/timersdb from; MySQL's and SQLite's are the split
corpus unison.split.yaml generates timers/internal/timerssplitdb from. The files
are one per dialect either way, so this command neither knows nor cares which
set a file belongs to — that is queries.Render's decision.

Because the check is what the file is for, this command also renders the other
half of it: `-schema <dialect>` prints the DDL sqlc reads it against, so
.scripts/sqlc_compile.sh has no hand-written copy of a schema to keep in step
with timers/migrations. `make unison` calls it that way before running the
emitter over the pair.

	go run ./internal/queriesgen                   # writes internal/queries/<dialect>_generated.sql
	go run ./internal/queriesgen -schema postgres  # prints the DDL to stdout
*/
package main
