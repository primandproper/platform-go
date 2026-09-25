/*
Package operationscfg assembles the long-running-operations tier — the store, the
service, the worker that runs operations, and the watcher that streams them —
from environment configuration.

There are two dependencies none of them can be built without: a database.Client
speaking Postgres, MySQL or SQLite, and a Registry naming the kinds of work this
build knows how to run. The dialect is not configured here at all — it comes off
the client, so the SQL cannot disagree with the database it runs against. The registry is not
configurable either, and cannot be: a Runner is a Go function, so a config file
has nothing to say about one.

# The shape of the nesting

An operations deployment is usually three processes wearing different hats out of
one binary: an API process that starts and reads operations, a worker fleet that
runs them, and whichever of them holds the SSE endpoints. Each half of this
config is inert for the processes that do not use it, so OPERATIONS_QUEUE_NAME
and OPERATIONS_WORKER_BATCH belong to one component and read like it.

The queue is nested rather than referenced. Its name has to agree with
Operations.QueueName on both sides of the seam — the service enqueues onto it and
the worker claims from it — and the surest way to make two things agree is to
derive them from one value, which NewQueue does.

In a container the queue is registered by name, under QueueKey, and resolved with
InvokeQueue. Every other registration here keys on its own type; this one cannot,
because *workqueue.Queue[string] is a type a consumer has every reason to
register too. QueueKey says why.
*/
package operationscfg
