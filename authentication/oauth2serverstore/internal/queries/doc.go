/*
Package queries is the authorization server's schema described as data: the four
canonical table names, each table's columns in the order every read projects
them, and the columns a write may leave NULL.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders these tables through database/querygen
into the canonical .sql files sqlc is run over; sqlc-gen-unison generates the
querier the store executes from those same files. A column list spelled in two
places could differ in one name, and the symptom would be a check that passes
over SQL nobody executes.

The .sql files beside this one are the generator's output — see [Render] and
authentication/oauth2serverstore/internal/queriesgen. Why they are committed
at all, when the generated Go carries the same statements in executable form, is
the store's package comment, under "Where the SQL comes from".

# Four tables, and not one standard set between them

[querygen.Generator.StandardCRUD] emits the set a conventional table gets, and
serves none of these. The three credential tables have no id: a code, an access
token and a refresh token are each keyed on a hex SHA-256 digest of the
credential, which is the natural key that makes a dump of this database
unredeemable — nothing in it can be presented anywhere. The registration table
has an id and no list, because nothing enumerates registrations: a client is
read by the identifier it presents, and RFC 7591 registration is open to
anonymous callers, so a paged read over that table would be a way to enumerate
every client an authorization server has ever accepted.

None of the four carries the convention triple either. There is no
last_updated_at, because none of these rows is edited — the writes here stamp a
nullable timestamp that was NULL, which is a different thing from an update —
and no archived_at, because a registration is deleted outright and a credential
is swept once it is dead. So the statements are the keyed forms throughout, and
the predicates every one of them carries are the ones the [querygen.Match]
values below name and no others.

# The statements the guards are

Three shapes carry the design, and each is a predicate rather than a check in Go.

The consume — of an authorization code, and of a refresh token — is one guarded
UPDATE. `redeemed_at IS NULL` is what makes two concurrent redemptions of one
credential resolve to exactly one winner; the deadline comparison closes the
window in which a credential expires between a read and the write that follows
it. A store that checked either of those in Go would have both races, and
neither would show up in a test that redeems one credential at a time. The
refresh token's carries a third, `revoked_at IS NULL`, which is what keeps a
revoked token from reading as a replay — it was never exchanged, so reporting
reuse would revoke a family every time somebody signs out and their client
retries.

The revocation guards on `revoked_at IS NULL`, which is what makes it idempotent
in the way the caller needs: a second revocation matches nothing and reports
zero rows rather than moving the timestamp, so the record still says when the
token actually stopped working.

The create is an insert that skips a row already there rather than raising on
it. That is what makes ErrClientExists and ErrRecordExists reportable without
parsing a driver's error — a duplicate key leaves zero rows affected instead of
a dialect-specific SQLSTATE — and it is deliberately not an upsert: registration
is open to anonymous callers, so the row already there has to win, or one of
them takes over another's client by guessing an identifier.

# Which clock the deadlines are read against

Every deadline comparison here binds an instant rather than asking the server
for one, which is [querygen.AtMostArgument] rather than [querygen.CurrentTime], and
it is this store's decision rather than querygen's default. The deadlines in
these columns were stamped by the authorization server's clock — the same clock
that decided a code lives for a minute and a token for an hour — so the
comparison that decides whether one has passed is made against that same clock.
Read against the database's instead, "issued for fifteen minutes" and "expired"
would be measured by two clocks that agree only by luck.

Sweep is the other half of it, and there the bound time is not even now: a
caller sweeping at a horizon an hour back reclaims only what nothing is still
deciding about, which is a thing the server's clock has no argument to express.
*/
package queries
