/*
Package issuereports is the report queue's promises, assertable against any
subject that mounts it.

Ten RPCs, and two audiences for them: the person who filed a report, and
whoever triages the queue. What a consumer needs verified about each is
different, so the assertions say which one they are about.

The filer's promise is authorship. A report is filed in the caller's name
whatever the request says, and it is readable by that person — and, from here,
by nobody the deployment's rule does not admit, answered as an absence rather
than a refusal, because the read has already happened by the time the rule is
asked and a refusal would confirm the identifier names a report. The one read
that names a person asks the rule before it reads anything, so there the
refusal is PermissionDenied, and it is the same for a person who has filed
nothing.

The triager's promise is the queue: that it is the caller's tenant's, by
identifier, by listing and in both directions, and that a move is a
compare-and-set. A report moves only from the status the mover last saw, so the
second of two people deciding from the same read is refused rather than
overwriting the first, and a revision never touches where a report stands.

# What is here and what stayed behind

issuereports/grpc keeps its construction and contract tests: what NewServer
refuses to be built from, the permission roster, the reservations in the proto,
the store-method roster, the converters and the options. It keeps every test
that builds the server some particular way — a triage rule that lets one
caller read everybody's reports, an authorizer that cannot decide, a grants
extractor that holds or lacks the archive grant — because a deployed service
was built once and cannot be rebuilt by the thing testing it. It keeps the
principal with no user identifier, which a subject cannot mint.

That includes most of what include_archived answers. Whether an ordinary caller
receives the reports taken out of the queue is the deployment's grants' answer,
so a suite cannot know which one to expect; what it asserts for one is the half
that holds under every answer — asking is never refused, and a read that did not
ask never receives them. An administrator is the caller whose answer is known,
because whatever grant a deployment reads the archive off is one an
administrator holds, so every listing is also asserted to hand an administrator
the archive it asked for. A subject that mints no administrator skips that half.

# Both directions, deliberately

Each confinement assertion proves the caller reaches its own report through
the same client before proving it cannot reach a neighbor's. Without that, a
deployment whose scoping is comprehensively broken passes every "the
neighbor's report is absent" assertion on the strength of reaching nothing at
all.

# The subject's rule, and why these assertions can hold under any other

Who may read a report is the deployment's ReportAuthorizer's answer. The
assertions assume only the narrowest thing any rule must say: a person may read
what they filed, and a colleague holding no triage role of their own may not.
A deployment whose rule is wider — every member of a tenant a triager — is one
the colleague assertions were not written for, and the subject says so by
minting callers who hold no such role.
*/
package issuereports
