package service

import (
	"context"
	"slices"
	"sync"

	oauth2serverstorecfg "github.com/primandproper/platform-go/v15/authentication/oauth2serverstore/config"
	passwordresetcfg "github.com/primandproper/platform-go/v15/authentication/passwordreset/config"
	signincfg "github.com/primandproper/platform-go/v15/authentication/signin/config"
	operationscfg "github.com/primandproper/platform-go/v15/operations/config"
	sagacfg "github.com/primandproper/platform-go/v15/saga/config"
	"github.com/primandproper/platform-go/v15/searchsync"
	sessionscfg "github.com/primandproper/platform-go/v15/sessions/config"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"

	"github.com/samber/do/v2"
)

// ErrScheduledJobsWithoutScheduler is what New reports for a service with jobs
// to schedule and no scheduler to run them: a []jobs.Job the application
// provided, or a reaper of this module's own that has no other way to run.
// Those jobs would otherwise be resolved, held and never fired, which is a
// reaper nobody notices is missing until the table it was meant to prune fills
// up — or, for operations' recovery, until an operation whose process died sits
// pending forever.
var ErrScheduledJobsWithoutScheduler = platformerrors.New(
	"scheduled jobs were provided but no job scheduler is registered to run them",
)

// platformJobSet is one config package's jobs, registered under its own key.
type platformJobSet struct {
	key string

	// fallback is set for a set whose store sweeps itself in-process when no
	// scheduler runs it, so a service without a scheduler skips it rather than
	// refusing to start: the table is still swept, once per replica rather
	// than once per fleet. A set without one has no other way to run.
	fallback bool
}

// platformJobs are the reapers this module's own stores schedule for
// themselves, each registered by its config package's RegisterJobs under a key
// of its own — the application's []jobs.Job is one provider, and do refuses a
// second for one type.
//
// It is a roster rather than a scan of whatever the injector holds because the
// two kinds of set are treated differently without a scheduler, and that is a
// fact about each store rather than something its registration can say. A key
// nobody registered is an absence: the subsystem was not configured.
var platformJobs = []platformJobSet{
	{key: operationscfg.JobsKey},
	{key: sagacfg.JobsKey},
	{key: passwordresetcfg.JobsKey, fallback: true},
	{key: oauth2serverstorecfg.JobsKey, fallback: true},
	{key: signincfg.JobsKey, fallback: true},
	{key: sessionscfg.JobsKey, fallback: true},
}

// poolGroupRunner joins a *jobs.PoolGroup to the runner slot.
//
// A group is not a Runner, and says why: its Start reports whether every pool
// came up, which a Run returning nothing cannot. So Service holds this adapter
// by name as well as in its runners, and Service.Run calls Start and reports
// its failure — a subscription the broker refuses is a startup failure, not a
// worker that drains nothing. Its Run only blocks until Close, which is what
// Runner promises — the pools consume on goroutines the group owns, so there
// is no loop here for Run to be.
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

var _ Runner = (*poolGroupRunner)(nil)

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
	resolve(r, func(g *jobs.PoolGroup) {
		s.poolGroup = newPoolGroupRunner(g)
		s.addRunner("jobs pool group", s.poolGroup)
	})
}

// resolveScheduler joins the jobs scheduler and hands it the application's
// jobs and the platform's own.
//
// Scheduler.Register must run before Scheduler.Run, and Service.Run is what
// calls Run, so New is the one place between the two that this package owns.
// The application's jobs arrive as a []jobs.Job it provides — one provider, the
// application's, since samber/do refuses a second provider for one type by
// panicking. The platform's arrive under the named keys platformJobs lists,
// which is how a reaper a store owns gets scheduled without the application
// having remembered to: operations' recovery and reap, saga's retention, and
// the token tables' sweeps are each on unless their config says Disabled.
//
// The whole list is registered in one call, so a duplicate name or an invalid
// job refuses all of them and fails New, rather than leaving a schedule that is
// partly what the application asked for.
func (s *Service) resolveScheduler(r *resolver) {
	var (
		scheduler *jobs.Scheduler
		scheduled []jobs.Job
	)

	resolve(r, func(sch *jobs.Scheduler) {
		scheduler = sch
		s.addRunner("jobs scheduler", sch)
	})

	resolve(r, func(application []jobs.Job) { scheduled = append(scheduled, application...) })

	scheduled = append(scheduled, resolvePlatformJobs(r, scheduler != nil)...)

	if r.err != nil || len(scheduled) == 0 {
		return
	}

	if scheduler == nil {
		r.err = ErrScheduledJobsWithoutScheduler

		return
	}

	if err := scheduler.Register(scheduled...); err != nil {
		r.err = platformerrors.Wrap(err, "registering the scheduled jobs")
	}
}

// resolvePlatformJobs builds every platform job set that was registered.
//
// Without a scheduler, a set whose store sweeps itself in-process is not built
// at all, and one that has no other way to run is — so that resolveScheduler
// reports it rather than a service that starts without its reaper.
func resolvePlatformJobs(r *resolver, haveScheduler bool) []jobs.Job {
	var built []jobs.Job

	for _, set := range platformJobs {
		if r.err != nil {
			return nil
		}

		if (set.fallback && !haveScheduler) || !namedRegistered(r.i, set.key) {
			continue
		}

		rendered, err := do.InvokeNamed[[]jobs.Job](r.i, set.key)
		if err != nil {
			r.err = platformerrors.Wrapf(err, "invoking %s", set.key)

			return nil
		}

		built = append(built, rendered...)
	}

	return built
}

// namedRegistered reports whether anything is registered under key.
//
// It is operationscfg.QueueRegistered's check, for the same reason: resolve
// draws the absent-versus-failed distinction through injection.InvokeOptional,
// which resolves strictly by do.NameOf, and a named registration needs
// presence asked for directly.
func namedRegistered(i do.Injector, key string) bool {
	return slices.ContainsFunc(i.ListProvidedServices(), func(d do.ServiceDescription) bool {
		return d.Service == key
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
