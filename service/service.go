package service

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v10/analytics"
	"github.com/primandproper/platform-go/v10/database"
	"github.com/primandproper/platform-go/v10/dataprivacy"
	"github.com/primandproper/platform-go/v10/distributedlock"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/featureflags"
	"github.com/primandproper/platform-go/v10/healthcheck"
	"github.com/primandproper/platform-go/v10/internal/injection"
	"github.com/primandproper/platform-go/v10/jobs"
	"github.com/primandproper/platform-go/v10/messagequeue"
	"github.com/primandproper/platform-go/v10/metering"
	async "github.com/primandproper/platform-go/v10/notifications/async"
	"github.com/primandproper/platform-go/v10/observability"
	"github.com/primandproper/platform-go/v10/observability/logging"
	"github.com/primandproper/platform-go/v10/outbox"
	"github.com/primandproper/platform-go/v10/ratelimiting"
	"github.com/primandproper/platform-go/v10/saga"
	"github.com/primandproper/platform-go/v10/secrets"
	grpcserver "github.com/primandproper/platform-go/v10/server/grpc"
	httpserver "github.com/primandproper/platform-go/v10/server/http"
	"github.com/primandproper/platform-go/v10/uploads"
	"github.com/primandproper/platform-go/v10/webhooks"

	"github.com/samber/do/v2"
)

// Service is a composed service's lifecycle: everything the config named, built
// in the order a service has to come up and held in the order it has to go
// down.
//
// It is not a second injector and not a registry. Every component it holds is
// still individually invocable from the injector it was built from — Service
// keeps a reference only because a shutdown ordering has to be written down
// somewhere, and the injector's own dependency graph does not know that the
// outbox relay has to outlive the HTTP server.
type Service struct {
	logger      logging.Logger
	shutdownErr error
	pillars     *observability.Pillars

	// Each slice is stored in startup order and walked backwards on the way
	// out, so adding a component in the right place is the whole of getting its
	// shutdown right.
	closers []named[func(context.Context) error]
	runners []named[Runner]
	flushes []named[func(context.Context) error]
	servers []named[Server]

	shutdownTimeout time.Duration

	shutdownOnce sync.Once
}

// New assembles the lifecycle of the service registered with i.
//
// It builds, in the order a service has to come up: the infrastructure clients,
// the platform clients that hold a connection, the background loops, and the
// servers last — nothing binds a listener before what it will serve from
// exists. Building here is eager on purpose. do.Provide is lazy, so without it
// a database whose credentials are wrong is a process that starts, reports
// healthy, and fails on its first request; here it is a startup error.
//
// Everything is resolved optionally, which keeps this a walk of what the config
// named rather than a list of what a service must have: a subsystem nobody
// configured was never registered and contributes nothing, while a subsystem
// that was registered and cannot be built is returned as an error.
//
// New starts nothing and blocks on nothing. Run does both.
//
// Prerequisites: the injector must be one Register has run against, since New
// reads *Config from it, and must already carry the application's own types —
// a gRPC server with no registered handlers is a wiring error New reports here
// rather than at the first RPC.
func New(i do.Injector, opts ...Option) (*Service, error) {
	o := newOptions(opts)

	cfg, err := do.Invoke[*Config](i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking the service config")
	}

	pillars, err := observability.InvokePillars(i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking the observability pillars")
	}

	svc := &Service{
		logger:          logging.NewNamedLogger(pillars.Logger, cfg.Name),
		pillars:         pillars,
		shutdownTimeout: cfg.ShutdownTimeout,
	}

	// A Config that was validated has a timeout; one assembled in code and
	// handed straight to Register has whatever the caller left there, and a
	// zero budget would turn every shutdown into an immediate deadline.
	if svc.shutdownTimeout == 0 {
		svc.shutdownTimeout = DefaultShutdownTimeout
	}

	r := &resolver{i: i}

	svc.resolveInfrastructure(r)
	svc.resolveClients(r)
	svc.resolveHealth(r, o.healthChecks)
	svc.resolveRunners(r)
	svc.resolveFlushes(r)
	svc.resolveServers(r)

	if r.err != nil {
		return nil, r.err
	}

	// Application loops go last, so they stop first: one that writes outbox
	// rows or enqueues jobs has to be finished before the relay and the pool it
	// was writing into are.
	svc.runners = append(svc.runners, o.runners...)

	return svc, nil
}

// resolver walks the injector and remembers the first failure, so a startup
// sequence reads as the list of what a service is made of rather than as a
// stack of identical error checks.
type resolver struct {
	i   do.Injector
	err error
}

// resolve hands fn whatever the injector can build for T, and nothing at all
// when nobody registered one.
//
// The optional invoke is what keeps New a walk of the config: absence is what a
// nil sub-config already means and is not a failure, while a component that was
// registered and cannot be built is.
func resolve[T any](r *resolver, fn func(T)) {
	if r.err != nil {
		return
	}

	v, err := injection.InvokeOptional[T](r.i)
	if err != nil {
		r.err = platformerrors.Wrapf(err, "invoking %s", do.NameOf[T]())

		return
	}

	if isAbsent(v) {
		return
	}

	fn(v)
}

// isAbsent reports whether v is what InvokeOptional yields for a service nobody
// registered.
//
// That zero value is a nil interface for the interface-typed components and a
// typed nil pointer for the rest. Only reflection reads those two as the same
// nil; `any(v) == nil` reads the second as a live component and hands shutdown a
// nil receiver.
func isAbsent[T any](v T) bool {
	return reflect.ValueOf(&v).Elem().IsZero()
}

// resolveInfrastructure collects the clients everything else is built on.
//
// They are appended in the order they come up, which reversed is the order they
// are closed — so the database client, which every loop above it can still
// touch on its way out, is the last thing released.
func (s *Service) resolveInfrastructure(r *resolver) {
	resolve(r, func(c database.Client) { s.addCloser("database client", closeErr(c)) })
	resolve(r, func(p messagequeue.PublisherProvider) { s.addCloser("message queue publishers", closeVoid(p)) })
	resolve(r, func(c messagequeue.ConsumerProvider) { s.addCloser("message queue consumers", closeVoid(c)) })
	resolve(r, func(src secrets.SecretSource) { s.addCloser("secret source", closeErr(src)) })
	resolve(r, func(m uploads.UploadManager) { s.addCloser("upload manager", closeErr(m)) })
	resolve(r, func(l distributedlock.Locker) { s.addCloser("distributed locker", closeErr(l)) })
	resolve(r, func(l ratelimiting.RateLimiter) { s.addCloser("rate limiter", closeErr(l)) })
}

// resolveClients collects the request-path clients that hold a connection or
// buffer something. They are the last things built and the first ones released,
// because everything below them can still report, flag, or notify while it
// shuts down.
func (s *Service) resolveClients(r *resolver) {
	resolve(r, func(rep analytics.EventReporter) { s.addCloser("analytics reporter", rep.Close) })
	resolve(r, func(m featureflags.FeatureFlagManager) { s.addCloser("feature flag manager", closeErr(m)) })
	resolve(r, func(n async.AsyncNotifier) { s.addCloser("async notifier", closeErr(n)) })
}

// resolveHealth joins the application's own checks to the registry Register
// built, which by now holds a checker for every piece of infrastructure the
// config named.
//
// It happens before the servers are resolved so that nothing can bind a listener
// serving a readiness answer that is still missing half its checks — even for
// the moment between.
//
// A registry the injector cannot produce is a failure rather than a shrug: the
// checks were handed over to be answered, and a service that reports ready
// without ever running them is lying about the one thing it was asked.
func (s *Service) resolveHealth(r *resolver, checks []healthcheck.Checker) {
	if len(checks) == 0 {
		return
	}

	var joined bool

	resolve(r, func(registry healthcheck.Registry) {
		joined = true

		for _, check := range checks {
			registry.Register(check)
		}
	})

	if r.err == nil && !joined {
		r.err = platformerrors.New("joining the application's health checks: no health registry is registered")
	}
}

// resolveRunners collects the background loops in start order.
//
// Start order is dependency order, and shutdown walks it backwards, which is
// what makes each loop's last cycle mean something:
//
//   - The outbox relay is first up and therefore last down. It is the drain
//     every other loop writes into, and its final cycle has to run after the
//     last writer has stopped — which is exactly what its Run taking no context
//     was for.
//   - The jobs pool is up before the scheduler that enqueues into it, so the
//     scheduler stops producing before the pool stops consuming.
//   - The saga worker writes outbox rows, so it stops while the relay is still
//     draining.
//   - The remaining loops own their own tables and depend on nothing above
//     them, so their order among themselves is stable rather than meaningful.
//
// The audit log's retention sweep is not here. It is a retention.Policy run by
// the jobs scheduler, so it is already governed by the scheduler's place in
// this order rather than by a loop of its own.
func (s *Service) resolveRunners(r *resolver) {
	resolve(r, func(relay *outbox.Relay) { s.addRunner("outbox relay", relay) })
	resolve(r, func(p *jobs.Pool) { s.addRunner("jobs pool", p) })
	resolve(r, func(sch *jobs.Scheduler) { s.addRunner("jobs scheduler", sch) })
	resolve(r, func(w *saga.Worker) { s.addRunner("saga worker", w) })
	resolve(r, func(w *webhooks.Worker) { s.addRunner("webhooks worker", w) })
	resolve(r, func(w *dataprivacy.Worker) { s.addRunner("dataprivacy worker", w) })
}

// resolveFlushes collects the drains that have no loop of their own and have to
// happen once, on the way out, after every producer has stopped.
//
// There is only one, because the other buffered thing in this module —
// eventcapture.Recorder — drains and closes its sink inside its own Close and
// therefore rides along as a Runner. An application's single-shot drain belongs
// in a Runner's Close for the same reason.
func (s *Service) resolveFlushes(r *resolver) {
	resolve(r, func(f *metering.Flusher) {
		s.addFlush("metering flusher", func(ctx context.Context) error {
			// The pass is leased and idempotency-keyed, so a final flush that
			// races the next replica's scheduled one bills nobody twice. What
			// it buys is that usage recorded in the last minutes of a process
			// does not wait for another replica's next tick.
			_, err := f.Flush(ctx)

			return err
		})
	})
}

// resolveServers collects ingress last, so that by the time anything can accept
// a request every client and loop it will reach already exists.
func (s *Service) resolveServers(r *resolver) {
	resolve(r, func(srv httpserver.Server) { s.addServer("HTTP server", srv) })
	resolve(r, func(srv *grpcserver.Server) { s.addServer("gRPC server", srv) })
}

func (s *Service) addCloser(name string, release func(context.Context) error) {
	s.closers = append(s.closers, named[func(context.Context) error]{name: name, v: release})
}

func (s *Service) addFlush(name string, flush func(context.Context) error) {
	s.flushes = append(s.flushes, named[func(context.Context) error]{name: name, v: flush})
}

func (s *Service) addRunner(name string, runner Runner) {
	s.runners = append(s.runners, named[Runner]{name: name, v: runner})
}

func (s *Service) addServer(name string, server Server) {
	s.servers = append(s.servers, named[Server]{name: name, v: server})
}

// closeErr and closeVoid adapt the two Close shapes this module's clients have
// to the one shape shutdown runs. Neither takes the context, which is honest:
// releasing a pool or a bucket handle is not cancellable, and pretending
// otherwise would suggest the shutdown budget bounds it.
func closeErr(c interface{ Close() error }) func(context.Context) error {
	return func(context.Context) error { return c.Close() }
}

func closeVoid(c interface{ Close() }) func(context.Context) error {
	return func(context.Context) error {
		c.Close()

		return nil
	}
}
