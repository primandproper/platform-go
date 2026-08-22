package dataprivacy

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/uploads"
)

// DefaultSweepJobName is the name the Sweeper's jobs.Job carries.
//
// It is a constant because a job's name is its lock key: two replicas that
// disagree about it both run the sweep, and the sweep deletes things.
const DefaultSweepJobName = "dataprivacy-sweep"

// SweepResult is what one pass did.
type SweepResult struct {
	// Overdue is how many unfulfilled requests are past their deadline, by
	// type. Reported whether or not anything was swept — it is the number an
	// operator actually needs, and a sweep that found nothing to delete is
	// exactly when nobody would otherwise look.
	Overdue map[RequestType]int64
	// ArtifactsExpired is how many artifacts were deleted from storage.
	ArtifactsExpired int64
	// ErasuresLapsed is how many unconfirmed erasures were cancelled.
	ErasuresLapsed int64
	// RecordsReaped is how many terminal request records were deleted.
	RecordsReaped int64
}

// Sweeper runs the three background chores this package needs: deleting
// expired artifacts, cancelling erasures nobody confirmed, and reaping request
// records past retention. It also samples the overdue gauge.
//
// The artifact expiry is the one that matters. Everything else here is
// housekeeping; that one is the difference between an export being a temporary
// artifact and being a permanent object in a bucket containing everything an
// application knows about a person. A deployment that fulfills requests and
// does not run the Sweeper accumulates those forever, which is why this is a
// separate, named, schedulable thing rather than a flag on the Fulfiller.
type Sweeper struct {
	store    Store
	clock    clock.Clock
	o11y     observability.Observer
	uploader uploads.UploadManager

	expiredCounter  metrics.Int64Counter
	lapsedCounter   metrics.Int64Counter
	reapedCounter   metrics.Int64Counter
	deleteErrCtr    metrics.Int64Counter
	overdueGauge    metrics.Int64Gauge
	sweepLatencyHst metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this sweeper actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg SweeperConfig
}

// NewSweeper builds a Sweeper. It does not schedule it; see Job.
//
// ctx is used to validate the config and is not retained.
func NewSweeper(ctx context.Context, cfg *SweeperConfig, store Store, opts ...SweeperOption) (*Sweeper, error) {
	if cfg == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy sweeper config")
	}

	if store == nil {
		return nil, ErrNilStore
	}

	cfg.EnsureDefaults()

	s := &Sweeper{
		cfg:   *cfg,
		store: store,
		clock: clock.NewClock(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := s.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating dataprivacy sweeper config")
	}

	s.o11y = observability.NewObserver(serviceName, s.logger, s.tracerProvider)

	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	var err error
	if s.expiredCounter, err = mp.NewInt64Counter(serviceName + "_artifacts_expired"); err != nil {
		return nil, platformerrors.Wrap(err, "creating artifacts expired counter")
	}
	if s.lapsedCounter, err = mp.NewInt64Counter(serviceName + "_erasures_lapsed"); err != nil {
		return nil, platformerrors.Wrap(err, "creating erasures lapsed counter")
	}
	if s.reapedCounter, err = mp.NewInt64Counter(serviceName + "_requests_reaped"); err != nil {
		return nil, platformerrors.Wrap(err, "creating requests reaped counter")
	}
	if s.deleteErrCtr, err = mp.NewInt64Counter(serviceName + "_artifact_delete_errors"); err != nil {
		return nil, platformerrors.Wrap(err, "creating artifact delete error counter")
	}
	if s.overdueGauge, err = mp.NewInt64Gauge(serviceName + "_requests_overdue"); err != nil {
		return nil, platformerrors.Wrap(err, "creating requests overdue gauge")
	}
	if s.sweepLatencyHst, err = mp.NewFloat64Histogram(serviceName + "_sweep_latency_ms"); err != nil {
		return nil, platformerrors.Wrap(err, "creating sweep latency histogram")
	}

	return s, nil
}

// Job renders the Sweeper as a jobs.Job, for registration with a
// jobs.Scheduler.
//
// Scheduling it there rather than running a ticker of its own is what makes the
// sweep run once across a fleet instead of once per replica. Ten replicas each
// deleting the same artifacts is mostly harmless; ten replicas each reaping the
// same rows is ten times the lock contention on a table people are still
// reading.
//
// LeaseTTL must comfortably exceed one sweep. The scheduler does not renew a
// lease while a job runs, so a sweep that outlives its lease loses exclusivity
// halfway through — see jobs.Job.LeaseTTL.
func (s *Sweeper) Job(schedule jobs.Schedule, leaseTTL time.Duration) jobs.Job {
	return jobs.Job{
		Name:     DefaultSweepJobName,
		Schedule: schedule,
		LeaseTTL: leaseTTL,
		Run: func(ctx context.Context) error {
			_, err := s.Sweep(ctx)

			return err
		},
	}
}

// Sweep runs one pass: lapse, expire, reap, and sample.
//
// The four run in that order and independently. An error in one is recorded and
// the rest still run — they are unrelated chores sharing a schedule, and a
// storage provider being unreachable is not a reason to skip the retention reap
// as well.
func (s *Sweeper) Sweep(ctx context.Context) (*SweepResult, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	defer op.Time(ctx, s.clock, s.sweepLatencyHst)()

	now := s.clock.Now().UTC()

	result := &SweepResult{}

	var errs []error

	lapsed, err := s.store.LapseUnconfirmed(ctx, now, s.cfg.BatchSize)
	if err != nil {
		errs = append(errs, platformerrors.Wrap(err, "lapsing unconfirmed dataprivacy erasures"))
	} else if lapsed > 0 {
		result.ErasuresLapsed = lapsed
		s.lapsedCounter.Add(ctx, lapsed)
	}

	if result.ArtifactsExpired, err = s.expireArtifacts(ctx, now); err != nil {
		errs = append(errs, err)
	}

	if !s.cfg.DisableReap {
		reaped, reapErr := s.store.Reap(ctx, now.Add(-s.cfg.RequestRetention), s.cfg.BatchSize)
		if reapErr != nil {
			errs = append(errs, platformerrors.Wrap(reapErr, "reaping dataprivacy requests"))
		} else if reaped > 0 {
			result.RecordsReaped = reaped
			s.reapedCounter.Add(ctx, reaped)
		}
	}

	if result.Overdue, err = s.sampleOverdue(ctx, now); err != nil {
		errs = append(errs, err)
	}

	op.SetValues(map[string]any{
		expiredKey: result.ArtifactsExpired,
		sweptKey:   result.ErasuresLapsed + result.RecordsReaped,
		overdueKey: result.Overdue[RequestExport] + result.Overdue[RequestErasure],
	})

	if len(errs) > 0 {
		return result, op.Error(platformerrors.Join(errs...), "sweeping dataprivacy requests")
	}

	return result, nil
}

// expireArtifacts deletes artifacts whose window has closed, one at a time.
//
// The object is deleted before the row is marked, never the other way round. A
// row marked expired against a surviving object is a file nobody is looking for
// any more and nobody will delete — the reference is cleared when the status
// changes, so the next sweep cannot even find it.
func (s *Sweeper) expireArtifacts(ctx context.Context, now time.Time) (int64, error) {
	if s.uploader == nil {
		// Refused rather than done badly. See WithSweeperUploadManager.
		return 0, nil
	}

	due, err := s.store.ExpiringArtifacts(ctx, now, s.cfg.BatchSize)
	if err != nil {
		return 0, platformerrors.Wrap(err, "selecting expiring dataprivacy artifacts")
	}

	var expired int64

	for _, req := range due {
		if deleteErr := s.deleteArtifact(ctx, req); deleteErr != nil {
			s.deleteErrCtr.Add(ctx, 1)
			s.o11y.Logger().WithValues(map[string]any{
				requestIDKey:   req.ID,
				artifactRefKey: req.ArtifactRef,
			}).Error("deleting expired dataprivacy artifact", deleteErr)

			// Left for the next sweep. Marking it expired now would clear the
			// only reference to an object that still exists.
			continue
		}

		if markErr := s.store.MarkExpired(ctx, req.ID, now); markErr != nil {
			// The object is gone and the row still points at it. The next sweep
			// selects it again, fails to delete something already deleted, and
			// takes the already-gone path below — so this converges.
			s.o11y.Logger().WithValue(requestIDKey, req.ID).
				Error("marking dataprivacy artifact expired", markErr)

			continue
		}

		expired++
	}

	if expired > 0 {
		s.expiredCounter.Add(ctx, expired)
	}

	return expired, nil
}

// deleteArtifact removes one object, treating one that is already gone as
// success.
//
// Delete failing because the object does not exist is the expected outcome of a
// sweep that was interrupted between the delete and the row update, and of an
// operator who cleaned up by hand. Retrying it forever would leave the row
// pointing at nothing in perpetuity, so the absence is confirmed and accepted.
func (s *Sweeper) deleteArtifact(ctx context.Context, req *Request) error {
	err := s.uploader.Delete(ctx, req.ArtifactRef)
	if err == nil {
		return nil
	}

	exists, existsErr := s.uploader.Exists(ctx, req.ArtifactRef)
	if existsErr != nil {
		return platformerrors.Wrap(err, "deleting dataprivacy artifact")
	}

	if exists {
		return platformerrors.Wrap(err, "deleting dataprivacy artifact")
	}

	return nil
}

// sampleOverdue records how many requests are past their statutory deadline.
//
// This is the package's primary health signal. Every other instrument is a rate
// or a latency, and none of them can distinguish "fulfilling requests steadily"
// from "fulfilling requests steadily while a queue of them goes past thirty
// days". Alerting on it is left to the operator: the number is a fact, and what
// counts as an incident is a policy.
func (s *Sweeper) sampleOverdue(ctx context.Context, now time.Time) (map[RequestType]int64, error) {
	counts, err := s.store.CountOverdue(ctx, now)
	if err != nil {
		return nil, platformerrors.Wrap(err, "counting overdue dataprivacy requests")
	}

	for requestType, count := range counts {
		s.overdueGauge.Record(ctx, count, requestTypeAttr(requestType))
	}

	return counts, nil
}
