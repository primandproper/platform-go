package retention

import (
	"context"
	"strconv"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// DefaultSweepJobName is the name the Sweeper's jobs.Job carries.
	//
	// It is a constant because a job's name is its lock key: two replicas that
	// disagree about it both run the sweep, and the sweep deletes things.
	DefaultSweepJobName = "retention-sweep"

	// DefaultBatchSize caps how many rows one batch removes.
	DefaultBatchSize = 1000
	// DefaultMaxBatches caps how many batches one sweep spends on one policy.
	// A thousand rows a batch means a policy may remove a million rows a run
	// before it defers the rest to the next one.
	DefaultMaxBatches = 1000
	// DefaultBatchPause is how long the sweeper waits between batches, so a
	// backlog is worked off at a rate the database is not obliged to absorb all
	// at once.
	DefaultBatchPause = 100 * time.Millisecond
	// DefaultBacklogCeiling is where the backlog gauge saturates.
	DefaultBacklogCeiling = 100_000
)

// SweeperConfig carries the sweeper-wide defaults a Policy may override.
//
// There is deliberately no dialect field. The SQL has to match the database the
// client is connected to, and database.Client reports which that is — a
// separate setting could only ever disagree with it.
type SweeperConfig struct {
	// BatchSize caps how many rows one batch removes, for policies that do not
	// set their own. Defaults to DefaultBatchSize.
	BatchSize int `env:"BATCH_SIZE" json:"batchSize,omitempty" yaml:"batchSize,omitempty"`

	// MaxBatches caps how many batches one sweep spends on one policy, for
	// policies that do not set their own. Defaults to DefaultMaxBatches.
	MaxBatches int `env:"MAX_BATCHES" json:"maxBatches,omitempty" yaml:"maxBatches,omitempty"`

	// BacklogCeiling is where the per-policy backlog gauge saturates. Defaults
	// to DefaultBacklogCeiling.
	BacklogCeiling int `env:"BACKLOG_CEILING" json:"backlogCeiling,omitempty" yaml:"backlogCeiling,omitempty"`

	// BatchPause is how long to wait between one policy's batches. Defaults to
	// DefaultBatchPause.
	//
	// It is the knob that turns a sweep from a burst into a trickle, and the
	// one worth raising first when a nightly sweep shows up in somebody else's
	// latency graph. Zero is permitted and means no pause, which is right for a
	// sweep running against a database nothing else is using.
	BatchPause time.Duration `env:"BATCH_PAUSE" json:"batchPause,omitempty" yaml:"batchPause,omitempty"`
}

var _ validation.ValidatableWithContext = (*SweeperConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *SweeperConfig) EnsureDefaults() {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.MaxBatches <= 0 {
		cfg.MaxBatches = DefaultMaxBatches
	}
	if cfg.BacklogCeiling <= 0 {
		cfg.BacklogCeiling = DefaultBacklogCeiling
	}
	if cfg.BatchPause < 0 {
		cfg.BatchPause = DefaultBatchPause
	}
}

// ValidateWithContext validates a SweeperConfig.
func (cfg *SweeperConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.BatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.MaxBatches, validation.Required, validation.Min(1)),
		validation.Field(&cfg.BacklogCeiling, validation.Required, validation.Min(1)),
		validation.Field(&cfg.BatchPause, validation.Min(time.Duration(0))),
	)
}

// PolicyResult is what one sweep did for one policy.
type PolicyResult struct {
	// Name is the policy's name.
	Name string
	// Target is what the policy deletes from, as the Target describes itself.
	Target string
	// Removed is how many rows were deleted.
	Removed int64
	// Backlog is how many expired rows remained when the policy stopped,
	// saturating at SweeperConfig.BacklogCeiling. It is sampled whether or not
	// anything was removed — a policy that deleted nothing because it is
	// blocked and one that deleted nothing because there was nothing to delete
	// are the two cases this number separates.
	Backlog int64
	// Batches is how many batches ran.
	Batches int
	// Drained reports whether the policy reached the end of its backlog. False
	// means it stopped at MaxBatches and will resume next sweep.
	Drained bool
}

// SweepResult is what one sweep did, in the order the policies were registered.
type SweepResult struct {
	// Policies holds one entry per policy that ran. A disabled policy is
	// absent rather than present with zeroes.
	Policies []PolicyResult
	// Removed is the total across every policy.
	Removed int64
}

// Sweeper executes retention policies.
//
// It owns no goroutine and no ticker: it is rendered as a jobs.Job and
// registered with a jobs.Scheduler, whose lock is what makes the sweep run once
// across a fleet. See Job.
type Sweeper struct {
	client   database.Client
	clock    clock.Clock
	o11y     observability.Observer
	recorder audit.Recorder

	removedCounter metrics.Int64Counter
	batchCounter   metrics.Int64Counter
	errorsCounter  metrics.Int64Counter
	backlogGauge   metrics.Int64Gauge
	sweepHist      metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this sweeper actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	actor audit.Actor

	policies []Policy

	cfg SweeperConfig
}

// NewSweeper builds a Sweeper over a set of policies. It does not schedule it;
// see Job.
//
// The policies are copied, validated against the client's dialect, and checked
// for duplicate names. A single bad policy fails the whole construction rather
// than being dropped: a sweeper that silently runs four of the five policies it
// was given is the failure this package exists to make impossible.
//
// ctx is used to validate the config and is not retained — each Sweep takes its
// own.
func NewSweeper(
	ctx context.Context,
	cfg *SweeperConfig,
	client database.Client,
	policies []Policy,
	opts ...SweeperOption,
) (*Sweeper, error) {
	if cfg == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil retention sweeper config")
	}
	if client == nil {
		return nil, ErrNilDatabaseClient
	}
	if len(policies) == 0 {
		return nil, ErrNoPolicies
	}

	cfg.EnsureDefaults()

	s := &Sweeper{
		cfg:    *cfg,
		client: client,
		clock:  clock.NewClock(),
		actor:  audit.Actor{ID: DefaultAuditActorID, Type: audit.ActorSystem},
		// Copied out of the caller's slice, which they still own and may go on
		// to mutate.
		policies: append(make([]Policy, 0, len(policies)), policies...),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := s.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating retention sweeper config")
	}

	if err := validatePolicies(s.policies, client.Dialect()); err != nil {
		return nil, err
	}

	s.o11y = observability.NewObserver(serviceName, s.logger, s.tracerProvider)

	if err := s.buildInstruments(); err != nil {
		return nil, err
	}

	return s, nil
}

// validatePolicies checks every policy and rejects duplicate names.
func validatePolicies(policies []Policy, d dialect.Dialect) error {
	seen := make(map[string]struct{}, len(policies))

	for i := range policies {
		if err := policies[i].validate(d); err != nil {
			return err
		}

		if _, taken := seen[policies[i].Name]; taken {
			return platformerrors.Wrapf(ErrDuplicatePolicy, "retention policy %q", policies[i].Name)
		}
		seen[policies[i].Name] = struct{}{}
	}

	return nil
}

// buildInstruments creates the Sweeper's metrics up front, so a misconfigured
// meter fails the constructor rather than the first sweep.
func (s *Sweeper) buildInstruments() error {
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	var err error
	if s.removedCounter, err = mp.NewInt64Counter(serviceName + "_rows_removed"); err != nil {
		return platformerrors.Wrap(err, "creating rows removed counter")
	}
	if s.batchCounter, err = mp.NewInt64Counter(serviceName + "_batches"); err != nil {
		return platformerrors.Wrap(err, "creating batches counter")
	}
	if s.errorsCounter, err = mp.NewInt64Counter(serviceName + "_sweep_errors"); err != nil {
		return platformerrors.Wrap(err, "creating sweep errors counter")
	}
	if s.backlogGauge, err = mp.NewInt64Gauge(serviceName + "_backlog"); err != nil {
		return platformerrors.Wrap(err, "creating backlog gauge")
	}
	if s.sweepHist, err = mp.NewFloat64Histogram(serviceName + "_sweep_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating sweep latency histogram")
	}

	return nil
}

// Policies returns the registered policies, for a caller reporting what this
// deployment enforces — a status endpoint, a startup log line, or the answer to
// somebody asking what happens to a table.
//
// The slice is a copy; the Policy values in it share the caller's original
// Target values, which are documented as immutable.
func (s *Sweeper) Policies() []Policy {
	return append(make([]Policy, 0, len(s.policies)), s.policies...)
}

// Job renders the Sweeper as a jobs.Job, for registration with a
// jobs.Scheduler.
//
// Scheduling it there rather than running a ticker of its own is what makes the
// sweep run once across a fleet instead of once per replica. Ten replicas each
// deleting the same rows is not ten times the deletion — the first one wins and
// the rest remove nothing — but it is ten times the lock contention on tables
// that are still serving traffic.
//
// leaseTTL must comfortably exceed one sweep. The scheduler does not renew a
// lease while a job runs, so a sweep that outlives its lease loses exclusivity
// halfway through — see jobs.Job.LeaseTTL. MaxBatches and BatchPause are what
// bound a sweep, and the two are what a lease has to be sized against: a policy
// at the defaults can spend a hundred seconds pausing alone.
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

// Sweep runs every enabled policy once and reports what it removed.
//
// Policies run in the order they were registered, which is the only control a
// caller has over ordering and the reason it is preserved: a child table whose
// parent is also under policy has to go first, or the parent's DELETE fails on
// the foreign key.
//
// A policy that fails does not stop the others. Its error is collected, counted,
// and returned joined with the rest alongside a result that still describes
// everything that did work — a locked table is not a reason to skip the four
// unrelated policies behind it.
func (s *Sweeper) Sweep(ctx context.Context) (*SweepResult, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(policyCountKey, len(s.policies)))
	defer op.End()

	defer op.Time(ctx, s.clock, s.sweepHist)()

	result := &SweepResult{Policies: make([]PolicyResult, 0, len(s.policies))}

	var errs []error

	for i := range s.policies {
		policy := &s.policies[i]
		if policy.Disabled {
			continue
		}

		// Not acknowledged again here. sweepPolicy has already reported it
		// against an operation carrying the policy's name and cutoff, and a
		// second line at this altitude would say less about the same failure.
		policyResult, err := s.sweepPolicy(ctx, policy)
		if err != nil {
			s.errorsCounter.Add(ctx, 1, policyAttrs(policy.Name))
			errs = append(errs, err)
		}

		// Appended even on error. A policy that failed on its third batch still
		// removed the rows the first two deleted, and those rows are gone
		// whether or not the caller is told about them.
		if policyResult != nil {
			result.Policies = append(result.Policies, *policyResult)
			result.Removed += policyResult.Removed
		}
	}

	op.Set(removedKey, result.Removed)

	if len(errs) > 0 {
		return result, op.Error(platformerrors.Join(errs...), "sweeping retention policies")
	}

	return result, nil
}

// sweepPolicy drains one policy, samples its backlog, and records the entry
// accounting for it.
//
// It returns a result even when it errors, so the caller learns what the batches
// before the failure removed.
func (s *Sweeper) sweepPolicy(ctx context.Context, policy *Policy) (*PolicyResult, error) {
	cutoff := s.clock.Now().UTC().Add(-policy.Age)

	ctx, op := s.o11y.BeginCustom(ctx, "sweep_policy")
	defer op.End()

	op.SetValues(map[string]any{
		policyNameKey: policy.Name,
		targetKey:     policy.Target.Describe(),
		cutoffKey:     cutoff,
	})

	result := &PolicyResult{Name: policy.Name, Target: policy.Target.Describe()}

	batchSize, maxBatches := s.bounds(policy)

	var err error

	for result.Batches < maxBatches {
		var removed int64
		if removed, err = s.sweepBatch(ctx, policy, cutoff, batchSize); err != nil {
			break
		}

		result.Batches++
		result.Removed += removed

		// Short of the bound means the target had nothing more to give. Asking
		// again would cost a query to be told the same thing.
		if removed < int64(batchSize) {
			result.Drained = true

			break
		}

		// Paused between batches, not after the last one: a policy that has
		// drained has nothing to be gentle about, and the pause would be paid
		// by every other policy waiting behind it.
		if err = s.clock.Sleep(ctx, s.cfg.BatchPause); err != nil {
			break
		}
	}

	s.recordCounters(ctx, policy, result)

	// Sampled last and regardless of the error above, because a policy that
	// just failed is precisely the one whose backlog somebody needs to see.
	result.Backlog = s.sampleBacklog(ctx, op, policy, cutoff)

	op.SetValues(map[string]any{
		removedKey: result.Removed,
		batchesKey: result.Batches,
		backlogKey: result.Backlog,
		drainedKey: result.Drained,
	})

	errs := make([]error, 0, 2)
	if err != nil {
		errs = append(errs, err)
	}

	// Written even when the batches failed. The rows the earlier batches removed
	// are gone, and an accounting record that omits a deletion because a later
	// one failed is the one kind of gap this package must not have.
	if auditErr := s.recordSweep(ctx, policy, result, cutoff); auditErr != nil {
		errs = append(errs, auditErr)
	}

	if len(errs) > 0 {
		return result, op.Error(platformerrors.Join(errs...), "sweeping retention policy %q", policy.Name)
	}

	return result, nil
}

// bounds resolves the batch size and batch cap for a policy, either of which it
// may override.
func (s *Sweeper) bounds(policy *Policy) (batchSize, maxBatches int) {
	batchSize, maxBatches = policy.BatchSize, policy.MaxBatches
	if batchSize <= 0 {
		batchSize = s.cfg.BatchSize
	}
	if maxBatches <= 0 {
		maxBatches = s.cfg.MaxBatches
	}

	return batchSize, maxBatches
}

// sweepBatch removes one batch, in a transaction of its own.
//
// One transaction per batch rather than one per policy is the point of
// batching: a transaction spanning every batch would hold every row's lock
// until the last one committed, which is the long lock-holding DELETE this
// exists to avoid, wearing a loop.
func (s *Sweeper) sweepBatch(ctx context.Context, policy *Policy, cutoff time.Time, batchSize int) (int64, error) {
	var removed int64

	err := s.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		var sweepErr error
		removed, sweepErr = policy.Target.Sweep(ctx, q, s.client.Dialect(), cutoff, batchSize)

		return sweepErr
	})
	if err != nil {
		// Not named with the policy here: sweepPolicy adds that, and the
		// Target has already named what it could not delete from.
		return 0, platformerrors.Wrap(err, "removing a batch of expired rows")
	}

	return removed, nil
}

// recordCounters posts the batch and row counters for a policy.
func (s *Sweeper) recordCounters(ctx context.Context, policy *Policy, result *PolicyResult) {
	attrs := policyAttrs(policy.Name)

	if result.Batches > 0 {
		s.batchCounter.Add(ctx, int64(result.Batches), attrs)
	}
	if result.Removed > 0 {
		s.removedCounter.Add(ctx, result.Removed, attrs)
	}
}

// sampleBacklog reads how much of a policy's backlog is left and records the
// gauge, reporting zero if it cannot.
//
// A failure here is logged rather than returned. The backlog is a reading about
// the sweep, not part of it, and failing a sweep that successfully deleted a
// million rows because the count that followed it timed out would be reporting
// the wrong thing.
func (s *Sweeper) sampleBacklog(
	ctx context.Context,
	op observability.Operation,
	policy *Policy,
	cutoff time.Time,
) int64 {
	backlog, err := policy.Target.Backlog(ctx, s.client.Reader(), s.client.Dialect(), cutoff, s.cfg.BacklogCeiling)
	if err != nil {
		s.errorsCounter.Add(ctx, 1, policyAttrs(policy.Name))
		op.Acknowledge(err, "sampling backlog for retention policy %q", policy.Name)

		return 0
	}

	s.backlogGauge.Record(ctx, backlog, policyAttrs(policy.Name))

	return backlog
}

// recordSweep writes the audit entry accounting for one policy's run.
//
// Nothing is written for a policy that removed nothing: a nightly entry per
// policy saying zero would, within a year, be most of what the audit log
// contains, and the log is where an investigation starts. The counters and the
// backlog gauge are what say a policy ran and found nothing.
//
// The entry is its own transaction rather than part of a batch's. An entry per
// batch would be one audit row per thousand deleted rows, in a table whose own
// retention default is seven years. The gap that buys — a crash between the
// last batch and this write loses the run's record — is why Removed is also a
// counter.
func (s *Sweeper) recordSweep(ctx context.Context, policy *Policy, result *PolicyResult, cutoff time.Time) error {
	if s.recorder == nil || result.Removed == 0 {
		return nil
	}

	entry := &audit.Entry{
		EventType:    audit.EventDeleted,
		ResourceType: AuditResourceType,
		ResourceID:   policy.Name,
		Actor:        s.actor,
		Scope:        policy.Scope,
		Metadata: map[string]string{
			"target":       result.Target,
			"cutoff":       cutoff.Format(time.RFC3339Nano),
			"age":          policy.Age.String(),
			"rows_removed": strconv.FormatInt(result.Removed, 10),
			"batches":      strconv.Itoa(result.Batches),
			"drained":      strconv.FormatBool(result.Drained),
		},
	}

	if policy.Basis != "" {
		entry.Metadata["basis"] = policy.Basis
	}

	// Written on a context that cannot be canceled, so a sweep stopped by its
	// job timeout still accounts for the rows it already deleted. The rows are
	// gone either way; losing the record of them because the clock ran out is
	// the failure, not the delay.
	ctx = context.WithoutCancel(ctx)

	err := s.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		return s.recorder.Record(ctx, q, entry)
	})
	if err != nil {
		return platformerrors.Wrapf(err, "recording retention sweep of policy %q", policy.Name)
	}

	return nil
}

// policyAttrs is the metric attribute set every instrument here carries. The
// policy name is the only dimension: the target is already implied by it, and
// two attributes that always vary together are one attribute and twice the
// cardinality.
func policyAttrs(name string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(policyNameKey, name))
}
