/*
Package grpc is webhook endpoint management on the wire: the gRPC service over
[github.com/primandproper/platform-go/v14/webhooks]'s Dispatcher and Store, the
converters between the generated messages and its types, a typed client, and the
default permission fragment a consumer composes into its policy.

It is imported as webhooksgrpc.

	srv, _ := webhooksgrpc.NewServer(dispatcher, store, db, extractPrincipal)
	// []grpcserver.RegistrationFunc{srv.RegisterOn}

It is the fourth domain surface in this module and it follows the pattern
identity/grpc's documentation states rather than restating it: who is calling is
an interface a consumer's own authentication interceptor satisfies, what each
method requires is a default map a consumer composes into their own, the scope
binds off the connection, a consumer's catalog stays an opaque string, and a
write whose caller is already inside the process's own transaction is not an
RPC. What is below is where webhooks lands on each and where it diverges.

# Who is calling

Who is calling is [github.com/primandproper/platform-go/v14/callers.Principal],
which a consumer's own authentication interceptor puts on the context and
[github.com/primandproper/platform-go/v14/callers.PrincipalExtractor] reads
back. Those are one package for the whole module rather than an interface per
surface, because a deployment has one authentication interceptor and one notion
of a caller, and that package's documentation is where the ruling that keeps the
method set at three lives.

This surface reads both of the facts it needs off one, which is one more than
the registry next door reads. The scope bounds every statement; the user
identifier is what SaveEndpoint writes into Endpoint.CreatedBy, so that "who
registered this endpoint" is answered by the connection rather than by a request
field somebody could put anything in.

# Eleven RPCs, and nine absences

Endpoint CRUD, the signing-key rotation, subscription CRUD, the catalog a
subscription is judged against, and the delivery log. That is the half of
webhooks that is a resource rather than a protocol: an operator adds a URL,
picks event types from what ListEventTypes offers, rotates a secret, and then
asks whether it got through and what came back. It is the only half a person
ever touches.

The other nine methods of webhooks.Store are absent, and each says so on itself
rather than only here, so that a reader of the Store finds the answer where they
are standing. Seven are the delivery machinery the Store already grouped under
"The delivery machinery takes neither" — Claim, MarkDelivered, RecordFailure,
RecordAttempt, Requeue, Backlog and Reap — and they are the machinery test in
its plainest form: the queue protocol's correctness is that a claim commits
before the request goes out, and an RPC is a caller choosing when that commit
happens.

EndpointsForEvent is the internal fan-out, the dispatcher asking itself who is
subscribed on the way to its own work.

Enqueue is the one worth arguing about, because it is the only absence here that
is genuinely consumer-facing. It writes a delivery and one dispatch per endpoint
in the caller's transaction, so that both commit with whatever else that
transaction did — and that clause is the whole of it. Over a wire the write
lands in a transaction of its own, at a moment the caller does not choose, so
you get a delivery for a row that rolled back or a committed row nobody was told
about. audit.Recorder states the same fact about an audit entry, and a consumer
who wants to publish an event from another process is describing an RPC of their
own, with webhooks.Dispatcher.Dispatch called inside its transaction.

# The tenant comes off the connection, and the schema enforces it

Every statement behind these RPCs binds the scope the caller's principal names,
and no message in webhooks.proto has a scope field. That second half is the
schema's rather than this package's: the name is reserved on every request and
on the three messages a response is built from, so protoc refuses a scope field
being added — here and in a consumer's fork of the file alike. It is
audit/grpc's pattern, adopted rather than re-derived, and it matters as much
here as there, because a scope a client could name on this surface is a read of
where another tenant's events are being sent.

# Signing keys go one way

A subscriber authenticates a delivery by its HMAC, so an endpoint's keys are
what lets somebody impersonate this deployment to them. webhooks.Store.GetEndpoint
reads an endpoint "secrets included" and [EndpointToProto] is where they stop:
webhookspb.WebhookEndpoint has no field to put them in, which makes the property
structural rather than a line somebody has to remember not to write.

They travel in the other direction on two messages, and they are required on
every save — including one that changes only a name — because no RPC here reads
the stored keys back and a save writes what it was given. So a save is a full
re-registration. That is a cost, and it is the cost worth paying: the
alternative in which an omitted keyring means "leave them alone" is one keystroke
away from an endpoint whose signature checks quietly stopped matching, and the
one refusal is visible at the console.

RotateSecret is the second message, and it is what keeps that cost from having a
second half. Rotating through a save means naming both keys, so a client that
rotates is a client that had to read the outgoing one — which is the read this
surface does not have and must not grow. The rotation names the incoming key
alone and the outgoing one is demoted by the statement itself, inside the
engine, so it is never a value any message or any process holds. Its response is
empty for that reason rather than for economy: it is the call that would most
plausibly have handed a key back. Rotating to the key already in force is a
no-op rather than a second rotation, so a retried request cannot close the
window the first one opened.

The keyless save is refused here rather than by webhooks.GRPCMapper, with
codes.InvalidArgument as this one call site's default. webhooks.ErrNoSigningSecret
is requestsigning.ErrNoSigningKey rather than a sentinel of this module's, so a
mapper case would decide what a keyring with no key means for every other caller
of requestsigning in the process, where it is a wiring failure and a 500 is
honest.

# Three writes go through the dispatcher and one goes through the store

Register and Subscribe are gates, not wrappers. Register validates the URL an
authenticated request from inside the deployment is about to be made to — SSRF
prevention, checked here and again at delivery because DNS is mutable — and
Subscribe checks the event type against the consumer's catalog, so that a typo
is a refusal rather than an endpoint that never fires. SaveEndpoint and
AddSubscription therefore write through webhooks.Dispatcher.

RotateSecret writes through the dispatcher as well, though its gate is the
smallest of the three: an empty key is refused before the transaction is opened,
because a rotation to nothing leaves an endpoint that cannot be delivered to at
all. There is no URL to check — the request names none — and the one on the row
was checked when it was registered and is checked again at delivery.

ArchiveEndpoint writes through the store, and it is the one write here that
does. There is no Dispatcher.Unregister, because nothing about retiring an
endpoint needs a URL checked or a catalog consulted; the seam's absence is the
answer rather than an omission.

Every write opens its own transaction with Client.WithTransaction, because
webhooks.Dispatcher's methods take a database.Tx and an RPC handler is precisely
the caller that method's documentation describes: one with nothing of its own to
join. SaveEndpoint reads the endpoint back inside that transaction, which is what
the Store's reads taking an executor rather than a reader is for — the read sees
the write it follows, and the response carries the timestamps the database
stamped rather than the epoch.

# Authorization has one half here, not two

identity/grpc has a TargetAuthorizer beside its permission map because eleven of
its RPCs name a row whose relationship to the caller cannot be read off the
method — an account they may or may not be a member of, within their own tenant.
Nothing here has that shape. Every row this surface reaches is reached by a
statement that binds the caller's scope, so an endpoint, a subscription or a
delivery in another tenant simply is not there, and the only other owner-shaped
field webhooks has is Endpoint.CreatedBy — which the package documents as
provenance rather than as what bounds a read, and which this surface fills from
the principal rather than from a request.

That is the arrangement identity/grpc's pattern section arrives at from the
other direction, having paid for the alternative once: an ownership check
standing in front of a write is not a check, because it reads through one
connection what the write will act on through another. The owner belongs in the
statement, and here it always is.

# Subscription means something else in billing

This service and billing's both declare GetSubscription, ListSubscriptions and
ArchiveSubscription, over two unrelated nouns: a paid plan there, an event
interest here. The wire tells them apart — the two live in different proto
packages, and their generated Go types in different packages again — so nothing
about a client's calls is ambiguous.

What is not ambiguous but does not compile is embedding both generated server
interfaces in one type. Go refuses overlapping method sets whose signatures
differ, and these differ in every request and response type, so a consumer
serving both domains from one struct gets "duplicate method GetSubscription" and
two more like it. The fix is to hold the two as fields rather than embedding
them, which is what a consumer serving two domains from one process wants
anyway.

It is written down rather than renamed because the name is right in both places
and the wire has no collision to fix. Renaming either side would make one
domain's vocabulary worse to serve an optional Go composition pattern, and would
not generalize: any two domains here that share a noun and the Get/List/Archive
shape collide the same way, and there is no rename that prevents the next one.

# Errors

This package registers nothing. webhooks.HTTPMapper and webhooks.GRPCMapper live
beside the sentinels they map, and installing them is the composition root's one
call — errormappers.Register, which service.Register makes for a service built
from a service.Config and a service assembled by hand makes itself. Without it
every sentinel this service returns arrives as codes.Unknown.

Every failure here is one grpcerrors.PrepareAndLogGRPCStatus with codes.Internal
as the *default*. The encoding interceptor re-runs the registered mappers over
the preserved chain, so the mapper wins over the guess made at the call site,
which is why no handler on this surface switches on a sentinel. The three
exceptions pass a code because nothing maps what they raise: a save that named no
endpoint, a save that named no signing keys, and a rotation that named no key to
rotate to.
*/
package grpc

//platform:transport resource surface: endpoint management, subscriptions and the delivery log — over `webhooks.Dispatcher` and `webhooks.Store`
