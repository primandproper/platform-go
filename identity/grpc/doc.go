/*
Package grpc is identity on the wire: the gRPC service over
[github.com/primandproper/platform-go/v14/identity]'s Service and Store, the
converters between the generated messages and its types, a typed client, and the
default permission fragment a consumer composes into its policy.

It is imported as identitygrpc.

# Why this module ships a transport at all

The module README's "Transports" section drew a line: a component that owns data
ships a store and stops, because a library that shipped /api/v1/users would be
versioning your API on its own cadence, in types your proto does not have, under
a scoping rule it guessed. This package is on the far side of that line, and it
is there because all three of those became false rather than because the line
was wrong.

The cadence is the domain tier's own, now that the primitives are leaving for
primitives-go — nothing else rides on a release this package is in. The types are
shipped: identity.proto is in this module, generated into identitypb here and
into a consumer's Swift, TypeScript and Kotlin from the same file, exactly as
filtering.proto already works. And the scope is not guessed, because
tenancy.Scope exists and this package binds it off the caller rather than off a
request field.

What stays the consumer's is what was always genuinely theirs, and each is a
seam here rather than a decision: who is calling ([Principal]), what each method
requires ([Permissions], declared in one call by [Require]), which rows a caller
may name ([TargetAuthorizer], which unlike the other three has a real default),
what else happens on a write (identity.Hooks, inside the transaction), and
whatever columns are their own.

# Authorization has two halves

[Permissions] is the first: a grant on the method, evaluated by
authorization/grpc's interceptor from the full method name and the caller's
grants, before the request body has been looked at. It answers whether this
caller may perform this kind of call at all.

[TargetAuthorizer] is the second, and it is here because it cannot be there.
Eleven RPCs take their target from the request — an account_id, a user_id, an
invitation_id — and asking whether the caller has any standing in that row means
reading it, which an interceptor holding the request and the grants has no handle
to do. So it is asked inside the handler, where the store already is, after the
request has been found well formed and before anything reads or writes.

The default, [MembershipAuthorizer], permits an account the caller holds a live
membership in, a user they share one with, and an invitation they sent or whose
account they are in. A consumer with a different rule supplies it with
[WithTargetAuthorizer]; a consumer who says nothing gets a directory that is
closed on other people's accounts rather than one where a grant is
directory-wide.

[github.com/primandproper/platform-go/v14/identity/config] assembles all three
layers from environment configuration and registers them with an injector, which
is the shorter of the two mounts below.

# The shape

Twenty-eight RPCs. Fifteen writes, each exactly one call into identity.Service,
which is one transaction with the consumer's hooks inside it. Thirteen reads on
identity.Store, on the client's reader — twelve of them one call, and the one
that lists what the caller has been sent reading the caller's row first for the
address it will not take from the request. No method here orchestrates
anything: it converts, calls one thing, and converts back. Anything that had to
happen atomically happened a layer down, where the transaction is.

# How a method fails

Every failure here is one call:

	grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "reading user %q", id)

which logs, traces, maps the code and returns an error that is still the sentinel
identity gave it — the chain intact for the encoding interceptor, the status
alongside it. There is deliberately no local helper wrapping that call: the two
things it used to add, the mapped code and the client-safe message, are what the
function itself does now, and a private name for a shared behavior is how the
two drift.

The code passed is a default, not an answer. The interceptor re-runs MapToGRPC
over the preserved chain, so identity.GRPCMapper wins over whatever a method
guessed, and that is why every read here passes codes.Internal and none switches
on a sentinel. The message is the description, unless a registered client-safe
sentinel has better words — which matters most where the codes collide, since a
taken username and a taken email address are both codes.AlreadyExists and a
client that cannot read the encoded details has only the message to tell them
apart.

# Mounting it

	srv, err := identitycfg.NewServer(ctx, cfg, svc, store, client, principalFromContext,
		identitycfg.WithPillars(pillars))

	reqs, err := identitygrpc.Require(authzgrpc.NewRequirements()).Build()

	errormappers.Register()   // or service.Register, which makes this call

	grpcServer, err := grpcserver.NewGRPCServer(ctx, cfg,
		[]grpc.UnaryServerInterceptor{
			authn,                                  // yours: puts the Principal on the context
			enforcer.UnaryServerInterceptor(),      // authorization/grpc, over the requirements above
			grpcerrors.UnaryErrorEncodingInterceptor(),
		},
		nil,
		[]grpcserver.RegistrationFunc{srv.RegisterOn},
	)

The errormappers.Register call is not optional and is not made here. Without it
every sentinel this service returns arrives as codes.Unknown — a taken username
included — because the mapping lives beside the sentinels in identity and
nothing installs itself into a process-wide registry by being linked in. See
[github.com/primandproper/platform-go/v14/errormappers].

# What is absent

No credential RPCs, no password on a registration, no avatar, and no scope on
any message. Each absence is deliberate and the reason is in identity.proto's
own documentation, which is the file a consumer generating a client in another
language actually reads.

# The pattern a fourth surface follows

This package, authentication/signin/grpc and authentication/oauth2clients/grpc
are three worked examples and were, for a while, the only statement of what a
domain transport here looks like. The domains still to cross should not have to
recover it by reading all three, so what the three have in common is written
down here and cited from there — audit/grpc is the first to have done so.

A write whose caller is already inside the process's own transaction is not an
RPC. billing's four status moves, settings.DeleteValuesForSubject,
issuereports.DeleteReportsByReporter, webhooks.Enqueue,
notifications.CreateNotification and audit.Record are the instances, and
audit.Recorder states the reason best: an audit entry that can commit while the
change it describes rolls back — or the reverse — is not a record of what
happened, and no amount of retrying fixes it after the fact. webhooks.Enqueue is
the same fact about a delivery, which it writes with one dispatch per endpoint
in the caller's transaction so that both commit with whatever else that
transaction did. An RPC moves such a write out of the transaction that was the
entire point of it.

Machinery has three shapes rather than one, and the test for all three is who
the realistic caller is. The transactional companion above is the first. The
second is the internal fan-out: notifications.ListDevicesByPrincipals and
webhooks.EndpointsForEvent are a component asking itself a question on the way
to its own work. The third is the provider callback hook —
notifications.InvalidateDeviceToken is reached when a push provider reports a
dead token, and is wired as mobile.WithTokenInvalidator rather than called by
anyone.

A consumer's catalog stays an opaque string in the proto and never becomes a
generated enum. comments.TargetType is a named string type and issuereports'
Report.Kind and Report.SubjectType are plain string fields, but all three are
the application's vocabulary rather than the package's, and both packages say
so — issuereports: what varies is the catalog of categories and what a report
can be about, and both of those are opaque to this package. A generated enum
would put that vocabulary on this module's release cadence, which is the first
of the three objections the README's Transports section says the split
answered. A closed set the package itself defines is the opposite case and is an
enum: settings.Kind is one.

Scope binds off the connection and never off a request field.
authentication/signin/grpc is the precedent, and audit was the sharp case: its
Query.Scope is a *string in which nil means every tenant's events, and the
field's own comment says getting it backwards is a cross-tenant disclosure
rather than a wrong answer. Held in process that is a capability an operator
built deliberately; put in a request field it is one any caller has. audit/grpc
has since crossed, and it took the rule one step further than a convention: its
schema reserves the name "scope" in every request message, so the field is one
protoc refuses rather than one a reviewer has to notice. A surface whose Go type
has a selector this dangerous should do the same.

Every one of the eleven now does, this file's own schema included. It was the
last of the three to, along with signin.proto and oauth2clients.proto, and the
three were the ones a consumer forks first — so the rule stated here was being
stated by the file least able to point at itself. Reserving a name no field uses
changes no descriptor a client depends on, so the crossing was not a wire break
and could not become one. What it bought is that the schema test the lane
promises is now eleven of eleven, and each package's grpc/ asserts it off
MessageDescriptor.ReservedNames rather than off a comment. The reservation
covers every request message, the inputs a request is built from, and the
messages a response is built from; the response wrappers hold nothing but those
and reserve nothing.

A field's JSON name is the name the Go type it renders beside already uses, which
for an id field means pinning it. protobuf derives resourceId from resource_id
and the Go types here tag that column `json:"resourceID"`, so a consumer serving
one row over its own handlers and over gRPC-JSON — by grpc-gateway or protojson
— emits two spellings of one field, and the client has to know which door it
came through to know which one it got. It is the only place the two derivations
disagree: resource_type is resourceType on both sides.

Two of the eleven schemas pinned the overrides and nine pinned none, and the two
were exactly the two whose grpc/ packages carry a conformance test matching Go
tags against descriptors — which is to say the pinning was a side effect of
being checked rather than a decision anybody took per file. The check that
notices is not one a package can make about itself: the disagreement is between
a Go tag in one package and a descriptor in another. internal/protoconvention is
where all eleven are checked at once, as an equality rather than as "an id field
carries some override", so a wrong spelling and a gratuitous one fail alongside
a missing one. It had a deadline the reservation above did not: adding a
json_name to a field that has shipped changes the wire spelling for every
transcoding client, in every language a consumer generates into, and there is no
Go major version that renames anything on the wire.

A surface owes a mapper pair beside its sentinels, an entry in
errormappers.Register, and rows in internal/sentinelmatrix, which reds until
every exported Err in the package is recorded as mapped, platform or unhandled.
Of the ten, dataprivacy had the pair before the lane opened and audit grew one
crossing; the eight still to cross owe theirs. Refusals whose
wording is meant for the person reading them go to
grpcerrors.RegisterClientSafeSentinels as well, or gRPC sends the code's name in
place of the sentence.

Two failures have already been paid for once here and should not be
rediscovered. The first is that a grant on the method is not the whole answer:
it says whether this kind of call is allowed at all, and which rows the caller
may name is a second question — the one [TargetAuthorizer] above exists to ask,
and a surface whose RPCs take their target from the request owes an equivalent.
The second is that an ownership check standing in front of a write is not a
check, because it reads through one connection what the write will act on
through another. The owner belongs in the statement — a write keyed on the id,
the scope and the owner together — rather than in a guard ahead of it.
*/
package grpc

//platform:transport resource surface: the four nouns and their lifecycle — over `identity.Service` and `identity.Store`
