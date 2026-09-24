# The conformance suite

What this module promises about its own surfaces, written once as shipped
library code, and assertable against a server this module did not build.

It lives here for the reason `client-contract.md` does: it describes a thing
consumers depend on, and a change to it should land in the pull request that
makes the change rather than be discovered afterwards.

**Status:** in progress. Five suites and all three subjects exist; the assembled
subject mounts all twelve gRPC surfaces over all three dialects and the three
HTTP surfaces wherever they can run, and the per-surface suites cover two of
them. [What is left](#what-is-left) is the honest list, and nothing below
describes something that has not been written.

## The problem it exists to solve

Every gRPC surface here is tested twice, and the two tests prove different
things.

Each `<pkg>/grpc` package hand-builds a server, hands it a store and a test
extractor, and asserts what the handler decides. That is 425 assertions across
twelve surfaces, and none of them has been through a composition root.

A consumer's integration suite boots a whole service and asserts the same
promises again — in their repository, in their assertion library, against their
one dialect. `dinnerdonebetter`'s `backend/testing/integration/apiserver` is the
worked example and carried roughly 120 such tests.

Neither is wrong. What is wrong is that they are two bodies of assertions about
one set of promises, so this module can break a promise and learn about it from
somebody else's CI. That is backwards ownership, and it is the whole reason this
package exists.

The other half is equally real and pulls the other way: a consumer needs to know
their *own* wiring honours what this module promised — that their extractor
reaches every surface, that they called `errormappers.Register()`, that their
`callers.PrincipalExtractor` resolves the scope they think it does. This module
cannot test that. Only their running service can.

So the answer is not to move the tests. It is to ship them.

## The three subjects

An assertion is written against the generated client interface and the subject
it runs against is a seam.

| subject | server | database | where it runs |
| --- | --- | --- | --- |
| **direct** | hand-built, in the suite's own harness | SQLite | every pull request, seconds, no Docker |
| **assembled** | built by `service.New` | SQLite, Postgres, MySQL 8 | every pull request |
| **deployed** | the consumer's | theirs | their integration suite |

All three are a real client over a real connection. Every harness serves on a
loopback TCP listener and dials its address; there is no in-memory transport and
no adapter around a server, because a test-only transport is one more thing
standing between an assertion and the claim it makes. Measuring the alternative
showed it buying nothing.

What separates the subjects is therefore only how the server was built and what
is underneath it — which is exactly the axis a consumer cannot vary and this
module cannot skip.

## What is settled

These were decided in the course of building it, and each cost something to
learn.

**A confinement assertion needs a positive control.** "The neighbour's row is
absent" is also true of a read that reaches nothing at all, so a deployment
whose scoping is comprehensively broken passes it on the strength of being
broken. Every confinement assertion here proves the caller reaches its *own* row
through the same client first. This is not a style preference: a deliberately
broken scope resolver passed two of the audit suite's five assertions before the
controls went in, and a mutation test is what found it. Mutate every new
confinement assertion before believing it.

**No assertion may count.** `direct` gets a database of its own; `deployed` gets
whatever the consumer is running, beside their other tests and possibly beside
real traffic. Assertions name the rows they created and check presence or
absence. A count assertion in a shared deployment is a test whose outcome
depends on what else is running, which will be read as a dialect bug.

**Assertions are written to the weakest dialect in the matrix.** A consumer runs
one; this module supports three. No sub-second timestamp comparison, no
read-back assuming `RETURNING`, no ordering relied on without an explicit
`ORDER BY`, nothing needing a clause MySQL 8 lacks. An assertion that can only
hold on Postgres goes green on a consumer and red in this module's own matrix.

**A seam describes an action, not a row.** `Actions.Auditable` asks a deployment
to do something it audits and report what the entry will name;
`Actions.Credentialed` asks it to store a secret its own way and report a
fragment that must never be rendered. The first draft handed over a row for the
suite to write, which is a backdoor: it puts the suite in the business of
manufacturing state and asserts against rows no deployment produces. It is also
the only shape that works — **no gRPC surface in this module records an audit
entry**, so "call the surface and read the log afterwards" works in a consumer's
deployment and writes nothing here.

**Absence is a skip, and the skip says why.** A nil client in `Surfaces` is a
surface the subject did not mount; a nil field in `Actions` is a state it cannot
bring about. Both skip with the reason printed. That is `service.Config`'s
presence-is-the-switch rule one level down. Nothing degrades quietly: a suite
that silently asserted nothing is worse than no suite.

**Construction stays in `<pkg>/grpc`.** `NewServer(nil, db)` has no wire form,
a permission roster is a statement about a server rather than a call, and a
converter test is about two Go types. So is anything that varies how the server
was built — all seven of `audit/grpc`'s `WithChainsResolver` tests, and
`identity/grpc`'s authorizer suite. A deployed service was built once and cannot
be rebuilt by the thing testing it.

**The suite is exported surface, but only `Run`, `Seams` and `Suite` are the
contract.** Assertions may be added between versions. One that reds a consumer's
CI has found a real wiring bug, which is what they installed it for. The only
edge worth a note is a suite pointed at a *remotely deployed* service built from
an older tag; an assertion needing a server floor says so and skips below it,
the way `client-contract.md`'s R10 and R11 do.

**Everything lands in this repository.** Conversions and the deletion of the
in-process tests they supersede go together, here. The consumer's suite is
refactored separately and later, by whoever owns it.

## Dialects, and who proves what

Dialect coverage is this module's, through the assembled subject fanning across
the matrix. What a consumer's run proves is their wiring, on their dialect — a
different question, and the one they cannot answer any other way.

Either real server may be one somebody else provided:

```
CONFORMANCE_POSTGRES_DSN    unset -> a container starts,  set -> that server
CONFORMANCE_MYSQL_DSN       unset -> a container starts,  set -> that server
```

Unset, a developer needs nothing but Docker. Set, the identical assertions run
against whatever is on the other end — a CI-provided database, or a local server
on a day when the container runtime is unwell. That is what keeps "all three
dialects, locally and in CI" one suite rather than two, and it is why the MySQL
half waited on primitives-go v2.7.0 rather than being written twice.

## CI

The suites run in a workflow of their own, `.github/workflows/conformance.yaml`,
on every pull request touching Go.

Containers are on: the real-server half of every subject runs there, from the
runner's Docker daemon. A job that already has databases sets the two
`CONFORMANCE_*_DSN` variables instead and starts nothing.

They are **not** in the coverage gate, and that is about the number rather than
about the tests. `go test` credits coverage to whichever harness executed the
code, so `conformance/anonymous` reported 0.9% while asserting against all
thirty-one of identity's RPCs on every run, and the two leaf suites that happen
to sit beside a harness reported ~100%. Both figures are the same accident
pointing in opposite directions. `codecov.yml` and `.scripts/coverage.sh` carry
the long form; excluding them from a number is not excluding them from CI, and
all three files say so and point at each other.

## What exists

| suite | assertions | notes |
| --- | --- | --- |
| `conformance/anonymous` | 147 | every RPC on all twelve gRPC surfaces and every route on the three HTTP ones |
| `conformance/filters` | 39 | every paged read refuses a malformed filter, behind a positive control |
| `conformance/pagination` | 156 | every paged read reports the filter it applied |
| `conformance/identity` | 49 | accounts, memberships, invitations, users |
| `conformance/settings` | 28 | definitions, values, reserved settings, confinement |
| `conformance/waitlists` | 39 | both audiences: the console, and the public signup page |
| `conformance/billing` | 31 | products, subscriptions, the account rule |
| `conformance/issuereports` | 43 | filing, lifecycle, the triage queue |
| `conformance/signin` | 31 | registration, the password and magic-link doors, refresh, sign-out |
| `conformance/webhooks` | 28 | event types, endpoints, signing keys, subscriptions |
| `conformance/comments` | 26 | writing, reading, authorship |
| `conformance/notifications` | 19 | the inbox and devices |
| `conformance/audit` | 12 | reads, confinement, paging, verification |
| `conformance/passwordreset` | 8 | the reset flow end to end |
| `conformance/oauth2clients` | 7 | the administered registry |
| `conformance/dataprivacy` | 5 | privacy requests over HTTP; one skipped on a known composition bug |

668 leaf assertions on the Postgres run of the assembled subject, the one that
serves every surface; SQLite and MySQL 8 run all but the HTTP surfaces that
need Postgres. Every one passes on all three, with ten skips each, every skip
printing its reason.

| subject | where | mounts |
| --- | --- | --- |
| direct | `conformance/audit`, `conformance/identity` | one surface each |
| assembled | `conformance/assembled` | all twelve gRPC surfaces on all three dialects; mediaregistry everywhere, dataprivacy and operations on Postgres |

**142 was what was enumerated, not what had run.** The anonymous suite reads all
twelve descriptors, but an RPC is only called on a surface the subject mounted,
and until the assembled subject existed no subject mounted anything but identity
— so identity's 31 were executed and the other 111 were compiled. Audit's three
ran for the first time through `service.New`, and all three failed; see below.
With every surface mounted all 142 run, on three dialects. The other ten
surfaces passed on first mounting: what they refuse without a caller was already
right, and the value of running them is that it now stays right.

The HTTP half adds ten routes — dataprivacy's five, mediaregistry's one and
operations' four — each refusing a request with nobody on it as 401. They are
listed rather than enumerated, since no registry holds an HTTP route, and the
list is checked against what each package's Mount actually returns. operations
needs Postgres (its queue claims with `SKIP LOCKED`), and dataprivacy's service
runs its requests as operations, so those two are asserted on Postgres only;
the README's matrix lists dataprivacy on all three dialects, which is true of its
store and not of its surface.

`conformance/filters` and `conformance/pagination` are the other two
cross-cutting suites, and both find their reads the way `anonymous` does: every
RPC whose request carries a `filtering.v1.QueryFilter`, 39 of them today. The
shared half is `conformance/internal/pagedrpc`, whose one hand-written part is a
request per read that needs more than a filter to be answerable — an account, a
comment target, a subject — enumerated rather than inferred, and checked against
the descriptors so a read with an unfilled field fails there rather than being
asserted against half-built.

*filters* asserts a malformed filter is refused as `InvalidArgument`: a sort
direction nobody recognizes, and a timestamp outside protobuf's range, the two
things `filtering/grpc` reports rather than corrects. Each read is first called
with a well-formed filter, and that call must not be `InvalidArgument` — without
that control, a read refused for a missing account would pass for the wrong
reason. All 39 pass on all three dialects, and a surface made to list despite the
error reds it.

*pagination* asserts a page reports the filter it applied rather than the one
it was sent: the default page size when none was asked for, a normalized sort
direction, the sent cursor echoed as `previous_cursor`, and a page size too large
for the wire's `uint16` clamped rather than wrapped. The last is phrased as a
comparison — asking for 65546 must be answered as asking for 65535 is — because
`MaxQueryFilterLimit` is a deployment's to raise and a suite that knew the
ceiling would be wrong on the deployments that did. A surface made to report the
request's filter as the applied one reds three of the four. Two reads skip,
with the reason printed: settings' `ListValuesForDefinition` answers an unknown
definition with NotFound, and identity's `ListInvitationsForEmailAddress`
answers `FailedPrecondition`, so neither has a page to read without state a
client should not be the one to create.

A surface the composition root stops mounting fails here rather than skipping:
the harness hands every suite a client for all twelve, and an unmounted one
answers `Unimplemented`. Dropping billing's config block reds all eighteen of
its RPCs.

`conformance/anonymous` is the shape that pays, and the reason to prefer
cross-cutting suites over per-surface ports where the promise allows it. It
enumerates each service's RPCs from its protobuf descriptor rather than naming
them, so an RPC added later is covered with nobody remembering to come back, and
it reads each surface's own declaration of which methods are deliberately open
— `waitlists.PublicMethods`, `signin.AnonymousMethods`,
`passwordreset.AnonymousMethods` — rather than carrying a list that could
disagree with them. Its roster is checked against `protoregistry.GlobalFiles`,
because a missing entry compiles perfectly and quietly asserts nothing about an
entire service.

It asserts both directions. The 127 RPCs that require a caller must refuse one
that has none; the 15 that do not must not be refused that way. The second
direction is the one nothing else checks and the one with a user-visible
failure: three of waitlists' public RPCs are a signup form, the link in the mail
that follows, and the unsubscribe in that mail.

Roughly 142 of the 150 assertions are dialect-independent; that ratio inverts as
per-surface work lands, which is almost entirely SQL-shaped.

## What it has already found

**platform-go#879** — concurrent registrations deadlocked on MySQL and MariaDB,
deterministically, and never on Postgres or SQLite. `replaceRoles` cleared an
owner's grants whether or not there was anything to clear, and on InnoDB a
`DELETE` matching no row still gap-locks the range it scanned. Fixed by asking
before clearing. Every existing identity test runs on SQLite, where it cannot
happen.

**primitives-go#26** — `pgtest` could be pointed at a server CI already provides
and its siblings could not, so the escape hatch covered one third of a
three-dialect matrix. Shipped as primitives-go v2.7.0.

**A MariaDB 1020, diagnosed** — `Error 1020 (HY000): Record has changed since
last read`, from `clearDefaultAccountsForUser` under concurrent registration. It
is MariaDB's REPEATABLE READ refusing an `UPDATE` whose current read disagrees
with the snapshot an earlier consistent read in the same transaction opened;
primitives-go#28 carries the reproduction. It is MariaDB's alone — the same
suites pass on MySQL 8, which is what the matrix now runs — and what is left of
it is #28's question of whether `WithTransaction` should retry what an engine
says to retry.

**A derived seam answered a missing caller as a bad request** — the first thing
the assembled subject found, and something only it could. Four surfaces never
see a principal: `service` derives the one fact each wants from the extractor.
With nobody on the request the derivation's error was unmapped, so audit
answered `InvalidArgument` and mediaregistry a 500, where the eight surfaces
reading a principal themselves say `Unauthenticated`. Every direct harness
hand-builds its resolver and never went through the derivation. Fixed:
`callers.ErrNoPrincipal`, mapped, and wrapped by `service.ErrNoPrincipal`. All
four now answer 401 or `Unauthenticated`, and the anonymous suite asserts it of
every one of their routes and RPCs.

**Two tenants' first audit entries deadlocked on MySQL** — found by the
assembled subject over MySQL 8, the first time audit had run against a real
server beside other writers. The recorder locked a scope's chain row and created
it only when the locked read missed, and a missed locked read takes an InnoDB
gap lock that another tenant's insert waits on. Fixed by creating before
locking. audit's own container suite had a test for exactly this race and could
not see it: its client config pinned every pool to one connection, so its
concurrent writers ran one at a time.

**Concurrent setting definitions deadlocked on MySQL** — #879's shape a third
time. settings cleared a definition's options with a DELETE before writing
them, whether or not there were any, and on InnoDB that gap-locks the range two
new definitions both insert into. Found by the settings suite on MySQL 8, where
parallel subtests create definitions; fixed the way identity was, by asking
before clearing.

**A privacy request's progress link 404s for the person who submitted it**
(platform-go#884, open). dataprivacy opens the fulfilling operation owned by the
person; `service` mounts operations/http resolving the owner as the caller's
tenant; the two never match. Only the assembled subject could see it, and its
assertion is skipped naming the bug until the ownership is ruled.

## What is left

1. **The per-surface tail is converted.** Every gRPC surface has a suite, and
   each suite's commit lists what converted, what was already superseded, and
   what stays in process with its reason — construction, rosters, converters,
   schema, options, observability, and anything that varies how a server was
   built. mediaregistry's HTTP surface has no per-surface suite yet: asserting
   it needs an object the application registered, which is a seam nobody has
   written.

2. **Deleting what the conversions supersede — blocked on a decision.** The
   ruling is that a conversion and the deletion of the in-process test it
   supersedes land together. The coverage gate cannot see that trade. It
   excludes `conformance/` and runs without `-coverpkg`, so a deletion removes
   coverage the gate counts while the conversion adds coverage it does not.
   Measured on identity: `identity/grpc` goes from 94.0% to 61.1% (271 of 822
   statements), which is 0.65% of the 41,416 statements Codecov counts —
   over its 0.5% threshold from one surface alone. All eleven together cost
   1,365 statements, 3.29%, and each deletion commit carries its per-package
   numbers. The deletions are therefore on `conformance-deletions`, stacked on
   this branch, until one of these is chosen:

   - **Credit conformance to what it executes.** Run the assembled subject in
     the coverage job with `-coverpkg` over the module, so a line an assertion
     reaches through `service.New` counts where it lives. codecov.yml names
     the missing `-coverpkg` as the reason its current numbers mislead; this
     is that fix, and it makes the gate measure the same thing the deletions
     assume it does.
   - **Accept the drop** once, with the threshold or a one-off override, and
     keep excluding the tree.
   - **Keep the in-process tests**, which abandons the ruling.

