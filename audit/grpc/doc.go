/*
Package grpc is the audit log on the wire: the gRPC service over
[github.com/primandproper/platform-go/v14/audit]'s Reader, the converters
between the generated messages and its types, a typed client, and the default
permission fragment a consumer composes into its policy.

It is imported as auditgrpc.

# The shape, and what it deliberately is not

Three RPCs, all reads: GetEntry, ListEntries, VerifyChain. Each converts, calls
one thing on the reader, and converts back. There is no orchestration and
nothing to orchestrate: nothing here opens a transaction, and the
database.Client the server takes is read for Reader() and for nothing else —
an audit read runs on the executor its caller supplies, and this surface's
caller is a connection with no transaction of its own to join.

What ships is strictly narrower than the interface beneath it, in two places,
and each is the reason this is a surface rather than a re-export.

audit.Recorder is absent. The reason is on the interface itself and it is the
sharpest instance of the rule the whole lane is made by: a write whose caller is
already inside the process's own transaction is not an RPC. An audit entry that
can commit while the change it describes rolls back — or the reverse — is not a
record of what happened, and no amount of retrying fixes it after the fact. See
identity/grpc, where that rule is stated once for every surface that follows it.

The scope is not settable. Both of the reads that may decline to narrow take a
*tenancy.Scope in which nil means every tenant's events, and those fields' own
comments say getting that backwards is a cross-tenant disclosure rather than a
wrong answer. Held in a process the unnarrowed read is a capability an operator
built deliberately; in a request field it would be one any caller has. So the
scope binds off the connection through a [ScopeResolver] —
authentication/signin/grpc is the precedent — and the schema reserves the field
name in every request message, which makes the absence something protoc enforces
rather than something a reviewer has to notice.

Nothing here compares a scope back after an unconfined read. This package used
to: GetEntry read by id alone, because audit.Reader.Get took no scope, and then
tested the entry's own against the connection's. Get takes the scope now, so an
entry belonging to somebody else is not read at all — and it is still answered
identically to an id that never existed, since telling the two apart would make
this an oracle for which entry ids exist in another tenant's log. The answer
moved into the method, which is what makes it true for every caller of Get
rather than for this one surface.

# Why the reading is worth crossing at all

Verify is. Reading entries back is something a consumer can approximate over
their own log; establishing that nobody edited, removed or reordered one is
not — it needs the chain, the anchor and retention's watermark, which is the
capability a hand-written log reader never gets around to. It is also the call
most worth making remotely and on a schedule, which is why
[PermissionVerifyChain] is its own grant: a verification carries no entry's
content, so a monitor that pages somebody when a chain breaks need not also be a
reader of what everybody did.

Because it is the scheduled call, it is also the one whose cost the server
bounds. Both ends of a VerifyChainRequest's window are optional, so a request
naming neither asks for a scope's whole history; the reader walks it in pages
and stops at its configured verification ceiling, reporting last_seq and
complete. A client that wants the rest sends the same window again with
after_seq set to that last_seq, and the server checks the link across the seam
like any other — see audit.SQLReader.Verify.

# What a deployment with per-actor chains cannot read here

The scope is the hash chain's partition as well as the row's label, and this
surface binds it to the connection. So the entries this service returns are the
entries of the chain the caller's principal names, and no request can ask for
another.

That is the whole of the tenancy guarantee and it is also a real limit, worth
knowing before it is discovered through empty pages. A deployment that files
some events under a per-actor scope — logins, sign-ups and password resets are
the usual reason, since a chain is a serialization point and putting every
login on one makes it the busiest row in the database — has put those entries
in chains no connection resolves to. They are not missing and not unreadable:
they are simply not this surface's to return, because the caller whose scope
would reach them is the actor, and the caller asking is an operator.

The answer for those is the reading that does not go through a connection's
scope: audit/privacy's collector, which is handed the subject a request names
and reads the repository directly, and is what a subject access request already
fans out over. A deployment wanting an operator-facing read of another actor's
chain builds it over audit.Reader in their own process, where the scope is an
argument rather than a property of who is calling.

What this service will not grow is a scope field on the request. The chain
partition being unnameable from the wire is what makes "no caller can read
another tenant's log" a property of the schema rather than of a check somebody
has to keep passing — see the reserved names in audit.proto, and the same
ruling in every other surface here.

# Errors

A method here hands the reader's error back with a default code and does not
switch on sentinels. What a missing entry means on the wire is decided once, by
audit.GRPCMapper, and a switch here would be a second copy of that decision free
to drift from it.

Every failure is one call, and there is deliberately no local helper wrapping it:

	grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "reading audit entry %q", id)

It logs, traces, maps the code and hands back an error that is still the
sentinel the reader returned, so the encoding interceptor has a chain to encode.
The code passed is the default for an error no mapper claims.

A broken chain is not among them. It is a finding rather than a failure to
answer, so VerifyChain reports it as an ordinary response carrying first_break —
audit.ErrChainBroken exists for a caller escalating one and is deliberately not
what the RPC returns.

That mapper reaches a client only once it is registered, which is
errormappers.Register — one call, made by service.Register for a service built
from a service.Config and by a hand-assembled service itself. This constructor
deliberately does not make it: a mapper that installs itself by a component
being constructed is a process-wide side effect a consumer cannot opt out of.
Without it, an entry that is not there arrives as codes.Unknown.

# Mounting it

	reader, err := audit.NewReader(client.Dialect(), audit.WithReaderLogger(logger))

	srv, err := auditgrpc.NewServer(reader, client,        // the client is read for Reader()
		auditgrpc.WithScopeResolver(scopeFromConnection),   // required: no default
		auditgrpc.WithPillars(pillars))

	reqs, err := auditgrpc.Require(authzgrpc.NewRequirements()).Build()

	errormappers.Register()   // or service.Register, which makes this call

	grpcServer, err := grpcserver.NewGRPCServer(ctx, cfg,
		[]grpc.UnaryServerInterceptor{
			authn,                                  // yours: whatever the resolver reads
			enforcer.UnaryServerInterceptor(),      // authorization/grpc, over the requirements above
			grpcerrors.UnaryErrorEncodingInterceptor(),
		},
		nil,
		[]grpcserver.RegistrationFunc{srv.RegisterOn},
	)

The last of those interceptors is the one to read about before mounting this
somewhere a browser or a mobile app can reach. grpcerrors.UnaryErrorEncodingInterceptor
puts the whole wrapped error into the status details so a peer can reconstruct
it, which is what makes a sentinel survive the wire — and what the status
*message* deliberately withholds, since an error's text can name tables,
connection strings and the permission that was missing. The two channels are not
protecting the same thing. A deployment serving untrusted clients strips the
detail at the edge; see that function's own documentation for the wording of
that obligation.

There is no config subpackage entry for this: audit/config builds the recorder,
the reader and the retention policy, and a server is three lines over what it
already returns.
*/
package grpc

//platform:transport resource surface: reading the audit log and verifying its chain — over `audit.Reader`
