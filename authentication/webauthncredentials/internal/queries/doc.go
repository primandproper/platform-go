/*
Package queries is the WebAuthn ceremony session schema described as data: the
canonical table name, its columns in the order every read projects them, and the
four statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through
the querier sqlc-gen-unison generates from those files. A column list spelled in
both places could differ in one name, and the symptom would be a check that
passes over SQL nobody executes.

# Why there is no standard set

[querygen.Generator.StandardCRUD] serves a table with a surrogate id, a paged
list keyed on it, and the convention triple of timestamps. This table has none
of them, and every absence is deliberate rather than an omission — see
authentication/webauthncredentials/migrations. Ceremony state lives for the length
of one registration or login and a sweeper removes it once its deadline passes,
so a soft delete would keep rows nothing can ever read; a challenge is written
once and consumed once, so there is no last mutation to record; and nothing
lists ceremonies, so there is no cursor and nothing for an id to be.

What is left is a natural key that carries a meaning a surrogate id would not.
The challenge is at least 16 bytes of cryptographic randomness and it is what
the second request of a ceremony arrives holding, so it is the key every
statement here addresses a row by — which is the keyed form's whole purpose, and
the same shape shredding's subject keys use.

# The four statements

  - UpsertSession writes one ceremony's state, converging on the challenge. A
    ceremony begun twice replaces the earlier one rather than being ignored.
  - GetSession reads the state and the deadline for one challenge, projecting
    less than the table because the caller is already holding the key.
  - DeleteSession removes it, and its row count is what decides who owns the
    ceremony when two requests answer the same challenge at once.
  - SweepExpiredSessions removes everything past its deadline, against the
    server's clock.

The sweep is the one whose meaning moved in the port, and [sweep] says why: a
bound time.Time is stored by SQLite's driver in Go's own rendering, which
compares against nothing the server writes, while CURRENT_TIMESTAMP is one
expression all three dialects agree on. Nothing depends on the sweep's clock
being the store's, because the sweep is not what makes a ceremony expire —
Consume refuses a row past its deadline whether or not anything has removed it
yet.

The rendered .sql files beside this one are the generator's output — see
[Render] and authentication/webauthncredentials/internal/queriesgen. Nothing
imports them and nothing executes them: they exist so `sqlc compile` can check
these statements against the schema migrations renders, at build time, with no
database running, and so the drift gate can pin the committed text byte for
byte against the renderer.
*/
package queries
