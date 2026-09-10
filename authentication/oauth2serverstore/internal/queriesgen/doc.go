/*
Command queriesgen writes the canonical sqlc input for the authorization
server's schema, one file per dialect, from
authentication/oauth2serverstore/internal/queries.

It is run by `make generate`, and the files it writes are checked in. Nothing
imports them and nothing executes them: they exist so that `sqlc compile` can
check the statements this store executes against the schema
authentication/oauth2serverstore/migrations renders, at build time, with no
database running. What the store executes is the querier sqlc-gen-unison
generates from these same files — the same text with the consumer's table prefix
substituted for {{prefix}} and the argument references rewritten into bind
markers.

Because the check is what the files are for, this command also renders the other
half of it: `-schema <dialect>` prints the DDL sqlc reads them against, so
.scripts/sqlc_compile.sh has no hand-written copy of a schema to keep in step
with the migrations package. `make unison` calls it that way before running the
emitter over both.

	go run ./internal/queriesgen                 # writes internal/queries/<dialect>_generated.sql
	go run ./internal/queriesgen -schema sqlite  # prints the DDL to stdout
*/
package main
