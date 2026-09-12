/*
Package http mounts the operations read surface on a routing.Router.

It is the "watch" half of the long-running-operation pattern: given an operation
ID, poll it, list what an owner has running, ask one to stop, or subscribe to it
over server-sent events and be pushed each state it reaches.

	handlers, err := operationshttp.New(svc,
		operationshttp.WithOwnerResolver(ownerFromSession),
		operationshttp.WithWatcher(watcher),
		operationshttp.WithLogger(logger))
	if err != nil {
		return err
	}

	handlers.Mount(router)

Mount registers all four routes. A consumer that wants some of them calls
MountGet, MountList, MountCancel, and MountEvents itself and leaves out the rest
— a deployment whose operations should run to completion is the usual reason,
and it mounts everything but MountCancel.

What this package cannot do is hand back a list of routes for somebody else to
register. routing.Route is what a registration returns rather than something that
can be registered, and routing.Get is generic over its handler's input and output
types, so the typed registration has to happen where those types are still known.
That is here, and per-route methods are the whole of the choice that leaves.

# Starting is not here, and that is deliberate

There is no generic POST that starts an operation, and there will not be one.

A start endpoint's request body is the *kind's* request type — an export's
subject and format, a reindex's shard list — so a generic one would have to take
an opaque JSON object. That is three separate losses at once: the OpenAPI
document would describe the body as "anything", so no generated client could help
anyone call it; the consumer's own validation and authorization would be bypassed,
because this package cannot know which fields of somebody else's request need
checking against which permission; and the endpoint would accept a kind name from
the request, letting a caller start any registered work by naming it.

So the consumer declares its own start endpoint — POST /exports, POST /reindexes
— with its own typed body, its own middleware, and its own OpenAPI, and returns
Accepted(op) from the handler. That is one line at the end of a handler the
consumer was writing anyway, and it is the line that makes the response a 202
carrying an operation ID and a Location header pointing at the endpoints below.

# Ownership is required

WithOwnerResolver has no default. The tenancy.Scope it returns is bound into
every statement a request makes — the polling read, the listing, the read the
cancellation makes first, and the subscription — so what confines a request to
one tenant is the query rather than a comparison a handler remembered to make
afterwards. That comparison used to be here, and it is gone: this surface made
it and the next one would have been the one that did not.

A resolver that returns the zero Scope fails the request at the driver rather
than widening it. A single-tenant deployment that genuinely has no owners passes
GlobalOwner instead, which is a name rather than an omission: it makes "every
operation here belongs to no tenant" a decision somebody wrote down, and it is
the counterpart of leaving operations.WithOwner off at Start.

# The event stream is registered on the backend

Every endpoint here goes through routing's typed registration and carries OpenAPI
for free, except the SSE one, which is registered on the Backend and then written
into the document by hand.

The reason is that routing's typed handlers are func(ctx, In) (Out, error): a
handler returns a value and the framework encodes it, once. A stream never
returns — it holds the response writer for minutes and writes a frame at a time —
so it cannot be expressed in that shape, and expressing it approximately would
generate a document describing a JSON body the endpoint never sends. Going
through the Backend still gets it the router's middleware, and DescribeStream
puts a text/event-stream operation in the spec that says what actually happens.

# One replica per stream, as ever

An SSE connection lives on the process that accepted it, and this package's
Watcher reads the database rather than a broadcast, so a fleet needs no affinity
and no fan-out: every replica can serve a subscription to any operation, because
every replica can read the row. That is the property that makes this different
from primitives-go's notifications/async, whose single-replica constraint comes
from holding state in the process.

# Why this package exists at all, when most stores here ship no handlers

Almost nothing in this module ships a resource surface — a component that owns
data ships a store, and the routes over it are the application's. This package
is the one exception, and it is one because every type on the wire here is this
module's own: an Operation, its two-tier progress and the states it moves
through are not yours, and polling one or subscribing to it is the pattern's
protocol rather than your API. The half that *is* yours — starting the work — is
the half above that is deliberately absent.

The module README's "Transports" section is where that rule is stated for the
module as a whole, along with everything else on this side of it.
*/
package http

//platform:transport resource surface: poll, list, cancel, subscribe — over `Operation`
