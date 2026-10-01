/*
Package database stores session records in a SQL table.

Reach for it when losing the cache must not sign everybody out, or when a
sign-out has to be enforceable rather than very nearly enforceable. Otherwise
sessions/cache is cheaper and does the same job.

	backend, _ := sessionsdatabase.NewBackend[Principal](
		&sessionsdatabase.Config{TablePrefix: "app"}, db,
		sessionsdatabase.WithSweeper(ctx, 5*time.Minute),
	)
	store, _ := sessions.NewStore(backend)

The dialect is taken from the database.Client rather than configured. A
configured dialect that disagrees with the client it is paired with produces
syntactically valid SQL the server rejects at runtime, and there is no way to
notice before it does.

# What the table buys

Two operations are single statements here and approximations in the cache
backend, and both matter for the same reason — a request that read a session
just before it ended must not be able to write it back afterwards:

Update is one UPDATE with a WHERE clause. A row that has been deleted is not
recreated, so a sign-out that has committed stays committed.

Rename runs its DELETE and INSERT in one transaction. Either the old identifier
stops resolving and the new one starts, or neither does. There is no interval in
which both work, which is the interval a session-fixation attack needs.

# Sweeping

A cache reclaims its own expired entries; a table does not. Without a sweep this
one grows with every session ever created — the rows are unusable long before
they are removed, since the store decides expiry from the record's own anchors,
but they are still there.

WithSweeper runs it in-process on a timer. In a fleet that is one sweeper per
replica doing the same delete; a scheduler calling Sweep once is the better
shape, and the reason Sweep is exported.

# The schema

The DDL lives in the migrations subpackage, rendered for a dialect and a table
prefix. It is not shipped as a numbered migration file — those are numbered
globally per consumer, so a platform-owned number would collide with theirs.

expires_at exists for the sweeper alone and appears in no read predicate. Which
sessions are live is decided above this layer, from created_at and last_seen_at
against the store's policy, so that both backends answer the question
identically — and so that clock skew between a writer and a reader cannot hide a
live session.

scope and principal are who holds the session, and they are what makes this the
only backend that can answer "which sessions does this person hold, and end the
ones that are not this one". The pair is indexed together and every statement in
that surface binds both: a revocation missing the scope reaches every tenant
that spells an identifier the same way, and one missing the principal signs out
everybody in the tenant. Neither has a column default, and scope's absence is
the deliberate one — the empty string is tenancy.Global(), so a default would
hand the global scope to a write that forgot the column.

The device metadata sits beside them and appears in no UPDATE either, for
created_at's reason one step out: it describes the moment a session was
established, and a recorded device that moved under a user would describe
nothing at all. A renewal carries both across by writing a fresh row from the
record it read.

created_at appears in no UPDATE. It anchors the absolute timeout, and leaving it
out of the statement makes "an update never extends a session's total lifetime"
structural rather than a rule somebody has to remember.

# Where the SQL comes from

Nothing in this package composes a statement. The ones it executes — the create,
the read, the overwrite, the existence check, the delete, the listing and
revocations a person's security page makes, and the sweep — are rendered from
sessions/database/internal/queries into one canonical .sql per dialect, checked
against this package's own schema by sqlc with no database running, and executed
through the querier sqlc-gen-unison generates from those same files. A column
renamed in migrations is then a failed `make unison` rather than a runtime scan
error on whichever dialect noticed first.

The rendered .sql is committed even though nothing imports it, for the reasons
identity's is: it is what `sqlc compile` is handed, it anchors the drift gate a
test states byte for byte against the renderer, and it is what a reviewer reads
when they want to see a statement in the spelling its arguments still have
names in. `make generate` writes it; `make unison` renders the per-dialect
schema beside it and runs the emitter over the pair.

The sweep is the one statement worth stopping at, because it is the only one
here whose predicate is not an equality and the only one that reads a clock. Its
deadline is bound rather than read off the server: expires_at is stamped as
now-plus-a-TTL from the clock this backend was constructed with — the interface
hands this layer a duration, not an instant — so a comparison against the
server's CURRENT_TIMESTAMP would be two clocks deciding one row, and under the
injected clock a test controls the two are years apart. See querygen.AtMostArgument.

One consequence of the tier is visible on SQLite and nowhere else. That engine
has no date type, so a timestamp is text, and the generated querier binds one in
the shape the engine's own CURRENT_TIMESTAMP writes — whole seconds. A session's
three stamps are therefore stored truncated to the second there, which moves
every deadline computed from them at most one second earlier. Earlier is the
safe direction, and the other two dialects store what they are given.

# Encoding

Payloads go into the data column through an encoding.Codec, CBOR by default. A
session with no payload stores NULL and reads back as nil rather than as a zero
value.

Rows written with one encoding are unreadable through another and carry no
record of which wrote them, so changing WithCodec on a deployed store signs
everybody out. Record.Version cannot soften that: it is a column, not part of
the blob, so it catches a changed payload shape and not a changed encoding.
*/
package database

//go:generate go run ./internal/queriesgen
