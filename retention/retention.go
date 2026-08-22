package retention

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "retention"

// AuditResourceType is the audit resource type a sweep's entry carries. The
// resource acted on is the policy, not the table: the table is named in the
// entry's metadata, and a policy may outlive the table it was written for.
const AuditResourceType = "retention_policy"

// DefaultAuditActorID identifies the sweeper in the audit log when no actor is
// configured. See WithSweeperActor.
const DefaultAuditActorID = "retention-sweeper"

// Observability keys for this package's spans and log fields. Declared once so
// a field set on a span and the same field logged beside it cannot drift, and
// so the retention. prefix is applied uniformly — an un-namespaced attribute
// name collides with every other component writing to the same trace.
const (
	policyNameKey  = "retention.policy"
	policyCountKey = "retention.policy_count"
	targetKey      = "retention.target"
	cutoffKey      = "retention.cutoff"
	removedKey     = "retention.rows_removed"
	backlogKey     = "retention.backlog"
	batchesKey     = "retention.batches"
	drainedKey     = "retention.drained"
)

var (
	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrNoPolicies indicates a Sweeper built with nothing to sweep.
	//
	// Refused rather than accepted as an empty schedule, because a sweeper with
	// no policies is indistinguishable at runtime from one whose policies are
	// all working — it logs nothing, deletes nothing, and reports no backlog.
	// The deployment that meant to disable retention should not register the
	// job.
	ErrNoPolicies = platformerrors.New("no retention policies provided")

	// ErrInvalidPolicy indicates a policy that cannot be swept. It is wrapped
	// with the offending policy's name and what is wrong with it.
	ErrInvalidPolicy = platformerrors.New("invalid retention policy")

	// ErrDuplicatePolicy indicates two policies sharing a name. Names appear in
	// the audit record and in every metric attribute, so two policies answering
	// to one name make both unaccountable.
	ErrDuplicatePolicy = platformerrors.New("duplicate retention policy")

	// ErrNilTarget indicates a policy with nothing to delete from.
	ErrNilTarget = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil retention target")
)

// Target is how a policy selects the rows that have expired.
//
// It is an interface with one implementation shipped — Table — because the
// declarative case covers nearly everything and the escape hatch has to exist
// for the rest: a policy whose notion of expiry involves a join, a parent's
// state, or a column that is not a timestamp cannot be expressed as data, and
// should be expressed as Go rather than as a SQL fragment smuggled through
// configuration.
//
// Implementations are values, not stateful objects. The Sweeper holds them for
// its lifetime and calls them from one goroutine at a time per policy, but
// makes no other promise — treat them as immutable.
type Target interface {
	// Validate reports whether this target can be executed against d. It is
	// called once per policy at construction, so a table name that would not
	// render a legal identifier, or a dialect the target cannot emit, fails the
	// process at startup rather than at four in the morning.
	Validate(d dialect.Dialect) error

	// Sweep deletes at most limit rows whose expiry instant is at or before
	// cutoff, and reports how many it removed.
	//
	// It is called inside a transaction the Sweeper owns, once per batch. It
	// must remove no more than limit rows: that bound is the only thing
	// standing between a neglected table and a DELETE that holds locks for
	// minutes. Returning fewer than limit is how the Sweeper learns the target
	// has drained, so an implementation that cannot honor the bound exactly
	// should undershoot rather than over.
	Sweep(ctx context.Context, q database.SQLQueryExecutor, d dialect.Dialect, cutoff time.Time, limit int) (int64, error)

	// Backlog reports how many rows are still at or before cutoff, saturating
	// at ceiling.
	//
	// It saturates because this is a gauge, not an inventory: the number an
	// operator acts on is "is the backlog growing", and answering it exactly
	// would mean an unbounded COUNT over the table the sweep just finished
	// deleting from. A reading equal to ceiling means "at least this many".
	Backlog(ctx context.Context, q database.SQLQueryExecutor, d dialect.Dialect, cutoff time.Time, ceiling int) (int64, error)

	// Describe names what this target deletes from, for telemetry and for the
	// audit entry. It is a table name for Table, and should be something an
	// operator reading a trace can act on.
	Describe() string
}

// Policy is one rule: what to delete, and how old it has to be.
//
// Policies are values assembled by the application and handed to NewSweeper as
// a slice. They are validated once, at construction, and not mutated after.
type Policy struct {
	// Target selects the expired rows. Required.
	Target Target

	// Name identifies the policy in telemetry, in the metric attributes, and as
	// the resource ID of the audit entry each sweep writes. It must be unique
	// within a Sweeper and stable across deploys — renaming a policy silently
	// starts a new accounting history for the same rows. Required.
	Name string

	// Basis is why the data is deleted, in the words the person who has to
	// answer for it would use: "access tokens are useless once expired",
	// "captures hold request bodies and may contain PII".
	//
	// It is recorded in the audit entry's metadata. That is the whole reason it
	// exists — a retention record saying 40,000 rows were deleted from a table
	// is evidence of a deletion, and one that also says why is evidence of a
	// policy.
	Basis string

	// Scope is the audit scope the sweep's entry is recorded in. Empty is a
	// scope like any other, and is the right one for a fleet-wide sweep that
	// belongs to no tenant.
	Scope string

	// Age is how long a row is kept past the instant in the column the Target
	// measures from.
	//
	// It carries two readings, depending on that column. Against a created_at
	// it is a retention window. Against an expires_at the row was already dead
	// at the instant recorded, and Age is the grace period after it — which is
	// why zero is permitted here, and why it is worth being sure which of the
	// two a given policy means. A zero Age against a created_at deletes the
	// table.
	Age time.Duration

	// BatchSize caps how many rows one batch removes. Zero takes the Sweeper's
	// SweeperConfig.BatchSize.
	//
	// It is per policy because the right batch is a function of the row: a
	// table of narrow token rows tolerates ten thousand at a time, and one
	// holding captured request bodies does not.
	BatchSize int

	// MaxBatches caps how many batches one sweep spends on this policy. Zero
	// takes the Sweeper's SweeperConfig.MaxBatches.
	//
	// A policy that hits the cap is reported as undrained with its backlog, and
	// resumes on the next sweep. The cap is what keeps one enormous table from
	// consuming the whole run — and the whole lease — while every other policy
	// waits.
	MaxBatches int

	// Disabled keeps the policy registered and stops it running.
	//
	// It exists so that turning a policy off is an edit to the policy rather
	// than a deletion of it: a commented-out policy loses its name, its basis,
	// and the reason anybody wrote it, and the next person adds a subtly
	// different one.
	Disabled bool
}

// validate reports whether the policy can be swept at all, against the dialect
// its target will be executed with.
func (p *Policy) validate(d dialect.Dialect) error {
	switch {
	case p.Name == "":
		return platformerrors.Wrap(ErrInvalidPolicy, "empty policy name")
	case p.Target == nil:
		return platformerrors.Wrapf(ErrNilTarget, "retention policy %q", p.Name)
	case p.Age < 0:
		return platformerrors.Wrapf(ErrInvalidPolicy, "policy %q has a negative age", p.Name)
	case p.BatchSize < 0:
		return platformerrors.Wrapf(ErrInvalidPolicy, "policy %q has a negative batch size", p.Name)
	case p.MaxBatches < 0:
		return platformerrors.Wrapf(ErrInvalidPolicy, "policy %q has a negative batch cap", p.Name)
	}

	if err := p.Target.Validate(d); err != nil {
		return platformerrors.Wrapf(err, "validating target of retention policy %q", p.Name)
	}

	return nil
}
