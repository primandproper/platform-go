/*
Package workqueuecfg assembles a work queue from environment configuration.

There is one thing to build and one dependency to build it from: a
database.Client speaking Postgres. The dialect is not configured here at all —
it comes off the client, so the SQL cannot disagree with the database it runs
against.

NewQueue is generic over the key type, which the caller names at the call site.
That is the only part of a queue the environment cannot express: a key is a Go
type, so a config file has nothing to say about it.

# Why there is no Config here

Every other config subpackage in this module declares its own Config, and this
one deliberately does not — it takes *workqueue.Config directly.

Those types earn their place by being a different shape than any single leaf
config: cachecfg.Config selects a provider and carries a circuit breaker,
outboxcfg.Config pairs a message queue with a relay, webhookscfg.Config gathers a
worker, an HTTP client, and a breaker. Where one of them does nest a leaf
package's config, that config is named for the role it plays there —
audit.RetentionConfig, saga.WorkerConfig, outbox.RelayConfig.

A work queue has exactly one thing to configure, so a wrapper here would hold a
single workqueue.Config field and nothing else. It would carry env:",init" with
no envPrefix, leaving every variable name identical; its EnsureDefaults and
ValidateWithContext would each forward one call to the leaf's; and workqueue.New
already rejects a nil config, applies defaults, and validates, so the forwarding
would run twice. That is a level of nesting in JSON and YAML in exchange for
nothing.

Resist adding one back for symmetry. A consumer that wants the queue under its
own key nests workqueue.Config in its application config the way uploadscfg.Config
nests objectstorage.Config.
*/
package workqueuecfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/workqueue"
)

// NewQueue builds a Queue from configuration.
//
// client must speak Postgres: this package's SQL is written against it rather
// than reduced to a portable subset, and workqueue.New returns
// dialect.ErrUnsupported for anything else. See the workqueue package doc for
// which construct is the binding one.
//
// K is the key type the queue schedules work for, and is the caller's to name:
//
//	queue, err := workqueuecfg.NewQueue[OrderID](ctx, cfg, client)
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything — including the key codec, which configuration has no way to
// express.
//
// The config is defaulted and validated by workqueue.New rather than here, so
// there is one place that decides what a usable queue config is.
//
// The returned Queue owns a goroutine and must be Closed.
func NewQueue[K comparable](
	ctx context.Context,
	cfg *workqueue.Config,
	client database.Client,
	opts ...Option,
) (*workqueue.Queue[K], error) {
	o := newOptions(opts)

	base := make([]workqueue.Option, 0, len(o.queue)+3) //nolint:mnd // the three observability options below

	if o.logger != nil {
		base = append(base, workqueue.WithLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, workqueue.WithTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, workqueue.WithMetricsProvider(o.metricsProvider))
	}

	return workqueue.New[K](ctx, cfg, client, append(base, o.queue...)...)
}
