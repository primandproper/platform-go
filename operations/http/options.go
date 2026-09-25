package http

import (
	"time"

	"github.com/primandproper/platform-go/v14/operations"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/eventstream/sse"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// DefaultHeartbeatInterval is how often a stream with nothing to report reports
// nothing anyway. See WithHeartbeatInterval.
//
// Fifteen seconds, which clears the 60-second idle timeout that nginx, an AWS
// ALB and most of the other things a stream travels through ship as their
// default — with room for three ticks before the first of them fires, so a
// missed one is not a closed connection. Being comfortably under thirty is the
// property that matters; the rest of the margin is slack.
const DefaultHeartbeatInterval = 15 * time.Second

// ErrNilOwnerResolver indicates handlers built without an OwnerResolver.
//
// It has no default, and that is the point. Every read this package serves is
// scoped to an owner, and a default of "no scoping" would make the safe wiring
// and the wiring that serves every tenant's operations to anyone look identical.
// A deployment that genuinely has no owners passes GlobalOwner, by name.
var ErrNilOwnerResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operations owner resolver")

// ErrNoOwners is an OwnersResolver that answered a request with no owners.
//
// Refused rather than read as "then this request reads nothing", for the reason
// the single resolver cannot answer the zero Scope: an empty set is a resolver
// that did not decide, and a request it did not decide about is not one to
// answer with an empty page that looks like a real one.
var ErrNoOwners = platformerrors.New("the operations owners resolver named no owner for this request")

// ErrInvalidHeartbeatInterval indicates WithHeartbeatInterval was given a
// non-positive interval.
//
// Refused rather than read as "then send no heartbeats", because those are the
// two readings a caller most needs told apart and the silent one is the
// expensive half: it is exactly the behavior this package had before there was a
// heartbeat, and its symptom is not here — it is a stream a proxy somewhere else
// closed, minutes later, mid-operation. There is deliberately no spelling of
// "no heartbeat at all": a long-lived connection nobody ever writes to is not
// something a deployment wants, it is the bug this interval exists to fix.
var ErrInvalidHeartbeatInterval = platformerrors.Wrap(
	platformerrors.ErrUnrecognizedInputValue,
	"non-positive operations event stream heartbeat interval",
)

type (
	// Option configures the handlers at construction.
	Option func(*options)

	options struct {
		resolver       OwnerResolver
		owners         OwnersResolver
		watcher        *operations.Watcher
		logger         logging.Logger
		tracerProvider tracing.Provider

		basePath string
		tags     []string

		heartbeat      time.Duration
		reconnectDelay sse.ReconnectDelay
	}
)

func newOptions(opts []Option) *options {
	o := &options{basePath: BasePath, tags: []string{"operations"}, heartbeat: DefaultHeartbeatInterval}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithOwnerResolver supplies the function that says whose operations a request
// may read. It is required; see ErrNilOwnerResolver.
func WithOwnerResolver(resolver OwnerResolver) Option {
	return func(o *options) { o.resolver = resolver }
}

// WithOwnersResolver supplies the function that says every owner whose
// operations a request may read, for a deployment that starts operations under
// more than one.
//
// It satisfies the resolver requirement on its own, and where both are given it
// is the one used. See OwnersResolver for when a deployment has more than one
// owner, and what the function must not do.
func WithOwnersResolver(resolver OwnersResolver) Option {
	return func(o *options) { o.owners = resolver }
}

// WithWatcher enables the server-sent-events endpoint.
//
// Without one, that endpoint is not registered and not documented, and the
// polling endpoints are unaffected. Polling an operation every couple of seconds
// is a complete implementation of the watch path; the stream is what saves the
// request per poll.
//
// The Watcher's Run must be started, or every subscription receives its first
// snapshot and then nothing.
func WithWatcher(watcher *operations.Watcher) Option {
	return func(o *options) { o.watcher = watcher }
}

// WithBasePath mounts the surface somewhere other than /operations.
//
// It changes the paths Accepted renders too, so a consumer's own 202 keeps
// pointing at the endpoints that actually exist.
func WithBasePath(basePath string) Option {
	return func(o *options) {
		if basePath != "" {
			o.basePath = basePath
		}
	}
}

// WithHeartbeatInterval sets how often the event stream writes a heartbeat
// frame while the operation it is following has nothing new to say.
//
// The default is DefaultHeartbeatInterval and is already under every idle
// timeout worth naming; what this is for is a deployment that knows its own
// proxy is stricter than that. A non-positive interval is refused at
// construction — see ErrInvalidHeartbeatInterval — rather than turning the
// heartbeat off.
//
// It has no effect without WithWatcher: no stream is registered, so there is
// nothing to keep warm.
func WithHeartbeatInterval(interval time.Duration) Option {
	return func(o *options) { o.heartbeat = interval }
}

// WithReconnectDelay sets the SSE "retry:" field every stream opens with, which
// is how long a disconnected client waits before reconnecting.
//
// The value comes from sse.NewReconnectDelay, which is where one the wire format
// cannot carry is refused — so this option, like every other here, cannot fail
// to be applied. Naming none emits no field and leaves each client on its own
// default, a few seconds, which is a reasonable answer for a single browser and
// a poor one for a fleet that will all reconnect at once when a proxy restarts.
// Which of those a deployment has is not something this package can tell, which
// is why there is no default rather than a guess.
func WithReconnectDelay(delay sse.ReconnectDelay) Option {
	return func(o *options) { o.reconnectDelay = delay }
}

// WithTags sets the OpenAPI tags on every registered operation, replacing the
// default of "operations".
func WithTags(tags ...string) Option {
	return func(o *options) { o.tags = tags }
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}
