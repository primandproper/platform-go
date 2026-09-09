/*
Package grpc is the audit log on the wire: the gRPC service over
[github.com/primandproper/platform-go/v14/audit]'s Reader, the converters
between the generated messages and its types, a typed client, and the default
permission fragment a consumer composes into its policy.

It is imported as auditgrpc.

# The shape, and what it deliberately is not

Three RPCs, all reads: GetEntry, ListEntries, VerifyChain. Each converts, calls
one thing on the reader, and converts back. There is no orchestration and
nothing to orchestrate — the reader owns its own handle and runs against the
read replica, so this package holds no database.Client, which is a dependency
shape no other domain in the transport lane has.

What ships is strictly narrower than the interface beneath it, in three places,
and each is the reason this is a surface rather than a re-export.

audit.Recorder is absent. The reason is on the interface itself and it is the
sharpest instance of the rule the whole lane is made by: a write whose caller is
already inside the process's own transaction is not an RPC. An audit entry that
can commit while the change it describes rolls back — or the reverse — is not a
record of what happened, and no amount of retrying fixes it after the fact. See
identity/grpc, where that rule is stated once for every surface that follows it.

audit.Query.Scope is not settable. In the Go type it is a *string in which nil
means every tenant's events, and the field's own comment says getting that
backwards is a cross-tenant disclosure rather than a wrong answer. Held in a
process it is a capability an operator built deliberately; in a request field it
would be one any caller has. So the scope binds off the connection through a
[ScopeResolver] — authentication/signin/grpc is the precedent — and the schema
reserves the field name in every request message, which makes the absence
something protoc enforces rather than something a reviewer has to notice.

audit.Reader.Get takes an id and no scope, because in a process it is an
operator's read. Here the entry's own scope is compared against the connection's
and one belonging to somebody else is answered as absent, identically to an id
that never existed — telling the two apart would make this an oracle for which
entry ids exist in another tenant's log.

# Why the reading is worth crossing at all

Verify is. Reading entries back is something a consumer can approximate over
their own log; establishing that nobody edited, removed or reordered one is
not — it needs the chain, the anchor and retention's watermark, which is the
capability a hand-written log reader never gets around to. It is also the call
most worth making remotely and on a schedule, which is why
[PermissionVerifyChain] is its own grant: a verification carries no entry's
content, so a monitor that pages somebody when a chain breaks need not also be a
reader of what everybody did.

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

	reader, err := audit.NewReader(client, audit.WithReaderLogger(logger))

	srv, err := auditgrpc.NewServer(reader, scopeFromConnection,
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

There is no config subpackage entry for this: audit/config builds the recorder,
the reader and the retention policy, and a server is three lines over what it
already returns.
*/
package grpc

//platform:transport resource surface: reading the audit log and verifying its chain — over `audit.Reader`
