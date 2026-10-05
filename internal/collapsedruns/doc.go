/*
Package collapsedruns is where the two schemas that were versioned runs in v14
are pinned to the tables those runs left behind, and it holds nothing else.

audit/migrations shipped as two versions and
authentication/signin/refreshtokens/migrations as four, and both were collapsed
into one schema each at the next major. The collapsed schema is a CREATE TABLE
IF NOT EXISTS, so it does nothing to a database that already has the table, and
that is only safe if the table it would have created is the table the run did:
the same columns in the same order with the same types, nullability and
defaults, the same indexes under the same names, and no table the run left that
the schema does not. A collapse written as version 1 plus the later columns gets
that wrong in ways no single-package test sees — the order the ALTERs appended
columns in, an index a later version added, the name SQLite's rebuild renamed
the old table to.

So the test here renders the frozen v14 run from testdata and the package's
schema as it stands, applies each to a fresh database under its own prefix, and
compares what the database reports about both. It then applies the schema again
over the run's tables and checks that nothing changed, which is the claim a
consumer whose database is at the run's latest version relies on. SQLite runs
with every `go test`; Postgres and MySQL run behind the container gate.

The testdata is the run as it shipped and is never edited. When either package
grows a new version, the version goes in a ddl.Migrations in that package and is
not this test's business: what this pins is that version 1 of the new run is
where the old one ended.
*/
package collapsedruns
