package service

import (
	"context"
	"fmt"

	"github.com/primandproper/primitives-go/v2/healthcheck"
)

// Option configures what New assembles.
//
// It exists for the half of a service this package cannot see. Everything the
// config names, New finds on the injector; everything the application owns — its
// own loops, its own drains and its own health checks — arrives here, because no config can name
// a type this module does not define.
type Option func(*options)

// options collects what the options set.
type options struct {
	runners      []named[Runner]
	flushes      []named[func(context.Context) error]
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

// WithFlush joins an application-owned drain to the service's final-flush slot,
// the one platform's own drains run in: after every background loop — the
// application's runners among them — has closed, and before the clients are
// released. A nil flush is ignored.
//
// It is for a component that batches on a goroutine of its own and is fed by
// a loop, which is the one shape WithRunners gets wrong. A runner closes before
// every loop the config named, so a queue joined that way stops accepting while
// the scheduler pass that feeds it may still be running, and that pass's
// remaining writes are refused. Here it drains once nothing above it can hand
// it more.
//
// Application flushes run before platform's, in the order they were given. A
// drain written by the application may record usage or enqueue an operation,
// and the platform drains those land in run after it; nothing platform drains
// feeds an application's.
//
// A failure is reported under name, beside platform's own, and does not stop
// the flushes or the release of the clients after it.
func WithFlush(name string, flush func(context.Context) error) Option {
	return func(o *options) {
		if flush == nil {
			return
		}

		o.flushes = append(o.flushes, named[func(context.Context) error]{name: name, v: flush})
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
