/*
Package audit is the audit surface's promises, assertable against any subject
that mounts it.

Three reads, and what a consumer needs verified about them is one sentence: the
chain a request reads is the one its session is in. Every assertion here is that
sentence from a different angle — by identifier, by listing, by a query that
names somebody else's actor — because the failure it guards against has no
symptom. A scope dropped from a predicate does not error; it widens, and the
widened answer looks exactly like a correct one until two tenants exist.

# What is here and what stayed behind

The surface's own suite varies how the server was built: seven of its tests
construct one WithChainsResolver and assert what changes. A deployed service was
built once and cannot be rebuilt by the thing testing it, so those assertions
are about construction rather than about behavior, and they stay in
audit/grpc where a test may choose its server. So do the converter round-trips,
the reserved-field check and the permission roster.

What moved is the half that holds however the server was built. Those are the
promises a consumer is owed, and until now the only place any of them was
asserted against an assembled service was a consumer's own repository.

# Seeding, and why it is required rather than worked around

There is no recording RPC. audit/grpc/client says why and means it: a recording
belongs inside the transaction of the change it describes, and a client is by
definition somewhere else. So every assertion here needs Seeds.AuditEntry, and
a subject that supplies none skips the whole suite.

The tempting alternative is to take some action over another surface and read
the entry it wrote. A consumer's suite does exactly that, and it is why their
cross-tenant test asserts against whatever entries happen to exist. It works
until a deployment records something else too, and then it is a count that
drifts. Seeding names the row, so the assertion is that this entry is here and
that one is not — true in a database the run owns and in one it shares.
*/
package audit
