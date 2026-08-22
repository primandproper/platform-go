package service

import (
	"fmt"

	"github.com/primandproper/platform-go/v13/healthcheck"
)

// Option configures what New assembles.
//
// It exists for the half of a service this package cannot see. Everything the
// config names, New finds on the injector; everything the application owns — its
// own loops and its own health checks — arrives here, because no config can name
// a type this module does not define.
type Option func(*options)

// options collects what the options set.
type options struct {
	runners      []named[Runner]
	healthChecks []healthcheck.Checker
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

// WithRunners joins application-owned background loops to the service's
// lifecycle. Nil entries are ignored.
//
// They start after everything the config named and close before it, which is
// the only order that can be right without knowing what they do: an
// application loop is built from the platform's clients and loops, so it is the
// one thing guaranteed to be downstream of all of them. A loop that writes
// outbox rows is finished before the relay drains; one that enqueues jobs is
// finished before the pool stops consuming.
//
// This is also how a generic loop joins. eventcapture.Recorder[E] satisfies
// Runner, but a type argument is not something a config can supply, so the
// application builds one and hands it over here.
//
// Failures are reported under each runner's type name, since a Runner arrives
// as a value with nothing else to call it by.
func WithRunners(runners ...Runner) Option {
	return func(o *options) {
		for _, runner := range runners {
			if runner == nil {
				continue
			}

			o.runners = append(o.runners, named[Runner]{name: fmt.Sprintf("%T", runner), v: runner})
		}
	}
}

// WithHealthChecks joins application-owned checks to the registry the service
// answers its probes from. Nil entries are ignored.
//
// The infrastructure the config named is already in there — Register wraps the
// database client and the message queue publisher it registered — so this is for
// what the platform cannot see: a domain dependency, a third-party API this
// service is useless without, and the components whose types no config can name.
// A cache is the standing example, since cache.Cache[T] is registered per
// concrete type:
//
//	service.WithHealthChecks(healthcheck.NewCacheChecker("sessions", sessionCache))
//
// Every check joined here is reported by both transports: it appears in the
// /readyz body under its own name, and a grpc_health_v1 Check can ask for it by
// that same name.
//
// A check that is slow or hangs bounds only itself — the registry runs them
// concurrently, each under its own timeout — but it still delays the probe, so a
// check should ask its component a cheap question rather than exercise it.
func WithHealthChecks(checks ...healthcheck.Checker) Option {
	return func(o *options) {
		for _, check := range checks {
			if check == nil {
				continue
			}

			o.healthChecks = append(o.healthChecks, check)
		}
	}
}
