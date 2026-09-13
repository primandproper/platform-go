/*
Package schemaconvention is where the module's schema convention is asserted,
and it holds nothing else.

Every table in this module that stores consumer rows carries the same three
columns:

	created_at      NOT NULL, defaulted by the server
	last_updated_at NULL until something changes the row
	archived_at     NULL until the row is soft-deleted

The convention is what database/querygen reads a table's shape from. It decides
which statements a table gets: a table spelling its last-mutation column
updated_at receives no update, no updated_after/updated_before window and no
UpdatedAfter/UpdatedBefore support from filtering.QueryFilter — and receives them
silently, because a generator that emits fewer statements looks exactly like a
table that wanted fewer. A created_at with no DEFAULT is worse: the generated
create omits the column, passes sqlc compile, and dies on a not-null violation
the first time it runs.

Both failures are invisible per-package, which is why the assertion is not
per-package. This package's test names every schema-shipping table in the module
exactly once — as conventional or as exempt, with the exemption's reason — so a
new table that quietly skips the triple fails a test rather than passing thirteen
of them. It imports every migrations subpackage and is imported by nothing.

Every table is reached through a roster of those subpackages, so the claim in
that paragraph is only as wide as the roster: a package missing from it ships
tables nothing classifies, and nothing about a green suite says which packages it
covered. The roster is therefore checked against a walk of the tree in both
directions — a migrations directory with no entry fails, an entry naming no
directory fails, and each entry's renderer has to create the tables its own
package's .sql files declare, so an entry cannot cover one package twice and
another not at all.

One table does carry created_at NOT NULL with no DEFAULT: action_links, which is
exempt and says why where it is named. Its store assigns the column on the
insert, because a link's creation time has to come from the same clock its
expires_at and purge_after were derived from.

A table is exempt only for a reason that outlives whoever wrote it, and there are
two shapes. A table a sweeper keeps small — sessions, work queue items, outbox
messages, WebAuthn ceremony state, metering's ingest ledger, and the tables whose
rows are bearer credentials named by their own digest — cannot carry a soft
delete, because archived_at there either does nothing or keeps the table growing
forever. A mapping row between two tables is not listed, filtered or soft-deleted
independently of its parents, so the triple would be three columns no statement
reads. audit_log_entries is exempt for reasons of its own, which the test spells
out where it names it.
*/
package schemaconvention
