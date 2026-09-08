/*
Package queries is the registered clients schema described as data: the
canonical table name, the table's columns in the order every read projects them,
and the subsets each write assigns.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders this table through database/querygen
into the canonical .sql files sqlc is run over; the store reads the same table
name to render its prefixed identifiers. A column list spelled in both places
could differ in one name, and the symptom would be a check that passes over SQL
nobody executes.

So it is spelled once, here, and both halves read it. The .sql files beside this
file are the generator's output — see [Render] and
authentication/oauth2clients/internal/queriesgen.

# Why the table does take the standard set

[querygen.Generator.StandardCRUD] emits what a conventional table gets: reads and
writes keyed on the row's own id and, where the caller names one, on an ownership
column. comments and issuereports both decline it, because every statement they
run is keyed on columns the standard set has no place for.

This table is the other case. A registration is a resource in exactly the sense
that set assumes — it has its own id, it is soft-deleted, and one ownership
column scopes every statement — so the get, the page, the update and the archive
are the generated ones.

Two members of the set are omitted and four statements are written out beside
what remains. The existence check is omitted outright, because nothing here asks
whether a registration exists without also wanting to read it. The create is
omitted and replaced: client_id carries a unique index, and the standard insert
would raise on a collision, which is every backing store parsing a dialect's
constraint text to tell a duplicate from a broken database. The other three
authored statements are the ones the standard set has no way to express — the
create's read-back of its own creation time, the self-service page keyed on the
scope and the owner both, and the authorization server's lookup.

# The one statement with no scope in it

[byClientID] names no tenancy column, and it is the only statement here that
does not. It is not an unscoped read: it is the read that *produces* a scope,
keyed on a server-minted identifier the schema declares globally unique, whose
only caller is the oauth2server.Store decorator answering an /authorize or
/token request that has named a client and nothing else.

That is the machinery carve-out the tenancy convention makes for a component
servicing itself, and it is narrow in the way the convention asks: there is no
scope the caller could have supplied, the identifier is not a name anybody
chose, and resolving a client grants nobody anything — oauth2clients.Client.Admits
is what turns a resolved registration into a permitted one, and it runs
somewhere else entirely.
*/
package queries
