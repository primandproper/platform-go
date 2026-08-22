package jobs

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/panicking"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// schedulerServiceName names the Scheduler's spans, logger, and metrics.
	schedulerServiceName = "jobs_scheduler"

	// DefaultLockKeyPrefix namespaces the scheduler's lock keys, so a job named
	// "reconcile" cannot collide with an unrelated lock of the same name.
	DefaultLockKeyPrefix = "jobs.scheduler."
	// DefaultLeaseTTL is how long a run's lock is held when neither the job nor
	// the config names a TTL.
	DefaultLeaseTTL = time.Minute
	// DefaultTimezone is the zone cron schedules are read in when nothing else
	// names one. It resolves without the zoneinfo database, which the images
	// this runs in may not carry.
	DefaultTimezone = "UTC"
)

// Observability keys for the Scheduler's spans and log fields.
const (
	jobNameKey     = "jobs.name"
	jobIntervalKey = "jobs.interval"
	jobScheduleKey = "jobs.schedule"
	jobRanKey      = "jobs.ran"
	jobOverranKey  = "jobs.overran_interval"
)

// Job is one piece of periodic work. It is registered before the Scheduler runs
// and executed on its own goroutine thereafter.
type Job struct {
	// Run is the work. It is given a context that carries the run's span and is
	// canceled at Timeout; a job that ignores it cannot be interrupted, and
	// will hold its lease past expiry.
	Run func(ctx context.Context) error
	// Schedule fires the job on a calendar rather than a stopwatch, for work
	// that belongs at an hour rather than at a frequency. Exactly one of
	// Schedule and Interval must be set.
	//
	//	Schedule: jobs.MustCron("0 3 * * *")
	//
	// Every replica computes the same fire times from the same expression, so
	// unlike Interval — whose ticker is phased by whenever each replica started
	// — the fleet contends for the lease at the same instant and the lease only
	// has to cover the run plus the clock skew between replicas.
	//
	// A cron schedule that named no zone of its own takes the Scheduler's
	// SchedulerConfig.Timezone when it is registered, and UTC failing that.
	Schedule Schedule
	// Name identifies the job in telemetry and forms its lock key. It must be
	// unique within a Scheduler and stable across deploys — renaming a job lets
	// an old replica and a new one run it concurrently during a rollout.
	Name string
	// Interval is how often the job fires. The first fire is one interval after
	// Run unless RunOnStart is set. Exactly one of Interval and Schedule must
	// be set.
	Interval time.Duration
	// Timeout bounds one execution. Zero falls back to the Scheduler's
	// DefaultTimeout, and zero there means no timeout.
	Timeout time.Duration
	// LeaseTTL is how long the run's lock is held. Zero falls back to the
	// Scheduler's DefaultLeaseTTL. It must comfortably exceed the job's
	// worst-case duration: the lease is not renewed while the job runs, so an
	// overrun lets a second replica start the same job.
	LeaseTTL time.Duration
	// RunOnStart fires the job once when the Scheduler starts, instead of
	// waiting a full interval or for the schedule's next fire time. Useful for
	// work that should not be skipped by a deploy — a six-hour job on a service
	// that deploys every four hours otherwise never runs, and neither does a
	// nightly one on a service that never happens to be up at 03:00.
	RunOnStart bool
}

// validate reports whether the job can be scheduled at all. now anchors the
// check that a schedule has any fire time left in it: an expression like
// "0 0 30 2 *" parses cleanly and never comes true, and the difference between
// rejecting that at startup and accepting it is a job that silently never runs.
func (j *Job) validate(now time.Time) error {
	switch {
	case j.Name == "":
		return platformerrors.Wrap(ErrInvalidJob, "empty job name")
	case j.Run == nil:
		return platformerrors.Wrapf(ErrInvalidJob, "job %q has no function", j.Name)
	case j.Schedule != nil && j.Interval > 0:
		return platformerrors.Wrapf(ErrInvalidJob, "job %q sets both an interval and a schedule", j.Name)
	case j.Schedule == nil && j.Interval <= 0:
		return platformerrors.Wrapf(ErrInvalidJob, "job %q has a non-positive interval", j.Name)
	}

	if j.Schedule != nil && j.Schedule.Next(now).IsZero() {
		return platformerrors.Wrapf(ErrInvalidJob, "job %q has a schedule that will never fire", j.Name)
	}

	return nil
}

// Scheduler runs registered jobs on an interval or on a calendar, each
// execution held under a distributed lock so that at most one replica runs a
// given job per tick.
//
// At most one, not exactly one, and both halves matter. A tick whose lock is
// already held is skipped rather than queued, so a job does not run on every
// replica — and it also does not run at all if the holder is still working. The
// lock is not renewed for the duration of a run either, so a job that outlives
// its lease loses exclusivity while still executing and a second replica may
// start it. Job.LeaseTTL is where that is sized; a job whose worst case
// approaches its lease has no mutual exclusion left to rely on.
type Scheduler struct {
	locker distributedlock.Locker
	clock  clock.Clock
	o11y   observability.Observer

	// location is cfg.Timezone resolved once, and is handed to the schedules of
	// registered jobs that did not settle their own zone.
	location *time.Location

	jobs map[string]*Job

	stop chan struct{}
	done chan struct{}

	runCounter          metrics.Int64Counter
	failureCounter      metrics.Int64Counter
	skippedCounter      metrics.Int64Counter
	panicCounter        metrics.Int64Counter
	lockErrCounter      metrics.Int64Counter
	leaseExpiredCounter metrics.Int64Counter
	overrunCounter      metrics.Int64Counter
	latencyHist         metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this scheduler actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg SchedulerConfig

	wg       sync.WaitGroup
	mu       sync.Mutex
	stopOnce sync.Once
	running  bool
}

// NewScheduler builds a Scheduler. It registers no jobs and starts nothing;
// call Register then Run.
//
// The locker is required rather than optional, because the difference between a
// job running once across the fleet and once per replica is invisible until
// something double-charges a customer. Single-replica deployments and tests
// pass distributedlock/memory, which is a real lock within one process;
// distributedlock/noop opts out entirely and lets every replica run every job.
//
// ctx is used to validate the config and is not retained — Run takes its own.
func NewScheduler(ctx context.Context, cfg *SchedulerConfig, locker distributedlock.Locker, opts ...SchedulerOption) (*Scheduler, error) {
	if cfg == nil {
		return nil, platformerrors.New("nil job scheduler config provided")
	}
	if locker == nil {
		return nil, ErrNilLocker
	}

	cfg.EnsureDefaults()

	s := &Scheduler{
		cfg:    *cfg,
		locker: locker,
		clock:  clock.NewClock(),
		jobs:   map[string]*Job{},
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := s.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating job scheduler config")
	}

	// Resolved here rather than at the first fire, because a zone name the
	// runtime cannot load is a deployment problem — usually an image without
	// the zoneinfo database — and startup is when that is still cheap to fix.
	location, locErr := time.LoadLocation(s.cfg.Timezone)
	if locErr != nil {
		return nil, platformerrors.Wrapf(locErr, "loading job scheduler timezone %q", s.cfg.Timezone)
	}
	s.location = location

	s.o11y = observability.NewObserver(schedulerServiceName, s.logger, s.tracerProvider)

	if err := s.buildInstruments(); err != nil {
		return nil, err
	}

	return s, nil
}

// buildInstruments creates the Scheduler's metrics up front, so a misconfigured
// meter fails the constructor rather than the first tick.
func (s *Scheduler) buildInstruments() error {
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	var err error
	if s.runCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_runs", schedulerServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating runs counter")
	}
	if s.failureCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_failures", schedulerServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating failures counter")
	}
	if s.skippedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_skipped", schedulerServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating skipped counter")
	}
	if s.panicCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_panics", schedulerServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating panics counter")
	}
	if s.lockErrCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_lock_errors", schedulerServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating lock errors counter")
	}
	if s.leaseExpiredCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_leases_expired", schedulerServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating leases expired counter")
	}
	if s.overrunCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_overruns", schedulerServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating overruns counter")
	}
	if s.latencyHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_run_latency_ms", schedulerServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating run latency histogram")
	}

	return nil
}

// Register adds jobs to the Scheduler. It must be called before Run, and
// rejects the whole batch if any job is invalid or duplicates a name already
// registered — a partially applied schedule is harder to reason about than a
// rejected one.
func (s *Scheduler) Register(jobs ...Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return ErrSchedulerRunning
	}

	now := s.clock.Now()

	// Staged rather than applied in place, so a batch containing one bad job
	// leaves the schedule exactly as it was.
	staged := make(map[string]*Job, len(jobs))
	for i := range jobs {
		// Copied out of the variadic slice, which the caller still owns and may
		// go on to mutate.
		job := jobs[i]

		// The last chance to settle a cron schedule's zone, and the reason the
		// copy above matters: the caller's Job is left as they wrote it, and
		// one Schedule value may be registered with two Schedulers configured
		// for different zones without either seeing the other's.
		if schedule, ok := job.Schedule.(cronSchedule); ok {
			job.Schedule = schedule.relocate(s.location, false)
		}

		if err := job.validate(now); err != nil {
			return err
		}

		if _, taken := s.jobs[job.Name]; taken {
			return platformerrors.Wrapf(ErrDuplicateJob, "job %q", job.Name)
		}
		if _, taken := staged[job.Name]; taken {
			return platformerrors.Wrapf(ErrDuplicateJob, "job %q", job.Name)
		}

		staged[job.Name] = &job
	}

	maps.Copy(s.jobs, staged)

	return nil
}

// Run starts a goroutine per registered job and blocks until Close.
//
// Like Pool.Run it takes no context: a scheduler tied to a request- or
// server-scoped context stops mid-job when that context is canceled, leaving
// the lease held until it expires. The owner ends it explicitly with Close.
func (s *Scheduler) Run() {
	defer close(s.done)

	s.mu.Lock()
	s.running = true
	scheduled := slices.Collect(maps.Values(s.jobs))
	s.mu.Unlock()

	ctx := context.Background()

	s.wg.Add(len(scheduled))
	for _, job := range scheduled {
		go s.runJob(ctx, job)
	}

	s.o11y.Logger().WithValue("job_count", len(scheduled)).Info("job scheduler started")

	<-s.stop
	s.wg.Wait()
}

// Close stops the Scheduler, waits for the in-flight runs to finish, and
// returns. Safe to call more than once, and only meaningful after Run — there
// is nothing to stop before it, so Close would wait out its whole context.
//
// If ctx expires first the error is returned without waiting, so a job still
// running holds its lease until it finishes or the lease expires.
func (s *Scheduler) Close(ctx context.Context) error {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	s.stopOnce.Do(func() { close(s.stop) })

	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return op.Error(ctx.Err(), "waiting for job scheduler to stop")
	}
}

// runJob owns one job's cadence for the life of the Scheduler, on a ticker or
// on a calendar depending on how the job was registered.
func (s *Scheduler) runJob(ctx context.Context, job *Job) {
	defer s.wg.Done()

	if job.Schedule != nil {
		s.runScheduled(ctx, job)

		return
	}

	s.runPeriodic(ctx, job)
}

// runPeriodic owns one interval job's ticker.
//
// Ticks are not queued. clock.Ticker coalesces them the way *time.Ticker does,
// so a job that overruns its interval simply fires again as soon as it
// finishes, rather than accumulating a backlog it can never work off — which is
// the behavior a periodic job wants and the reason overruns are counted rather
// than compensated for.
func (s *Scheduler) runPeriodic(ctx context.Context, job *Job) {
	// Started before the RunOnStart execution rather than after it, so the
	// first interval is measured from the Scheduler starting rather than from
	// whenever that execution happened to finish.
	ticker := s.clock.NewTicker(job.Interval)
	defer ticker.Stop()

	if job.RunOnStart && !s.startTick(ctx, job, job.Interval) {
		return
	}

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.Chan():
			s.tick(ctx, job, job.Interval)
		}

		// Rechecked after the run, so a job whose execution outlasted the stop
		// signal does not start another one.
		select {
		case <-s.stop:
			return
		default:
		}
	}
}

// runScheduled owns one calendar job's goroutine.
//
// The next fire time is recomputed from the clock after every run rather than
// accumulated, so fire times that passed while a run was in progress are
// skipped rather than queued — the same coalescing the interval path gets from
// its ticker, and the reason a job that outruns its schedule is counted as an
// overrun instead of being compensated for.
func (s *Scheduler) runScheduled(ctx context.Context, job *Job) {
	if job.RunOnStart && !s.startTick(ctx, job, job.window(s.clock.Now())) {
		return
	}

	for {
		now := s.clock.Now()

		next := job.Schedule.Next(now)
		if next.IsZero() {
			// Register rejects a schedule that is already exhausted, so this is
			// a caller's Schedule retiring itself mid-flight. There is nothing
			// left to wait for and the goroutine has to end somewhere; it is
			// logged because the job going quiet is otherwise indistinguishable
			// from the job never being due.
			s.o11y.Logger().WithValue(jobNameKey, job.Name).
				WithValue(jobScheduleKey, describeSchedule(job.Schedule)).
				Error("job will not fire again and is no longer scheduled", nil)

			return
		}

		if !s.waitUntil(next.Sub(now)) {
			return
		}

		s.tick(ctx, job, job.window(next))
	}
}

// startTick performs the RunOnStart execution and reports whether the job
// should carry on. Guarded, so a Scheduler that was closed before its
// goroutines were scheduled does not fire a round of jobs on its way out.
func (s *Scheduler) startTick(ctx context.Context, job *Job, window time.Duration) bool {
	select {
	case <-s.stop:
		return false
	default:
		s.tick(ctx, job, window)

		return true
	}
}

// waitUntil blocks for d, and reports whether the wait finished rather than
// being cut short by the Scheduler stopping.
//
// A one-shot ticker rather than a timer, because clock.Clock exposes NewTicker
// and not NewTimer: taking a single tick costs one receive and keeps both kinds
// of job waiting on the same seam, so a test that injects a clock drives a
// calendar job exactly the way it drives an interval one.
func (s *Scheduler) waitUntil(d time.Duration) bool {
	if d <= 0 {
		// Schedule.Next is documented to return a time strictly in the future,
		// so this is a caller's implementation not doing that. It means "now",
		// which is the one duration NewTicker panics on.
		select {
		case <-s.stop:
			return false
		default:
			return true
		}
	}

	ticker := s.clock.NewTicker(d)
	defer ticker.Stop()

	select {
	case <-s.stop:
		return false
	case <-ticker.Chan():
		return true
	}
}

// window is how long a calendar job has from t before it is next due. A run
// that outlasts it has overrun — the interval path's equivalent is simply the
// interval, but a calendar's headroom is not constant: a job at "0 9 * * 1-5"
// has three days of it on Friday night and one on Monday.
//
// Zero means there is no next fire to be late for, and suppresses the check.
func (j *Job) window(t time.Time) time.Duration {
	next := j.Schedule.Next(t)
	if next.IsZero() {
		return 0
	}

	return next.Sub(t)
}

// tick attempts one run, under the job's lease.
//
// Acquire is used directly rather than distributedlock.ScopedLocker because the
// TTL belongs to the job rather than to the Scheduler: a nightly compaction and
// a one-minute health sweep want very different leases, and a ScopedLocker
// carries a single TTL for every key it serves.
//
// A contended lock is not an error. It means another replica is running this
// job right now, which is the whole point — the tick is skipped and counted.
func (s *Scheduler) tick(ctx context.Context, job *Job, window time.Duration) {
	key := s.cfg.LockKeyPrefix + job.Name
	ttl := job.LeaseTTL
	if ttl <= 0 {
		ttl = s.cfg.DefaultLeaseTTL
	}

	ctx, op := s.o11y.BeginCustom(ctx, "scheduled_job")
	defer op.End()

	op.SetValues(map[string]any{
		jobNameKey:      job.Name,
		jobIntervalKey:  window,
		keys.LockKeyKey: key,
		keys.LockTTLKey: ttl,
	})

	if job.Schedule != nil {
		// The expression as written, so a trace answers "when is this supposed
		// to run" without the reader deriving it from the gap to the next fire.
		op.Set(jobScheduleKey, describeSchedule(job.Schedule))
	}

	attrs := metric.WithAttributes(attribute.String(jobNameKey, job.Name))

	lock, err := s.locker.Acquire(ctx, key, ttl)
	if err != nil {
		if stderrors.Is(err, distributedlock.ErrLockNotAcquired) {
			s.skippedCounter.Add(ctx, 1, attrs)
			op.Set(jobRanKey, false)

			return
		}

		s.lockErrCounter.Add(ctx, 1, attrs)
		op.Acknowledge(err, "acquiring lease for job %q", job.Name)

		return
	}

	op.Set(jobRanKey, true)

	// Released on a context that cannot be canceled, so a Scheduler shutting
	// down cannot strand the lease until its TTL expires.
	defer func() {
		releaseErr := lock.Release(context.WithoutCancel(ctx))
		if releaseErr == nil {
			return
		}

		if stderrors.Is(releaseErr, distributedlock.ErrLockNotHeld) {
			// The lease expired while the job was still running, so another
			// replica may have started the same job. This is the failure the
			// package is most likely to actually meet, and the signal that a
			// job has outgrown its LeaseTTL.
			s.leaseExpiredCounter.Add(ctx, 1, attrs)
			op.Acknowledge(releaseErr, "lease for job %q expired before it finished: it may have run twice", job.Name)

			return
		}

		s.lockErrCounter.Add(ctx, 1, attrs)
		op.Acknowledge(releaseErr, "releasing lease for job %q", job.Name)
	}()

	s.execute(ctx, op, job, attrs, window)
}

// execute runs the job once and records the outcome. A failure is not retried:
// the next tick is the retry, and a job that cannot tolerate waiting one
// interval wants a shorter interval rather than an inner retry loop.
//
// The run gets a span of its own, nested inside the tick's. The tick span
// covers acquire-run-release, so without this split a job that is slow and a
// lock backend that is slow look identical on a trace — and they call for
// opposite responses.
func (s *Scheduler) execute(ctx context.Context, op observability.Operation, job *Job, attrs metric.MeasurementOption, window time.Duration) {
	s.runCounter.Add(ctx, 1, attrs)

	runCtx, runOp := s.o11y.BeginCustom(ctx, "run_job")
	runOp.Set(jobNameKey, job.Name)

	startTime := s.clock.Now()

	err := s.invoke(runCtx, runOp, job, attrs)

	runOp.End()

	elapsed := s.clock.Since(startTime)
	s.latencyHist.Record(ctx, float64(elapsed.Milliseconds()), attrs)

	if window > 0 && elapsed > window {
		s.overrunCounter.Add(ctx, 1, attrs)
		op.Set(jobOverranKey, true).Logger().
			WithValue("elapsed", elapsed).Info("job outran its interval")
	}

	if err != nil {
		s.failureCounter.Add(ctx, 1, attrs)
		op.Acknowledge(err, "running job %q", job.Name)
	}
}

// invoke runs the job's function once, containing a panic. Left uncontained a
// panic would unwind runJob's goroutine and stop that job permanently, while
// every other job kept ticking — a failure that looks like "the nightly report
// stopped arriving" months later rather than a crash now.
func (s *Scheduler) invoke(ctx context.Context, op observability.Operation, job *Job, attrs metric.MeasurementOption) (err error) {
	defer func() {
		err = containedPanic(ctx, op, err, s.panicCounter, attrs, ErrJobPanicked)
	}()

	timeout := job.Timeout
	if timeout <= 0 {
		timeout = s.cfg.DefaultTimeout
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	return panicking.Contain(func() error { return job.Run(ctx) })
}
