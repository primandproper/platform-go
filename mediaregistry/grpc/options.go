package grpc

import (
	mediaregistryhttp "github.com/primandproper/platform-go/v15/mediaregistry/http"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures a [Server] at construction.
type Option func(*Server)

// WithCallerResolver supplies the function that says who is asking and which
// tenant they are asking in. It is required; see ErrNilCallerResolver.
//
// It is mediaregistry/http's CallerResolver, deliberately: one deployment has
// one answer to who is calling its media registry, and a composition root
// that mounts both surfaces hands both the same function.
func WithCallerResolver(resolver mediaregistryhttp.CallerResolver) Option {
	return func(s *Server) { s.resolver = resolver }
}

// WithEntitlement replaces OwnerOnly as the rule deciding which objects a
// caller may read. It is mediaregistry/http's Entitlement, and it is asked of
// every row this surface hands back; a nil one leaves OwnerOnly in place.
//
// It governs reads and only reads. Archiving is the owner's, whatever the
// entitlement says: a rule that lets everybody on a ticket open its
// attachments is not a rule that lets everybody on it remove them.
func WithEntitlement(entitlement mediaregistryhttp.Entitlement) Option {
	return func(s *Server) {
		if entitlement != nil {
			s.entitlement = entitlement
		}
	}
}

// WithKeyFunc replaces DefaultKeyFunc as the layout an upload's key is built
// with. A nil one leaves the default in place.
//
// It moves the default RecordKeyPolicy with it, since that policy reads the
// caller's prefix off whichever layout the server was built with.
func WithKeyFunc(keys KeyFunc) Option {
	return func(s *Server) {
		if keys != nil {
			s.keys = keys
		}
	}
}

// WithContentTypePolicy replaces the default content-type rule — refuse what a
// browser executes, and a type nobody stated — with the deployment's own,
// AllowContentTypes most often. A nil one leaves the default in place.
func WithContentTypePolicy(policy ContentTypePolicy) Option {
	return func(s *Server) {
		if policy != nil {
			s.contentTypes = policy
		}
	}
}

// WithMaxBytes replaces DefaultMaxBytes as the largest upload accepted. A
// value that is not positive leaves the cap where it was: switching it off is
// WithoutMaxBytes, by name.
func WithMaxBytes(limit int64) Option {
	return func(s *Server) {
		if limit > 0 {
			s.maxBytes = limit
		}
	}
}

// WithoutMaxBytes accepts uploads of any size. It exists so that an uncapped
// surface is one somebody asked for in so many words; see DefaultMaxBytes.
func WithoutMaxBytes() Option {
	return func(s *Server) { s.maxBytes = 0 }
}

// WithRecordKeyPolicy replaces KeysUnderPrefix as the rule deciding which keys
// a caller may register as theirs. A nil one leaves the default in place.
func WithRecordKeyPolicy(policy RecordKeyPolicy) Option {
	return func(s *Server) { s.recordKeys = policy }
}

// WithAfterUpload supplies what runs once an upload is stored and registered —
// metering, most often. See AfterUpload for why it cannot fail the upload.
func WithAfterUpload(after AfterUpload) Option {
	return func(s *Server) { s.afterUpload = after }
}

// WithLogger sets the server's logger.
func WithLogger(logger logging.Logger) Option {
	return func(s *Server) { s.logger = logger }
}

// WithTracerProvider sets the server's tracer provider.
func WithTracerProvider(provider tracing.Provider) Option {
	return func(s *Server) { s.tracerProvider = provider }
}

// WithMetricsProvider sets the server's metrics provider.
func WithMetricsProvider(provider metrics.Provider) Option {
	return func(s *Server) { s.metricsProvider = provider }
}

// WithPillars supplies logger, tracer provider and metrics provider at once.
//
// Options apply in order, so WithPillars(p) followed by WithMetricsProvider(nil)
// leaves this server unmetered.
func WithPillars(pillars *observability.Pillars) Option {
	return func(s *Server) {
		if pillars == nil {
			return
		}

		s.logger = pillars.Logger
		s.tracerProvider = pillars.TracerProvider
		s.metricsProvider = pillars.MetricsProvider
	}
}
