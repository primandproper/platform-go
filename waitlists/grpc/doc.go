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
erasure path waitlists/privacy builds a dataprivacy.Eraser on. It is on the wire
because its realistic caller is an operator honoring a request out of band, and
it is behind a grant of its own — [PermissionEraseSignups] — because a holder
can take a named person off every list in the deployment in one call.

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
request carrying a principal takes its tenant from [Principal.Scope], which the
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

GetSignupByContact is administrative, and that is the sharpest authorization
decision on this service. It is a read, it looks harmless beside Join, and
answering it for anybody who can reach the port makes the surface an oracle over
which addresses are on which list — which is what the list holds and what a
person joining one has not agreed to publish.

The question a person on a signup page is actually asking is "am I already on
this", and Join's refusal answers it: waitlists.ErrAlreadySignedUp and
waitlists.ErrContactWithdrawn reach them with the sentinel's own wording, and
neither discloses anything the caller did not already send.

The contact digest is absent for the same family of reasons and is absent
structurally: waitlists.proto reserves the name, so no response has anywhere to
put one. It is unsalted over a fast hash, deliberately — the store's own
documentation is explicit that it is not there to make a withdrawal secret — so
a client holding one could test any address it liked against it offline.

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

Five answer with nothing, and three of those now drop a row the store offered.
The two retirements drop it because the operator who sent the request already
holds the row and what the store hands back is for a consumer's audit entry
rather than for this wire. Withdraw drops it for a sharper reason: the row it
hands back is the one from *before* the blanking — the address, the notes, the
subject — and the caller who has just asked to be forgotten is the last caller to
send that to. The erasure and its count are unchanged.

# Errors

This package registers nothing. waitlists.HTTPMapper and waitlists.GRPCMapper
live beside the sentinels they map, and installing them is the composition root's
one call — errormappers.Register, which service.Register makes for a service
built from a service.Config and a service assembled by hand makes itself.
Without it every sentinel this service returns arrives as codes.Unknown,
including the four a person on a signup form meets.

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
that matters most: four of the five are FailedPrecondition, each has a different
remedy, and the person reading them is looking at a signup form rather than a
log.
*/
package grpc

//platform:transport resource surface: the catalog, the queue and the two audiences that reach them — over `waitlists.Store`
