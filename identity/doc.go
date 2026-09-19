/*
Package identity stores the thing being authenticated.

The platform ships every engine an application needs to authenticate somebody —
argon2 hashing, TOTP, WebAuthn, OAuth2 and token issuance in primitives-go,
session management and authorization tables here — and until this package
shipped nothing to authenticate.
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
[github.com/primandproper/primitives-go/v2/authentication/argon2] hashes and
compares, [github.com/primandproper/primitives-go/v2/authentication/totp]
generates and validates, [github.com/primandproper/primitives-go/v2/authentication/webauthn]
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
Store. The engines remain engines — this package never hashes a password, never
compares one, never generates a TOTP secret. It stores what they produce, and
[User.Redacted] is how a user reaches a response body without them.

The two link tokens this schema holds are the exception, and they are stored as
digests: the email-address verification token and the invitation token. Each is
a bearer credential — one proves an address, the other joins an account — so a
backup, a replica or a support engineer's query would hand out every outstanding
one of them if the column held the raw value, and the verification column is
indexed besides. The store hashes on the way in and on every lookup, so the
secret is an argument and never a column, and no read hands one back: what a
read carries is [User.EmailAddressVerificationTokenDigest] and
[Invitation.TokenDigest]. The one exception is
[InvitationStore.CreateInvitation], which answers with the token its caller
minted so that it can be mailed.

What is deliberately not here, and why it is not an omission: WebAuthn ceremony
state, password reset tokens, and sessions. Each is a set per user rather than a
column on one, each has a lifecycle of its own (a ceremony is begun and
answered, a reset token is issued and burned, a session expires), and each is
consumed by exactly one engine. Their home is beside that engine — the same rule
that put the password hash here, applied to a fact that is not a column.
Sessions live in [github.com/primandproper/platform-go/v14/sessions], WebAuthn
ceremony state in
[github.com/primandproper/platform-go/v14/authentication/webauthnsessions], and
password reset tokens in
[github.com/primandproper/platform-go/v14/authentication/passwordreset], which
also owns the two properties a consumer writing that table by hand gets wrong:
the token is stored as a digest, and single use is enforced by the store rather
than by whoever called it.

# Passwordless users

A user with no password is a supported user, not an unfinished one.

primitives-go ships a WebAuthn engine and an OAuth2 one, and a registration
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
[github.com/primandproper/primitives-go/v2/authentication/argon2], rather than
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

Every row here carries a [github.com/primandproper/primitives-go/v2/tenancy.Scope],
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

# What the directory owes a subject

This package holds the names, the addresses and the credentials, so it meets the
dataprivacy seam like any other store of personal data.
[github.com/primandproper/platform-go/v14/identity/privacy] ships the two halves:
a dataprivacy.Collector that returns who somebody is to the directory, and a
dataprivacy.Eraser that destroys them. It is a package of its own rather than two
methods here, so that a service with a login form and no privacy pipeline does
not compile the operations queue and the scheduler behind it.

Two things about the erasure are worth knowing before you wire one up. It is two
writes rather than one — [InvitationStore.EraseInvitationsForSubject] and then
[AdminWriter.EraseUser] — because identity_invitations references neither user it
names and so cascades from nothing, and the first reads the subject's address off
the row the second destroys. And neither of them can resolve an account the
subject owned: EraseUser is not refusable, so an owned account survives naming an
owner who no longer exists. Transfer or archive those before the request runs.

# Account status is enforced on every read, not only at the door

[AccountStatus] decides whether somebody may be here at all, and only StatusGood
says yes — [AccountStatus.AdmitsSignIn] is that rule, spelled once.

[Store.GetPrincipal] applies it. A user whose status admits no sign-in is refused
with [ErrSignInNotAdmitted] rather than answered with a [Principal], and since
that read is what every authenticated request resolves its caller through, a ban
is effective on the next request against every surface at once. The alternative
was to document an obligation — "check principal.User.AccountStatus in your
interceptor" — and an obligation nobody is compiled against is one line away from
being forgotten in each consumer, which is the state where an operator believes
they suspended somebody and the suspension is waiting for a token to expire.

Two things it does not do. It does not tell the three refusing statuses apart:
one sentinel goes out, because a caller already holding a credential has had the
remedy conversation and a client is owed one answer. The sign-in door is where
they are told apart — [github.com/primandproper/platform-go/v14/authentication/signin]
checks the status before it resolves a principal and answers with its own
ErrUserUnverified, ErrUserBanned or ErrUserTerminated, the second of those
carrying the explanation an operator wrote to be shown.

And it does not revoke anything. A suspended user's sessions and refresh-token
families are rows in other packages' tables, and reaching across for them is the
dependency [Hooks] exists to avoid — so
[Hooks.AfterUpdateUserAccountStatus] is where a consumer clears them, and its
documentation names the two calls. The refusal makes the ban effective; the hook
makes it tidy.

One consequence to wire for: [User.EnsureDefaults] leaves a new user
StatusUnverified, so a registration that never moves them to StatusGood is a
registration that resolves no principal. An application with no verification step
sets StatusGood on the [User] it passes to [Service.Register].

# The operations, and what a consumer still writes

A registration is three writes in one transaction:

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		registered, err := store.CreateUser(ctx, tx, scope, user)
		if err != nil {
			return err
		}
		account.OwnerUserID = registered.ID
		created, err := store.CreateAccount(ctx, tx, scope, account)
		if err != nil {
			return err
		}
		membership.BelongsToUser, membership.BelongsToAccount = registered.ID, created.ID
		_, err = store.CreateMembership(ctx, tx, scope, membership)
		return err
	})

Each write answers with the row it wrote and leaves the value it was handed
alone, so the ids the next write keys on come off what the last one returned
rather than off the struct that was passed to it.

A user without an account, or an account without an owner, is the failure mode
every application discovers in production rather than in a test, and the shape
above is what rules it out — which is why [Service] ships it rather than this
documentation showing it. [Service.Register] is that block, and its twenty-one
siblings are the rest of what the block-writing turned out to be: the
invitation lifecycle, an ownership transfer, a default-account switch, an
archival with its membership fan-out, the two administrative status changes,
the profile and account saves, an agreement, the two roster writes, and the
seven credential writes. Measured in the consumer this package was extracted from, the layer
those replace is a little over two thousand lines.

The credential seven are the group that needed an argument, because
[github.com/primandproper/platform-go/v14/authentication/signin] already writes
three of them and asks for the current password first. That is the right rule
for somebody changing their own credential and an impossible one for every flow
that has no current password to ask for: a reset answering a mailed link, an
operator forcing a change, a verification link that is itself the proof. Those
flows were reaching [CredentialStore] directly, and a store write is a place a
consumer's audit entry has nothing to commit with — so each of them is an
operation here too, hooked the same way, holding no more policy than the rest.
[Service.UpdateUserPassword] carries the long form.

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
[github.com/primandproper/primitives-go/v2/database.Tx] and every read takes the
wider [github.com/primandproper/primitives-go/v2/database.SQLQueryExecutor]. That
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

# Handles are folded, because one dialect was folding them for us

A username and an email address are stored, compared and looked up in one
spelling: lower case, folded in Go before anything is bound. A user who
registers as "Ada" signs in as "ADA", and the invitation sent to
Ada@example.com is the one ada@example.com is shown.

It is a fold rather than a collation because the collation is not the same
everywhere. MySQL renders these columns as VARCHAR under the server's default,
which compares case-insensitively; Postgres and SQLite compare TEXT byte for
byte. The same registration sequence therefore gave three different answers to
"is this handle taken" — Ada and ada were one user on MySQL and two on the
other two, and ErrUsernameTaken fired for inputs that depended on which server
a deployment happened to pick. Folding in Go is what makes the case half of the
collation stop mattering, including a server whose default collation is changed
underneath a running directory: what MySQL folds afterwards is already folded,
and what the other two compare byte for byte was folded before it was bound.

Case is not the whole of what a collation decides, and the rest is settled in
the schema rather than in Go. MariaDB 11 — which is the flavor this module's
MySQL suite runs against — defaults to utf8mb4_uca1400_ai_ci, which is accent
insensitive as well: against a stored "renee" it matches "renée", where Postgres
and SQLite do not. A Go fold cannot close that one and should not try. Stripping
accents would make renee and renée the same person on all three dialects, which
is a worse answer than the divergence — nobody's name is a spelling of somebody
else's — so the convergence goes the other way. username, email_address and
to_email are collated utf8mb4_bin on MySQL, which is the byte-exact comparison
the other two engines already do, leaving Go's fold as the only thing that
folds anything. The clause is in identity/migrations/mysql.sql, and the case
that fails without it is in the handle folding suite.

FoldHandle is that fold, and it is exported because it is not this package's
private business. Every write and every lookup here calls it, so does
authentication/signin — for the read it makes and for the handle it records a
failed attempt under, which is the key a lockout counter counts against — and
so should a consumer that reaches these columns through an index of their own.
A second copy of a normalisation is a copy that can disagree with the rows.

The spelling somebody submitted is not lost. A username has two columns: the
folded handle the directory is keyed on, in User.Username, and the spelling as
given beside it, in User.UsernameDisplay, which nothing looks up and nothing
is unique on. Show people the second and compare the first. A write that names
no display spelling adopts the username's, which is what a registration does,
and one that names a spelling of some other handle is refused — see
ErrUsernameDisplayMismatch.

An email address gets no such companion. Nobody renders the case of their own
address, and a second column is a second thing to keep in step.

What this costs a directory that already has rows: they were written in
whatever case was submitted, so fold the username and email_address columns in
the same migration that adds username_display, or a lookup will not find the
rows that were not already lower case. Backfilling the display column itself is
optional — a row with none reads its folded handle back in UsernameDisplay, so
a page rendering that field never renders a blank. On MySQL the same migration
carries the collation across, with an ALTER per column; it rebuilds the unique
index, and it cannot newly conflict, because utf8mb4_bin makes rows more
distinct than the default did rather than less.

# Why there are no handlers here

The bargain above — you keep your policy, your HTTP handlers and your proto —
is not this package's alone. It is where the module draws the line
between what it stores and what it serves, and the module README states it once,
under "Transports", along with the few components on the other side of it and
the reason each is there.
*/
package identity

//go:generate go run ./internal/queriesgen
