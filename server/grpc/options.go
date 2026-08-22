package grpc

import (
	"github.com/primandproper/platform-go/v13/healthcheck"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the server this package constructs. The zero configuration
// works: an absent logger logs nowhere and an absent tracer provider traces
// nowhere.
type Option func(*options)

type options struct {
	logger                logging.Logger
	tracerProvider        tracing.Provider
	healthRegistry        healthcheck.Registry
	serviceName           string
	maxReceiveMessageSize int
	maxSendMessageSize    int
	reflection            bool
}

func newOptions(opts []Option) *options {
	cfg := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on served
// RPCs.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithServiceName names the server's logger and instrumentation scope. It
// mirrors the HTTP server's serviceName, which is an option there for the same
// reason: a hardcoded name makes two servers in one process indistinguishable
// in logs.
func WithServiceName(serviceName string) Option {
	return func(o *options) { o.serviceName = serviceName }
}

// WithReflection registers the gRPC server reflection service.
//
// It is off by default. Reflection enumerates every method and message the
// server exposes to anyone who can reach the port, which is a debugging
// convenience in development and an inventory of the attack surface in
// production — so it is opted into rather than out of.
func WithReflection() Option {
	return func(o *options) { o.reflection = true }
}

// WithHealthRegistry registers the grpc_health_v1 service, backed by the given
// registry. It names the same registry the HTTP sibling's option of the same
// name takes, so both transports answer from one set of checkers rather than
// from two that can disagree.
//
// A nil registry registers nothing. A server that registers its own
// grpc_health_v1 implementation through a RegistrationFunc must not also pass
// this — gRPC rejects a service registered twice by panicking.
func WithHealthRegistry(registry healthcheck.Registry) Option {
	return func(o *options) { o.healthRegistry = registry }
}

// WithMaxReceiveMessageSize bounds a single received message, in bytes,
// overriding Config.MaxReceiveMessageSize.
//
// It is named after gRPC's own vocabulary rather than after
// routing.WithMaxRequestBody, its HTTP counterpart, because Config spells the same
// number MaxReceiveMessageSize and one knob with two names in one package is
// the thing worth avoiding. The bound is per message, not per RPC: a stream of
// a thousand messages is bounded a thousand times, once each.
//
// Zero leaves the Config field to decide. UnboundedMessageSize removes the
// bound; anything negative or past it is refused by NewGRPCServer.
func WithMaxReceiveMessageSize(size int) Option {
	return func(o *options) { o.maxReceiveMessageSize = size }
}

// WithMaxSendMessageSize bounds a single sent message, in bytes, overriding
// Config.MaxSendMessageSize, on the same terms as WithMaxReceiveMessageSize.
//
// This is the direction worth setting deliberately. grpc-go leaves send
// effectively unbounded, so an oversized response fails on whichever client
// called — under its own 4 MiB receive default, in a process the service owner
// may not operate. Platform bounds it at DefaultMaxMessageSize so the failure
// belongs to the handler instead; raising it here means also raising the
// calling client's receive bound, which is the one that actually breaks.
func WithMaxSendMessageSize(size int) Option {
	return func(o *options) { o.maxSendMessageSize = size }
}
