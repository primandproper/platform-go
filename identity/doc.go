/*
Package identity stores the thing being authenticated.

This module ships every engine an application needs to authenticate somebody —
argon2 hashing, TOTP, WebAuthn, OAuth2, session management, token issuance,
authorization policy, tenancy — and until now shipped nothing to authenticate.
Every consumer supplied the User themselves, which meant every consumer wrote
the schema, the repository, the paging, the soft delete, the scoping, the
mocks, and the fakes. Measured in one of them that came to roughly eight
thousand hand-written non-test lines, and eighteen of the twenty fields on its
user type were fields every application has.

So this package owns the four things — users, accounts, memberships, invitations —
in the same way [github.com/primandproper/platform-go/v14/webhooks] owns
endpoints: a Store interface, a SQL implementation of it, the DDL for three
dialects, and a mock. Over that it owns [Service], the operations that are more
than one write — a registration, an invitation answered, an ownership
transferred — each in one transaction with the consumer's own writes joining it
through [Hooks]. Over that again, [github.com/primandproper/platform-go/v14/identity/grpc]
serves it: twenty-eight RPCs, the .proto they are described by, a typed client,
and the permissions each one wants. A consumer keeps its policy, its
authentication, and whatever columns are genuinely its own; it does not keep a
users table, it no longer keeps the transaction-shaped code around one, and it
no longer writes the service and the converters over that.

That last part reverses a line this module drew on purpose, and the reasons are
in identity/grpc's own documentation and in the README's "Transports" section.
The short version is that all three objections — a library versioning your API,
in types your proto does not have, under a scoping rule it guessed — were
properties of a module that also held the primitives, and the split answers each
of them.

# Where the boundary with authentication falls

A credential lives here, on the user.

That is the load-bearing decision in this package and it is worth being
explicit about, because the obvious alternative — identity holds who somebody
is, authentication holds how they prove it — reads cleaner and is wrong for the
shape this module already has. The authentication subpackages are engines:
[github.com/primandproper/platform-go/v14/authentication/argon2] hashes and
compares, [github.com/primandproper/platform-go/v14/authentication/totp]
generates and validates, [github.com/primandproper/platform-go/v14/authentication/webauthn]
attests. None of them stores anything, none of them wants to, and giving one of
them a table would mean an application that hashes passwords in a different
package still ends up with a credential store it did not choose.

Splitting the row instead — user here, credential there — buys a tidier
diagram and costs a join on the single hottest read in any application, the one
that turns a username into something to compare a password against. It also
creates a second table with the same primary key, the same soft delete, and the
same scope, which two components would then have to keep consistent with each
other. A password hash is not a separate entity from the person; it is a column
on them.

What that means concretely: HashedPassword, RequiresPasswordChange,
PasswordLastChangedAt, TwoFactorSecret, TwoFactorSecretVerifiedAt, and the
email-address verification token are User fields, written and read through this
Store. The engines remain engines — this package never hashes, never compares,
never generates a TOTP secret. It stores what they produce, and
[User.Redacted] is how a user reaches a response body without them.

What is deliberately not here, and why it is not an omission: WebAuthn
credentials, password reset tokens, and sessions. Each is a set per user rather
than a column on one, each has a lifecycle of its own (a credential is
registered and revoked, a reset token is issued and burned, a session expires),
and each is consumed by exactly one engine. Their home is beside that engine —
the same rule that put the password hash here, applied to a fact that is not a
column. Sessions live in [github.com/primandproper/platform-go/v14/sessions],
WebAuthn credentials and ceremonies in
[github.com/primandproper/platform-go/v14/authentication/webauthn/database], and
password reset tokens in
[github.com/primandproper/platform-go/v14/authentication/passwordreset], which
also owns the two properties a consumer writing that table by hand gets wrong:
the token is stored as a digest, and single use is enforced by the store rather
than by whoever called it.

# Passwordless users

A user with no password is a supported user, not an unfinished one.

This module ships a WebAuthn engine and an OAuth2 one, and a registration
through either produces somebody there is no hash to store for. So
HashedPassword is not required by [User.ValidateWithContext] and never was
load-bearing: the check it was written for — catching the caller who forgot to
hash — could not work, because a plaintext password is a non-empty string and
passed. What it did instead was make passkey-only registration unspellable, and
make the obvious profile save fail, since every bulk read and [Principal.User]
returns a redacted user and the read-modify-write then had nothing to put in
the field.

The empty string means the user holds no password credential. What it must
never be read as is "any password will do". This package stores what an engine
produced and never compares, so that obligation lands on the sign-in flow: ask
[User.HasPassword] before reaching for
[github.com/primandproper/platform-go/v14/authentication/argon2], rather than
handing the engine an empty hash and trusting it to error. A user who has no
password should be refused a password sign-in and sent to the credential they
do have — which is a different answer from "wrong password", and only the flow
is in a position to give it.

A user acquires a password later through [CredentialStore.UpdateUserPassword],
which is the only writer of the column and refuses an empty hash. The two rules together
are what keep the state honest: a user is passwordless because they registered
that way, and no write can walk a user who has one back to none by forgetting
the field.

Whether a registration may go through without a password remains the
consumer's call, like the rest of registration policy. This package stops
having an opinion, rather than acquiring the opposite one.

# Scope is not the account

Every row here carries a [github.com/primandproper/platform-go/v14/tenancy.Scope],
and every read filters on it — the module's rule, not an exception to it. The
scope is *not* the account. Accounts are rows in this schema; the scope is
whoever owns the directory those accounts and users live in.

For nearly every application that is [tenancy.Global], and the package then
behaves exactly as an unscoped one would: one directory, usernames unique
across it, accounts belonging to users who belong to accounts. An application
that runs several isolated directories out of one database — a reseller per
customer, a per-region deployment sharing a cluster, a staging tenant beside a
production one — names them with a scope, and a username is then unique within
a directory rather than across all of them.

The alternative was to make an account *be* a scope, which is tempting because
tenancy.Scope has always assumed an organization exists without being able to
name one. It is not what this package does. Doing it would mean an application
whose tenant is not an account — a workspace above accounts, a project below
them — could not use this store at all, and it would turn a consumer's
modeling choice into a compatibility promise this module then owes forever.
An account has a scope, like every other row in this module.

# Extending a user

A consumer with columns of its own puts them in a side table keyed by user ID,
and joins.

That is not a hedge, it is the whole value. The moment this package accepts
configurable columns it can no longer own its migrations, and owning the
migrations is what a consumer is actually adopting — the schema, the indexes
that make the reads fast, and the guarantee that both move together. An avatar
reference into another domain and a birthday for an age gate are the two
examples that motivated this package's existence, and both are exactly the kind
of thing a side table holds well.

# Getting the tables

The DDL lives in [github.com/primandproper/platform-go/v14/identity/migrations],
rendered per dialect and table prefix, and hands to database/migrate's
WithGeneratedMigration so nothing is copied into a consumer's repository. See
that package for why no numbered migration file ships.

That package also answers which tables exist, at your prefix, through its Tables
function — the list is complete and read from the DDL, so a between-tests
TRUNCATE, a backup policy or a privacy inventory names every one of them without
anybody copying seven names out of the schema.

# The operations, and what a consumer still writes

A registration is three writes in one transaction:

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		if err := store.CreateUser(ctx, tx, scope, user); err != nil {
			return err
		}
		if err := store.CreateAccount(ctx, tx, scope, account); err != nil {
			return err
		}
		return store.CreateMembership(ctx, tx, scope, membership)
	})

A user without an account, or an account without an owner, is the failure mode
every application discovers in production rather than in a test, and the shape
above is what rules it out — which is why [Service] ships it rather than this
documentation showing it. [Service.Register] is that block, and its eight
siblings are the rest of what the block-writing turned out to be: the
invitation lifecycle, an ownership transfer, a default-account switch, an
archival with its membership fan-out, and the two administrative status
changes. Measured in the consumer this package was extracted from, the layer
those replace is a little over two thousand lines.

The transaction is the Service's there rather than the caller's, which is the
one place this package departs from the module's store convention and does so
on purpose: an operation is several store writes plus the consumer's own, and
something has to own the transaction they share. [Hooks] is how the consumer
gets into it — one method per operation, each called inside that transaction,
so an audit entry, a data change event or a search stamp commits with the row
or neither does.

What a consumer still writes is the policy, and that is the point of the split.
Whether a registration requires a password, whether an invitation is required
to begin one, what a username may look like, how long a link lives, who may
invite, which transactional email goes out — all application judgement, all
decided before the call. The Service holds none of it, and the omission is the
design: a package that decided any of those would be a package a consumer
forks the first time their answer differs.

# The transaction is the caller's

Every write in this package takes a
[github.com/primandproper/platform-go/v14/database.Tx] and every read takes the
wider [github.com/primandproper/platform-go/v14/database.SQLQueryExecutor]. That
is the module's store convention rather than this package's invention, and
[Store] carries the argument for it.

What it means in practice is that the transaction in the example above is not
optional and not only for registration. A consumer's write almost never travels
alone: an audit entry, an outbox event and the row itself are one fact, and a
store that opened its own transaction is a store whose companions land in a
second one. A caller with genuinely nothing to join writes the same
Client.WithTransaction block for one write.

The reads take the wider type so that one method serves a caller holding
Client.Reader() and a caller inside a transaction, and the second sees that
transaction's own uncommitted writes — so the user the block above created can
be read back inside it.

# Where the SQL comes from

Every statement this package executes is generated, through a pipeline with two
committed artifacts, and the duplication between them is deliberate.

The schema's facts — the table names, each table's columns in projection order,
the subsets a write may assign — are spelled once, in identity/internal/queries.
`make generate` renders them through database/querygen into the canonical .sql
files beside that package, in sqlc's spelling: named statements whose arguments
are sqlc.arg references. sqlc compiles every one of those statements against the
DDL identity/migrations produces, on all three dialects, and sqlc-gen-unison
emits identity/internal/identitydb from them — typed params and methods over
driver placeholders — which is what the store executes. A column that does not
exist is a build failure with no database running, where it used to be a scan
error at runtime.

Committing the generated Go is not a choice: consumers compile this module from
the module cache, so the package the store executes has to be in the tree. The
.sql could in principle be rendered into a temp directory on each generation and
never committed, and it is committed anyway because of what would go with it.
It is the reviewable form of the contract — the generated Go embeds the same
statements, but in driver spelling, argument names erased into positional
markers, where the .sql's sqlc.arg(current_owner_user_id) is the spelling in
which a reviewer can see that a guard and its assignment are two arguments. It
anchors the drift gate — a test pins the committed text byte for byte against
the renderer, so "the SQL sqlc checks is the SQL the store runs" is a fact a
test states rather than a property of a pipeline taken on trust. And it is the
debugging seam when sqlc rejects a statement, because the file is exactly what
the analyzer was handed. The two cannot drift from each other: one is rendered
from code, the other is generated from the first, and the gates hold both ends.

The three reads that cross the membership junction come from the same place —
an account's roster, the accounts a user belongs to, and the user's own
membership list — because querygen renders a junction list as well as a
single-table one. The roster projects the member's columns beside the
membership's under a user_ prefix, so a page of thirty members is one query and
the row it comes back as is generated rather than paired to a Scan by eye. The
username prefix search is a rendered pair the same way: the page and the count
beside it, which is the one read here whose statement is not a filtered list.

The batched reads come from the same place, and they were the last reads that
did not. A page's rows point at users — "created by" — and a page of users,
memberships or invitations has roles hanging off it; read one key at a time
that is a round trip per row, and the loop converting rows is where that shape
arrives without anybody choosing it. Each is a read keyed on a bound set, which
querygen renders as a bound array on Postgres and a placeholder expansion on
the other two, under one Go signature. What each still owes its caller is the
empty batch, answered before the query rather than by it.

The two writes whose guards are not equalities came the same way once querygen
learned to say them: the two-factor verification, which stamps a proof only
where a secret exists and has not been proven, and the collision check behind
ErrUsernameTaken, which excludes the row being updated through an argument a
registration simply does not send. Both were hand-built for exactly as long as a
predicate meant "this column equals a bound value".

Nothing remains hand-written. The last six builders and the runtime binder they
rendered through are gone, and so is the projections file that paired their
SELECT lists with a list of scan targets by eye. Two of them were shapes the
port owed a generator and now has: the default-flag clear, whose predicate
excludes the membership being set rather than matching one, is one static
statement over an argument that may be absent — the same COALESCE the collision
check uses — and the agreements a registration accepts are a statement per
document run inside a transaction, in place of a SET list assembled from
however many documents were named. The other four were membership statements
addressed by the (user, account) pair, and each is now the ordinary form of
what it always was: an update assigning the default flag, three soft deletes
naming one membership, a user's, or an account's, and a keyed read in place of
the count that only ever asked whether there was one. The archivals no longer
clear the default flag themselves — a soft delete stamps archived_at from the
server's clock and nothing else — so the clear is the statement before them,
which is one more round trip and one fewer clock.

What follows from that is the store's own shape: it holds a table prefix and a
generated querier, and it does not know the dialect it is running against. It
calls no ExecContext and no QueryRowContext, so there is no write left whose row
count a driver could decline to report — the tolerance that case needed went
with the statements that needed it. What the store still counts is the writes
that matched no row, which is where "not there", "another directory's" and
"lost the race" become one sentinel.

# A note on timestamps, because one dialect does something surprising

Every time this package binds is a UTC time.Time, and every comparison is
against another such value. Postgres and MySQL store these as real temporal
types and compare them as such. SQLite does not: modernc's driver stores a
bound time.Time as Go's own String() rendering — "2026-07-30 12:00:00 +0000
UTC" — so expires_at <= ? there is a string comparison.

That is still correct, because the rendering begins with a fixed-width
"YYYY-MM-DD HH:MM:SS" prefix and everything is UTC, so lexical order is
chronological order. It stops being correct the moment a value is bound in a
non-UTC location, so do not remove the .UTC() calls at the binding sites.

The stamps the statements write are not among them. created_at, last_updated_at
and archived_at come from the server's own CURRENT_TIMESTAMP, which on SQLite is
the shape that column's comparisons are lexicographic over rather than one that
happens to sort right — see identity/migrations.

# Why there are no handlers here

The bargain above — you keep your policy, your HTTP handlers and your proto —
is not this package's alone. It is where the module draws the line
between what it stores and what it serves, and the module README states it once,
under "Transports", along with the few components on the other side of it and
the reason each is there.
*/
package identity

//go:generate go run ./internal/queriesgen
