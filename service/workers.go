package service

import (
	"context"
	"sync"

	"github.com/primandproper/platform-go/v14/searchsync"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"
)

// ErrScheduledJobsWithoutScheduler is what New reports for an application that
// provided a []jobs.Job and configured no scheduler to run them. Those jobs
// would otherwise be resolved, held and never fired, which is a reaper nobody
// notices is missing until the table it was meant to prune fills up.
var ErrScheduledJobsWithoutScheduler = platformerrors.New(
	"scheduled jobs were provided but no job scheduler is registered to run them",
)

// starter is a background loop whose start can fail.
//
// Runner.Run reports nothing, which is right for a loop that has nothing to
// set up, and wrong for one that subscribes to a broker on its way up: a
// subscription that cannot be established is a startup failure, and Run has
// nowhere to put it. A Runner that is also a starter is started by
// Service.Run, which takes the service down with the error rather than serving
// without the loop.
type starter interface {
	Start(ctx context.Context) error
}

// poolGroupRunner joins a *jobs.PoolGroup to the runner slot.
//
// A group is not a Runner, and says why: its Start reports whether every pool
// came up, which a Run returning nothing cannot. So this adapter is a starter
// as well, and Service.Run calls Start and reports its failure. Its Run only
// blocks until Close, which is what Runner promises — the pools consume on
// goroutines the group owns, so there is no loop here for Run to be.
//
// It is closed from the runner slot rather than as a flush because the group is
// a consumer with producers upstream of it: the scheduler that enqueues into it
// closes first, and the outbox relay that publishes into it closes after, the
// same place the single *jobs.Pool has always held.
type poolGroupRunner struct {
	group *jobs.PoolGroup
	stop  chan struct{}
	once  sync.Once
}

var (
	_ Runner  = (*poolGroupRunner)(nil)
	_ starter = (*poolGroupRunner)(nil)
)

func newPoolGroupRunner(group *jobs.PoolGroup) *poolGroupRunner {
	return &poolGroupRunner{group: group, stop: make(chan struct{})}
}

// Start builds and starts every pool in the group, or none of them.
func (r *poolGroupRunner) Start(ctx context.Context) error {
	return r.group.Start(ctx)
}

// Run blocks until Close.
func (r *poolGroupRunner) Run() {
	<-r.stop
}

// Close stops every pool and waits for their in-flight handlers. It is safe
// before Start — PoolGroup.Close is — so a service that failed before its loops
// started spends none of its shutdown budget here.
func (r *poolGroupRunner) Close(ctx context.Context) error {
	r.once.Do(func() { close(r.stop) })

	return r.group.Close(ctx)
}

// resolvePoolGroup joins the application's *jobs.PoolGroup, when it registered
// one.
//
// No config names a group: its specs are handlers, and a handler is not
// something an environment variable can supply. searchsync.Registry.PoolSpecs is
// the standing source of them, which is why the registry and the group arrive
// together in a worker process.
func (s *Service) resolvePoolGroup(r *resolver) {
	resolve(r, func(g *jobs.PoolGroup) { s.addRunner("jobs pool group", newPoolGroupRunner(g)) })
}

// resolveScheduler joins the jobs scheduler and hands it the application's
// jobs.
//
// Scheduler.Register must run before Scheduler.Run, and Service.Run is what
// calls Run, so New is the one place between the two that this package owns.
// The jobs arrive as a []jobs.Job the application provides — one provider, the
// application's, since samber/do refuses a second provider for one type by
// panicking. A job the platform schedules for itself joins that same Register
// call after the application's list is resolved, rather than through a
// provider of its own.
//
// The whole list is registered in one call, so a duplicate name or an invalid
// job refuses all of them and fails New, rather than leaving a schedule that is
// partly what the application asked for.
func (s *Service) resolveScheduler(r *resolver) {
	var scheduler *jobs.Scheduler

	resolve(r, func(sch *jobs.Scheduler) {
		scheduler = sch
		s.addRunner("jobs scheduler", sch)
	})

	resolve(r, func(scheduled []jobs.Job) {
		if len(scheduled) == 0 {
			return
		}

		if scheduler == nil {
			r.err = ErrScheduledJobsWithoutScheduler

			return
		}

		if err := scheduler.Register(scheduled...); err != nil {
			r.err = platformerrors.Wrap(err, "registering the scheduled jobs")
		}
	})
}

// resolveSearchIndexing joins the application's *searchsync.Registry as a
// flush.
//
// The registry owns each index's stamp buffer, and a buffer closed while the
// pools are still indexing drops the stamps they produce afterwards — which
// leaves last_indexed_at saying those documents were never indexed. The flush
// slot runs after every runner has closed, the pool group among them, and
// before the database client the stamps are written through is released, which
// is the one window that is right on both ends.
func (s *Service) resolveSearchIndexing(r *resolver) {
	resolve(r, func(reg *searchsync.Registry) { s.addFlush("search index registry", reg.Close) })
}
