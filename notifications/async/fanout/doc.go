/*
Package fanout gives the self-hosted async notification providers the semantics
the hosted ones already have: a Publish on any replica reaches every subscriber,
wherever that subscriber is connected.

# The problem

async.AsyncNotifier's providers split into two behavior classes the type system
does not distinguish. Pusher and Ably are fleet-safe because a hosted broker
holds the connections. The self-hosted sse and websocket providers hold
connections in process memory, so Publish on replica A misses every subscriber
connected to replica B — silently, as missing notifications rather than errors.
Those providers are therefore correct only at one replica, and the constraint
bites at exactly the moment a service first scales out.

# The shape

A Notifier wraps a local async.AsyncNotifier and a messagequeue publisher and
consumer, and is itself an async.AsyncNotifier and an async.ConnectionAcceptor,
so nothing downstream changes:

	n, err := fanout.New(ctx, cfg, local, publisherProvider, consumerProvider,
		fanout.WithLogger(logger), fanout.WithTracerProvider(tracerProvider))

Publish goes to a messagequeue topic and nowhere else. Every replica consumes
that topic and hands each event to its own local connections. Because publishing
only enqueues and delivery only happens on consumption, each event traverses
exactly one path to every connection — there is no origin marker and no dedup
machinery, because there is nothing to deduplicate.

That single-path property is the invariant to protect. A later "skip the round
trip for subscribers on this replica" optimization would reintroduce precisely
the duplicate delivery this design exists to avoid.

# One topic, channel in the envelope

messagequeue.ConsumerProvider is one-consumer-per-topic and returns
ErrConsumerAlreadyRegistered on a second request for a topic it already serves.
A topic per notification channel would therefore mean standing consumers up and
tearing them down as clients connect and disconnect, plus refcounting when two
clients on one replica share a channel. A single fixed topic — channel name
carried in the envelope, the handler routing into the local notifier — needs
exactly one consumer for the backplane's lifetime. The cost is that every
replica receives every event and filters locally, which is fine at notification
volume.

# Broker fit

Redis pub/sub is the natural messagequeue backend here. It is at-most-once with
no persistence, which is the same contract Pusher and Ably offer for a missed
message: the client reconciles on reconnect. Durable delivery is explicitly not
this seam — that is webhooks and outbox territory. A durable backend works too,
but redelivery buys nothing, since a notification nobody was connected for has
nowhere to go.

# Lifecycle

async.AsyncNotifier is Publish plus Close, but a backplane runs: it needs a live
consume loop and something draining the consumer's error channel, which the
messagequeue.Consumer docs require. New therefore takes a context and starts
both; Close cancels them and waits for them to exit. The context must outlive
the notifier — a request-scoped one stops delivery the moment its request ends.
*/
package fanout
