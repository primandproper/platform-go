/*
Package grpc is waitlists on the wire: the gRPC service over
[github.com/primandproper/platform-go/v14/waitlists]'s Store, the converters
between the generated messages and its types, a typed client, and the default
permission fragment a consumer composes into its policy.

It is imported as waitlistsgrpc.

	srv, _ := waitlistsgrpc.NewServer(store, db, extractPrincipal, withdrawals)
	// []grpcserver.RegistrationFunc{srv.RegisterOn}

It is the fourth domain surface in this module and it follows the pattern
identity/grpc's documentation states rather than restating it: who is calling is
an interface a consumer's own authentication interceptor satisfies, what each
method requires is a default map a consumer composes into their own, the scope
never comes off a request field, and a write whose caller is already inside the
process's own transaction is not an RPC. What is below is where waitlists lands
on each and where it diverges.

# Who is calling

Who is calling is [github.com/primandproper/platform-go/v14/callers.Principal],
which a consumer's own authentication interceptor puts on the context and
[github.com/primandproper/platform-go/v14/callers.PrincipalExtractor] reads
back. Those are one package for the whole module rather than an interface per
surface, because a deployment has one authentication interceptor and one notion
of a caller, and that package's documentation is where the ruling that keeps the
method set at three lives.

Unlike every other surface in this module, an extractor here reports "nobody" on
requests that are working exactly as intended: three of this service's RPCs are a
signup page, and the person on it has not signed in. [NewServer] and the two
sections below are where that lands.

# Seventeen RPCs and no absences

Every method of waitlists.Store is here. That is unusual on this lane — the
other nine domains each carve something out — and it is why this one went first.
The carve-outs elsewhere are all one test applied to different machinery: is the
realistic caller a worker on a timer, a processor callback, or the consumer's
own code inside its own transaction? A waitlist has no queue protocol, no
fan-out and no provider callback. Every one of the seventeen is a form somebody
submitted or a console somebody is looking at, and roster_test.go is where a
store method added later has to be classified rather than reflexively published.

The nearest thing to a carve-out is WithdrawSignupsForSubject, which is the
erasure path waitlists/privacy builds a dataprivacy.Eraser on. It is on the wire,
and comments, issuereports and settings each keep their equivalent off it, so the
reason this one crosses has to be the thing that is not true of them. It is not
the realistic caller: an operator honoring a request out of band is the realistic
caller of all four. It is that this erasure is re-runnable and theirs are not.

Theirs is a hard delete of everything matching, and it exists to commit inside
the transaction that removes the rest of the person. An RPC moves it into a
transaction of its own, at a moment the caller does not choose, so what a
half-done run leaves is a subject gone from one table and present in the others
— and an RPC over it is a second route into the same removal, which whoever
re-drove that run would take instead of the one the erasure walks. This one has
no such divergence to publish. It blanks the subject reference it matched on, so
a second call naming the same subject finds nothing the first left, restamps
nothing and answers zero: the two routes converge on one row state, and the
later arrival does nothing at all. waitlists' own withdrawal_test.go pins that,
because the argument for this RPC is that property rather than a sentence about
it.

That is what makes both callers safe, and there are two. waitlists/privacy still
reaches this method inside the erasure transaction, on the database.Tx
dataprivacy.Eraser.Erase is handed, while a handler here opens one of its own —
which from the store's side is the same call, since a Tx does not say who opened
it. That is why the distinction cannot be drawn down there and is drawn here
instead. The RPC is behind a grant of its own — [PermissionEraseSignups] —
because a holder can take a named person off every list in the deployment in one
call.

# Two audiences, and that is the interesting half

ListOpenLists, Join and Withdraw are reachable without a grant. The other
fourteen are behind one. No surface before this one had both, and three things
follow from it.

The first is that "public" is a declaration rather than an omission.
[PublicMethods] names the three and [Require] declares them alongside the
fourteen, because authorization/grpc is fail-closed and a method declared
nowhere is denied — which nothing reports at wiring time. It is
authentication/signin/grpc's arrangement, applied to a service where only part
of the surface is public.

The second is that the scope has two sources and each call has exactly one. A
request carrying a principal takes its tenant from
[github.com/primandproper/platform-go/v14/callers.Principal.Scope], which the
consumer's interceptor proved. A request carrying nobody — which is what a
signup page looks like, and on a pre-launch list the visitor has nothing to sign
in to — takes it from a [ScopeResolver] reading the connection, which is
authentication/signin/grpc's precedent and is there for the same reason: the
caller has not become a principal. Neither is ever a request field, and
waitlists.proto reserves the name "scope" on every message so that protoc
refuses one being added, here and in a consumer's fork of the file alike.

The third is [SignupAuthorizer], and it is the trap identity/grpc's pattern
section says a surface owes an answer to. Withdraw is public and names a row. A
grant on the method could not say whose signup it is — the caller frequently
holds no grants at all — and the identifier is not a credential, because
waitlists mints it and Join hands it back. So the standing to move that row is a
seam with no default, asked inside the handler after the request is found well
formed and before anything is written. An action link redeemed through
platform-go/links is the shape the answer usually takes, and this package ships
none of it, because how a person proves they are themselves is the consumer's.

# What the public half is not allowed to answer

Whether an address is on a list. That is the one fact this half withholds, and
withholding it takes two decisions rather than one, because there are two ways
to ask.

GetSignupByContact is the direct way, and keeping it administrative is the
sharpest authorization decision on this service. It is a read, it looks harmless
beside Join, and answering it for anybody who can reach the port makes the
surface an oracle over which addresses are on which list — which is what the
list holds and what a person joining one has not agreed to publish.

Join is the other way, and it used to answer the same question by refusing.
waitlists.ErrAlreadySignedUp and waitlists.ErrContactWithdrawn told an anonymous
caller, per address they typed, whether it was on this list and whether its
owner had asked to be left alone — which is the oracle the paragraph above
declines to ship, reached from the write side. Nothing on a signup form
establishes that the caller owns the address, and "you already sent it" is not
an answer to that: an address is a public thing to hold and a private thing to
be on a list with.

So Join answers uniformly. A new signup, an address already on the list and one
that withdrew are one empty response — see [Server.Join] and
[waitlistspb.JoinResponse] — and neither sentinel is on
waitlists.ClientSafeSentinels any more. They are still what the store returns to
a Go caller, who is inside the trust boundary and needs them, and the
authenticated read above still answers the question for a console that holds a
grant. What is left to the visitor is what is true of the list rather than of
anybody's address: closed, or not there.

The cost is that the form cannot tell somebody they are already on the list, and
this package cannot buy it back: a uniform answer with nothing behind it means
an address can be put on a list by whoever typed it. The consumer's double
opt-in is what closes that, and waitlists' own documentation states the
obligation and why the send is not shippable here.

What the uniform answer does not cover is how long it takes. A join that
collides does one read and no insert, and a machine timing thousands of requests
can see the difference. Closing that would mean the handler doing the work it
declined to do, on every request, to keep the clock honest — and it is a
narrower channel than the one the sentinels were, which was a sentence naming
the answer.

The contact digest is absent for the same family of reasons and is absent
structurally: waitlists.proto reserves the name, so no response has anywhere to
put one. It is unsalted over a fast hash, deliberately — the store's own
documentation is explicit that it is not there to make a withdrawal secret — so
a client holding one could test any address it liked against it offline.

# What an archived row is worth asking for

include_archived is on every paged read's filter and it arrives on the wire, so
it is a request and not an instruction. This service archives two nouns under
two grants, and each read asks about the one it pages: the list reads honor the
field for a caller holding [PermissionArchiveLists] and the signup reads for one
holding [PermissionArchiveSignups]. Everybody else has it cleared before the
filter reaches the store, which on [Server.ListOpenLists] is everybody the RPC
exists for — a visitor who has not signed in carries no grants at all, so the
public catalog is the live catalog.

The authority comes from the authorization.GrantsExtractor a consumer supplies
to [WithGrantsExtractor], the same one they hand the enforcer. A server built
without it clears the field on every read, which is the fail-closed default.
archived.go carries the ruling.

# The writes open their own transactions

waitlists.Store's writes take a database.Tx, which only Client.WithTransaction
produces, so each write here opens one: an RPC handler is precisely the caller
with nothing of its own to join that the Store's documentation describes.

Four of them answer with a row — UpdateList, UpdateSignupNotes, Invite and
Convert — and the read behind each is the store's own, made on the transaction
the write ran in. This package made that read for itself until the store's writes
started returning what they wrote; the helper that did it is gone, and what is
left in its place carries a value out of a WithTransaction closure. It matters
most on the two transitions, because status_changed_at is the field a consumer
schedules a reminder off and it is stamped from the store's clock.

Five answer with nothing, and four of those now drop a row the store offered.
The two retirements drop it because the operator who sent the request already
holds the row and what the store hands back is for a consumer's audit entry
rather than for this wire. Withdraw drops it for a sharper reason: the row it
hands back is the one from *before* the blanking — the address, the notes, the
subject — and the caller who has just asked to be forgotten is the last caller to
send that to. Join drops it for a sharper one still, and the section above is
where that argument is. The erasure and its count are unchanged.

# What a refused withdrawal says

[github.com/primandproper/platform-go/v14/callers.ErrTargetNotPermitted] is what
a [SignupAuthorizer] returns to refuse, and it is never registered as a
client-safe sentinel. Its text says the caller was refused, and what this
surface does with it is answer as though nothing had been named — see
[Server.Withdraw], which is the shape a public RPC over a minted identifier
owes: a refusal that reads the same as an identifier nobody minted.

# Errors

This package registers nothing. waitlists.HTTPMapper and waitlists.GRPCMapper
live beside the sentinels they map, and installing them is the composition root's
one call — errormappers.Register, which service.Register makes for a service
built from a service.Config and a service assembled by hand makes itself.
Without it every sentinel this service returns arrives as codes.Unknown,
including the three a person on a signup page meets.

Every failure the store raises goes through one
grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*. The
encoding interceptor re-runs the registered mappers over the preserved chain, so
the mapper wins over the guess made at the call site, which is why no handler on
this surface switches on a sentinel.

The call sites that pass a code as an answer rather than as a default are the
ones the store never sees: a request that named no list, no signup and no
subject, a filter that could not be read, a connection whose scope could not be
resolved, and an administrative request with nobody on it. Each is a refusal this
package raises about the request itself, and there is nothing for a mapper to
say about them that the call site does not already know.

The refusals waitlists.ClientSafeSentinels names reach a client with the
sentinel's own wording rather than the code's name, and this is the service where
that matters most: all three are FailedPrecondition, each has a different
remedy, and the person reading them is looking at a signup page rather than a
log. The two sentinels that came off that list are the subject of the section
above; they no longer reach this wire at all.
*/
package grpc

//platform:transport resource surface: the catalog, the queue and the two audiences that reach them — over `waitlists.Store`
