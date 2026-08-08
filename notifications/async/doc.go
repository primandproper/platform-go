/*
Package async provides a channel-based async event delivery interface with
implementations for WebSocket, SSE, Pusher, and Ably.

# Two behavior classes

The providers differ in one way the AsyncNotifier interface does not express,
and it is the difference that matters most in production:

  - pusher and ably are fleet-safe. A hosted broker holds the client
    connections, so a Publish from any replica reaches every subscriber.
  - sse and websocket hold connections in this process's memory. A Publish on
    replica A reaches only the subscribers connected to replica A — and misses
    the rest silently, as absent notifications rather than as an error.

The self-hosted providers are therefore correct as they stand only at a single
replica. Beyond one, put them behind the messagequeue backplane in
notifications/async/fanout, which gives them the hosted providers' semantics;
the async notifications config package has a knob for it.
*/
package async
