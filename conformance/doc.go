/*
Package conformance is this module's promises, written once and assertable
against three different subjects.

A surface here is tested twice today and the two tests prove different things.
Each <pkg>/grpc package hand-builds its server, hands it a store and a test
extractor, and asserts what the handler decides; that is 425 assertions across
twelve surfaces and none of them has ever been through a composition root. A
consumer's integration suite boots a whole service and asserts the same things
again, in the consumer's repository, in the consumer's assertion library,
against the consumer's single dialect. Neither is wrong. What is wrong is that
they are two bodies of assertions about one set of promises, so the module can
break a promise and learn about it from somebody else's CI.

This package is the one body. An assertion is written against the generated
client interface — auditpb.AuditServiceClient and its eleven siblings — and the
subject it runs against is a seam:

	direct      the server wrapped to the client's shape, over SQLite.
	            Fast, no Docker, every make test.
	assembled   a service built by service.New, over containers, across the
	            dialect matrix. What the composition root actually composed.
	deployed    a consumer's running service over their own connection.
	            Whether their wiring honors what this module promised.

All three are a real client over a real connection. direct is not a lesser mode
and it is not an adapter around a server: it dials a bufconn, so it carries the
same interceptors, the same metadata and the same error encoding a consumer's
does, and the status code an assertion reads is the code a client reads rather
than the codes.Internal a handler hands over before a registered mapper has had
it. waitlists/grpc's own harness carries a comment warning that a suite which
skipped that registration would pin Internal as the answer to "we have stopped
taking signups" and pass.

What separates the modes is therefore only how the server was built and what is
underneath it — which is exactly the axis a consumer cannot vary and this module
cannot skip.

# What stays where it is

Construction, contract and conversion. NewServer(nil, db) has no wire form,
Require(*authzgrpc.RequirementsBuilder) is a statement about a server rather
than about a call, and a converter test is about two Go types. Those 110 tests
are correctly in process and none of them moves here. This package is the
behavioral half.

# What a subject supplies, and what it may decline

Seams is a struct of nilable fields, and absence is absence — the rule
service.Config states one level up. A nil client in Surfaces is a surface the
subject did not mount, and its suite skips rather than failing; a nil field in
Seeds is a row this subject cannot make, and the assertions that need one skip
with the reason named. Nothing here degrades quietly: a skip prints what was
missing, because a suite that silently asserted nothing is worse than no suite.

# Isolation, and why no assertion may count

direct gets a database of its own. deployed gets whatever the consumer is
running, beside every other test in their suite and possibly beside real
traffic. An assertion body that runs in both may therefore asserts the presence
or absence of rows it created and named, and never the number of rows that came
back. Every suite here mints a fresh tenant per test for the same reason. This
is not a style preference: a count assertion in a shared deployment is a test
whose outcome depends on what else is running, which is a flake that will be
read as a dialect bug.

# Dialects

A consumer runs one. This module supports three, and the weakest of them
decides how an assertion may be written: no sub-second timestamp comparison
(unison truncates SQLite times to seconds), no read-back that assumes RETURNING
(MySQL has none), no ordering relied upon without an explicit ORDER BY, and
nothing that needs a clause MariaDB does not have. An assertion that can only
hold on Postgres is an assertion that goes green on a consumer and red in this
module's own matrix, which is the wrong way round.

Dialect coverage is therefore this module's, through assembled mode. What a
consumer's run proves is their wiring, on their dialect — a different question,
and the one they cannot answer any other way.

# Running one

	conformance.Run(t, seams, conformanceaudit.Suite())

or every suite the subject mounted:

	conformanceall.Run(t, seams)

conformance/all is a package of its own so that linking every suite is a choice.
A consumer wiring only settings imports conformance/settings and links one
surface's protobuf bindings, rather than twelve.
*/
package conformance
