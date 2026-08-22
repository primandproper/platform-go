package tracingcfg

import (
	"github.com/primandproper/platform-go/v13/observability/logging"
)

// Option configures how NewTracerProvider assembles its provider.
//
// The logger is an option rather than a parameter because it is genuinely
// optional: an absent logger logs nowhere. Requiring it positionally made a
// caller that wanted no logging name one anyway, usually a noop.
//
// There is no WithPillars here, and there cannot be: the observability package
// that defines Pillars imports this one in order to build them. A pillar's own
// constructor is what a Pillars gets assembled from, so it takes the one
// dependency that precedes it and nothing else.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger logging.Logger
}

// newOptions applies opts, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}
