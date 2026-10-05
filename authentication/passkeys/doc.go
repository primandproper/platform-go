/*
Package passkeys stores the credentials a WebAuthn registration produces, and
runs the ceremonies that produce and verify them.

A passkey login has three pieces of state and two of them already had a home. The
protocol — the challenge, the attestation, the assertion — is primitives-go's
authentication/webauthn. The ceremony in flight, the row one replica writes and
another reads a moment later, is authentication/webauthnsessions. What a finished
ceremony *produces* is a registered credential, and until this package that was
the application's to keep: primitives-go says so on the interface, because
assembling a webauthn.User means knowing what a user is, and only the directory
holding them can answer that.

So every consumer wrote this table, and the write that gets dropped is always the
same one.

# The sign count is the reason this exists

An authenticator keeps a counter and increments it each time it signs. The relying
party stores the last value it saw, and compares: a count that did not advance
means two authenticators are answering for one credential, which is a cloned key.
It is the only clone detection WebAuthn has.

A deployment that stores credentials but treats the counter write-back as
best-effort has a working passkey login and no clone detection, and nothing will
ever say so. That is why [Store.RecordUse] answers with an error the caller is
expected to surface rather than log: a login whose bookkeeping failed is a login
that did not happen.

# What the caller still owns

Resolving a WebAuthn user handle to one of this application's users. A
discoverable login arrives holding an opaque handle and nothing else, and the
directory that can answer is the consumer's — which is exactly why this could not
be a primitive, and why it is a domain package rather than one. [NewUserSource]
takes that answer as a function and assembles the rest.

# The ceremonies

[Service] runs them: registration, the named login and the discoverable one,
and the list and archive a settings page offers. It mints nothing — a finished
login answers with the credential that proved somebody and whether the
authenticator verified them, and issuing them a session is
authentication/signin's IssueForPrincipal — given its MultiFactor option only
when [Login.UserVerified] says so, since a passkey is two factors only then.
What it adds is the order the steps run in and four decisions every
hand-written copy got wrong.

An unknown username is answered rather than refused. [Service.BeginLogin]
hands a username nobody holds the same options it hands a known one, and no
named login lists the user's credentials, so the two answers cannot be told
apart; [Service.BeginRegistration] registers discoverable credentials only for
that reason.

Enrollment is gated. Adding a passkey is adding a way into an account, so a
registration asks an [EnrollmentGate] first and again at the write. There is no
default: [WithEnrollmentGate] is required, and a deployment that has decided a
live session is enough says so with [AdmitEveryEnrollment].

The last passkey stays. [Service.ArchiveCredential] refuses to leave a user with
no passkey and no other way in; [WithAlternativeSignIn] is how it learns about a
password, and [WithoutLastCredentialGuard] turns it off by name.

And the writes are recorded. [Hooks] run on each write's transaction — a
registration's, an archive's, and one a refused login opens for itself — and a
hook that refuses rolls its write back.

A deployment that owes each write an audit entry and a domain event installs
[RecordingHooks], built over a recording.Recorder, rather than writing one. A
registration and a revocation are credential events: recorded, published to the
outbox, and offered to no webhook subscriber, because [EventCatalog] is empty on
purpose. A refused login records nothing, for the reasons
[RecordingHooks.AfterFailedPasskeyLogin] gives.

The seams are the consumer's two answers about users, both functions so that
this package never imports identity: a [UserResolver] from a handle to a user,
and a [UsernameResolver] from what somebody typed to a handle.

A login's sign count commits before [Service.FinishLogin] returns, in a
transaction the service opens, so the caller minting a token afterwards cannot
roll it back. A count that advanced for a login the caller then failed to finish
is harmless: the authenticator had already advanced it. A registration writes
its credential in a transaction of its own too, after the challenge is spent:
the ceremony state may be a table in the same database, and a caller's
transaction held open around its consumption is a writer it waits on — see
[Service.FinishRegistration].

# The index that is the point

credential_id is unique among live rows only, on all three dialects. A plain
UNIQUE would mean a revoked passkey can never be enrolled again from the same
authenticator; no index at all would let a replayed registration write a second
row, after which two counters advance independently and clone detection reports on
whichever one the lookup happened to reach. Postgres and SQLite spell it as a
partial index; MySQL has none, so the predicate lives in a generated column the
unique key is declared over — see authentication/passkeys/migrations.

# Backup flags come off the ceremony, not off the row

go-webauthn refuses an assertion whose BackupEligible flag disagrees with the one
on the credential it is verifying, and a passkey synced through a platform
keychain reports flags that differ from whatever the registration saw. So the
flags are an argument to [Credential.WebAuthnCredential] and to [UserSource.User]
rather than a column: replaying stored ones fails logins that are perfectly valid.

# Privacy

A row here says a named person enrolled an authenticator, what they called it,
when, and whether they have taken it off their account. That is data held about
somebody, so this package is in a subject access request through
authentication/passkeys/privacy — a dataprivacy.Collector over
[Store.ListAllCredentialsForUser] and a dataprivacy.Eraser over
[Store.DeleteCredentialsForUser].

Both are methods the privacy pair needed and the ceremonies did not, and each
absence was the point. [Store.GetCredentialsForUser] excludes revoked passkeys,
which is right for a login and wrong for an export: nothing here ever removes an
archived row, so an export built on that read would be one whose completeness
depended on what the subject had got around to revoking. And
[Store.ArchiveCredentialForUser] keeps the row, because the row is what answers
"this person had an authenticator here and removed it on this date" — which is
exactly the sentence a forgotten subject has asked nobody to be able to write.

The export carries no secret and cannot. A passkey's private half is generated
inside the authenticator and never leaves it; what this table holds is the public
key an assertion is checked against and the credential ID every login sends in
the clear.

# The table is yours to create

authentication/passkeys/migrations renders the DDL for a dialect and prefix.
Nothing here creates a table on its own: a library that ran DDL against a
caller's database would be a library that decided when a deployment's schema
changed.

# Where the SQL comes from

Every statement this package executes is generated. The table's facts — its name,
its columns in projection order, and which of them a write assigns — are spelled
once, in internal/queries. `make generate` renders them through database/querygen
into the canonical .sql files beside that package, in sqlc's spelling;
`make sqlc_compile` checks every one of them against the DDL migrations produces,
on all three dialects, with no database running; sqlc-gen-unison emits
internal/passkeysdb from the same files, and that is what the store executes.

So a column renamed in the DDL is a failed generate rather than a scan error at
run time. What this package writes by hand is which statements it wants; it
writes no SQL.
*/
package passkeys

//go:generate go run ./internal/queriesgen
