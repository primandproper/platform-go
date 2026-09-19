/*
Package main renders authentication/passkeys/internal/queries into the canonical
.sql files beside it, one per dialect, and prints the schema those files are
checked against.

It is what `go generate ./authentication/passkeys/...` runs, and what
.scripts/sqlc_compile.sh and .scripts/unison_generate.sh call with -schema to get
the DDL at the empty table prefix. Neither output is edited by hand: the queries
are regenerated and diffed by CI, and the schema is rendered from the package's
own migrations so there is no second copy to drift.
*/
package main
