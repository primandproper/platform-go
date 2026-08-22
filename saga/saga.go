package saga

import (
	"context"
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "saga"

// Observability keys for this package's spans and log fields. Declared once so
// a field set on a span and the same field logged beside it cannot drift, and
// so the saga. prefix is applied uniformly — an un-namespaced attribute name
// collides with every other component writing to the same trace.
//
// Nothing here carries saga state. A Step's T is the application's own domain
// object — an order, a booking, a charge — and a span exporter is a durable
// store the application never chose to put it in.
const (
	instanceIDKey  = "saga.instance_id"
	definitionKey  = "saga.definition"
	statusKey      = "saga.status"
	stepKey        = "saga.step"
	stepIndexKey   = "saga.step_index"
	stepCountKey   = "saga.step_count"
	phaseKey       = "saga.phase"
	attemptsKey    = "saga.attempts"
	terminalKey    = "saga.terminal"
	replayedKey    = "saga.replayed"
	claimedKey     = "saga.claimed"
	advancedKey    = "saga.steps_advanced"
	stateBytesKey  = "saga.state_bytes"
	nextAttemptKey = "saga.next_attempt"

	// Store-layer keys. The database client traces the statement, but with the
	// SQL text suppressed by default — so without these a trace shows an
	// anonymous query span and no indication of which instance it was about.
	storeOpKey      = "saga.store_operation"
	fromStatusKey   = "saga.from_status"
	rowsAffectedKey = "saga.rows_affected"
	guardMissedKey  = "saga.guard_missed"
	selectedKey     = "saga.selected"
	resultCountKey  = "saga.result_count"
	resultTotalKey  = "saga.result_total"
	limitKey        = "saga.limit"
)

// Phases a step can be executed in. They are part of the idempotency key, so
// running a step and undoing it can never share a claim.
const (
	phaseDo   = "do"
	phaseUndo = "undo"
)

var (
	// ErrNilStore indicates a nil Store. It wraps errors.ErrNilInputParameter,
	// so a caller may check either.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil saga store")

	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrNilExecutor indicates a Store method that runs in the caller's
	// transaction was called without one.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")

	// ErrNilRegistry indicates a nil *Registry.
	ErrNilRegistry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil saga registry")

	// ErrNilInstance indicates a nil instance record.
	ErrNilInstance = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil saga instance")

	// ErrNilLocker indicates a Worker built without a distributed lock.
	//
	// It has no default, on the same terms as idempotency.ErrNilLocker: an
	// implicit noop would leave every other guarantee in this package intact
	// while quietly removing the one that stops two workers running the same
	// step at the same time — which is the failure this package exists to
	// prevent, and the one nobody notices until it has been charging cards
	// twice for a week.
	ErrNilLocker = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil saga locker")

	// ErrInstanceNotFound indicates an instance ID that is not in the table, or
	// one that is not in the status the operation required.
	ErrInstanceNotFound = platformerrors.New("saga instance not found")

	// ErrUnknownDefinition indicates a definition name this process has not
	// registered.
	ErrUnknownDefinition = platformerrors.New("unknown saga definition")

	// ErrDuplicateDefinition indicates two registrations under one name. A
	// silent overwrite would swap the step list under instances that are
	// already in flight, which is the one thing the drift check exists to
	// prevent.
	ErrDuplicateDefinition = platformerrors.New("duplicate saga definition")

	// ErrInvalidDefinition indicates a Definition that cannot be run: no name,
	// no steps, an unnamed or duplicately-named step, or a step with no Do.
	ErrInvalidDefinition = platformerrors.New("invalid saga definition")

	// ErrStateTypeMismatch indicates a Runner[T] used against a definition
	// registered with a different state type. The registry erases T, so the
	// compiler cannot catch this; it is reported at the call rather than
	// producing a state value silently decoded into the wrong shape.
	ErrStateTypeMismatch = platformerrors.New("saga state type does not match the definition")

	// ErrDefinitionDrift indicates an instance whose stored step names no
	// longer match the definition this build registers.
	//
	// Versioning of in-flight definitions is permanently out of scope, so this
	// is the honest answer rather than a temporary one: the instance is marked
	// stuck and left for a human. The alternative is running a compensation
	// from the new step list against work done by the old one, which is how a
	// deploy comes to refund the wrong charge.
	ErrDefinitionDrift = platformerrors.New("saga definition has changed since this instance started")

	// ErrNotResumable indicates a Resume against an instance in a status Resume
	// does not apply to — one that is already running, or one that finished.
	ErrNotResumable = platformerrors.New("saga instance is not resumable")

	// ErrStepPanicked indicates a Do or Undo that panicked. It is contained and
	// converted into that step's failure: somebody else's code running in our
	// goroutine should cost its own saga, not every other instance in the batch.
	ErrStepPanicked = platformerrors.New("saga step panicked")

	// ErrCompensationFailed indicates a compensation that exhausted its retry
	// budget. The instance is StatusStuck and needs an operator.
	ErrCompensationFailed = platformerrors.New("saga compensation failed")
)

// Status is where an instance has got to.
//
// The transitions between these are diagrammed in the package overview. There
// are five and there will not be more: every additional status in a durable
// execution engine is another pair of edges the compensation logic has to be
// correct about.
type Status string

const (
	// StatusRunning is an instance working forward through its steps.
	// CurrentStep is the index of the step that runs next.
	StatusRunning Status = "running"

	// StatusCompleted is an instance that ran every step. Terminal.
	StatusCompleted Status = "completed"

	// StatusCompensating is an instance unwinding. CurrentStep is the index of
	// the step whose Undo runs next, counting down.
	StatusCompensating Status = "compensating"

	// StatusCompensated is an instance that unwound cleanly. Terminal, and not
	// a success: the work did not happen, and it was undone on purpose.
	StatusCompensated Status = "compensated"

	// StatusStuck means a compensation itself failed past its retry budget, or
	// the instance cannot be advanced at all — an unknown definition, or one
	// whose steps have changed underneath it.
	//
	// It is a first-class terminal state rather than a flavor of failure, and
	// it is never resolved automatically. Something has been half-done and the
	// library has run out of ways to undo it, which is a fact about the outside
	// world that no amount of retrying inside this process can change. Most
	// homegrown saga implementations swallow this case; it is how money goes
	// missing. Alert on saga_instances_stuck, fix whatever broke, and call
	// Runner.Resume.
	StatusStuck Status = "stuck"
)

// Terminal reports whether a status is one a worker will not move out of.
//
// StatusStuck is terminal in exactly this sense — no worker advances it — while
// still being the one terminal status Resume accepts.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusCompensated, StatusStuck:
		return true
	case StatusRunning, StatusCompensating:
		return false
	default:
		return false
	}
}

// Valid reports whether s is a status this package writes.
func (s Status) Valid() bool {
	switch s {
	case StatusRunning, StatusCompleted, StatusCompensating, StatusCompensated, StatusStuck:
		return true
	default:
		return false
	}
}

// activeStatuses are the statuses a worker can advance. Used by the claim
// predicate and by every guarded write, which must not move a terminal row.
var activeStatuses = []Status{StatusRunning, StatusCompensating}

// Step is one unit of a saga.
type Step[T any] struct {
	// Do executes the step. It receives the saga's state and may mutate it; the
	// mutated state is persisted before the next step runs.
	//
	// It must be idempotent with respect to the idempotency key the library
	// supplies — see the package documentation on what that key does and does
	// not promise. Returning an error wrapped in retry.Unretryable skips the
	// remaining attempts and begins compensation immediately, which is the
	// right answer for a rejection that will not become an acceptance.
	//
	// Required.
	Do func(ctx context.Context, state *T) error

	// Undo compensates a Do. It must be idempotent and it must tolerate a Do
	// that only partly applied, because it is run for the step that failed as
	// well as for the steps that succeeded.
	//
	// That inclusive boundary is deliberate and it is the one place this
	// departs from the textbook description of a saga. A Do that returned an
	// error may still have posted the charge, written the object, or sent the
	// message before it failed; a compensation that started at the previous
	// step would leave exactly that half-applied effect behind, which is the
	// effect a saga exists to clean up. An Undo with nothing to undo is a
	// no-op, and this contract already requires it to be one.
	//
	// Nil means the step needs no compensation — a pure read, or an inherently
	// idempotent write. It is skipped rather than treated as a failure.
	Undo func(ctx context.Context, state *T) error

	// Name identifies the step. It must be unique within the definition and is
	// part of the idempotency key, so renaming a step in a deploy is a change
	// to that key — see ErrDefinitionDrift, which refuses to advance an
	// instance across such a rename rather than re-executing under a new key.
	//
	// Because it goes into a key, it is restricted rather than escaped: up to
	// 64 bytes of printable ASCII with no spaces and no colons. charge_card and
	// reserve-inventory are fine; a sentence is not.
	//
	// Required.
	Name string

	// Delay is how long to wait before running this step, measured from when
	// the previous one completed. Zero runs it as soon as a worker gets to it.
	//
	// The wait is persisted as a timestamp rather than slept through, so it
	// survives a restart and costs no worker for its duration. It is the whole
	// of this package's scheduling: a delay is not a timer, has no cancellation,
	// and cannot be signaled. If a step needs to wait for something rather than
	// for a duration, that something is an external event and this package does
	// not do those.
	Delay time.Duration
}

// Definition is a named, linear sequence of steps.
//
// Linear is the hard constraint, not a v1 simplification: no branching, no
// parallel fan-out, no sub-sagas. See the package documentation for what to
// reach for when a process needs any of those.
type Definition[T any] struct {
	// Name identifies the definition. Instances record it, so it is the one
	// string that must stay stable across deploys.
	Name string

	// Steps run in order. At least one is required.
	Steps []Step[T]
}

// Instance is one run of a definition and everything known about where it got
// to.
//
// The generic parameter is the state type. Store operates on Record — this same
// struct with the state left as encoded bytes — because the storage layer, the
// worker pool, and the DI container that holds them are all necessarily
// non-generic. T appears at the API surface and nowhere below it.
type Instance[T any] struct {
	// StartedAt is when the instance was created. It never moves.
	StartedAt time.Time `json:"startedAt"`

	// UpdatedAt is when the instance last changed status, cursor, or state.
	UpdatedAt time.Time `json:"updatedAt"`

	// State is the saga's own data, as the last completed step left it.
	State T `json:"state"`

	// ID identifies the instance.
	ID string `json:"id"`

	// Definition names the definition being run.
	Definition string `json:"definition"`

	// LastError is why the last attempt failed, rendered and truncated. Empty
	// once a step succeeds.
	LastError string `json:"lastError,omitempty"`

	// Status is where the instance got to.
	Status Status `json:"status"`

	// ResumeStatus is the status a stuck instance was in when it broke, and the
	// one Resume returns it to. Empty for an instance that is not stuck.
	//
	// It is stored rather than inferred because both phases can end in
	// StatusStuck — a compensation that exhausted its budget, and a running
	// instance whose definition drifted underneath it — and guessing wrong
	// resumes a saga forward through steps it was in the middle of undoing.
	// It is also the first thing an operator wants to know about a stuck saga:
	// whether the work was being done or being taken back.
	ResumeStatus Status `json:"resumeStatus,omitempty"`

	// StepNames is the definition's step list as it stood when the instance
	// started. It is stored rather than derived so that a deploy changing the
	// steps is detectable — see ErrDefinitionDrift.
	StepNames []string `json:"stepNames"`

	// CurrentStep is the cursor, and what it points at depends on the status:
	// while running it is the index of the step that runs next, and while
	// compensating it is the index of the step whose Undo runs next, counting
	// down. A compensating instance at -1 has unwound everything.
	CurrentStep int `json:"currentStep"`

	// Attempts is how many times the current step has been attempted. It is
	// incremented when a worker claims the instance rather than only when a
	// step returns an error, so a step that reliably kills its worker exhausts
	// its budget instead of being reclaimed forever. It resets to zero every
	// time the cursor moves.
	Attempts int `json:"attempts"`
}

// Record is the storage shape of an instance: the same struct with its state
// left as the bytes the codec produced.
//
// It is an alias rather than a distinct type so that the one field that differs
// is the one field that differs, and a Store implementation cannot be written
// against a struct that has quietly drifted from the public one.
type Record = Instance[json.RawMessage]

// Runner is the application-facing seam: start a saga, ask after one, re-drive
// one that needs a human's help.
//
// Advancing is deliberately not on this interface. A Start that ran eleven
// steps inline would tie a durable process to the lifetime of an HTTP request,
// and surviving the process that accepted it is the entire guarantee a saga
// offers. Start writes a row; a Worker advances it.
type Runner[T any] interface {
	// Start records a new instance of the named definition and returns it. The
	// instance is durable before Start returns and is advanced by a Worker.
	//
	// It returns an error wrapping ErrUnknownDefinition for a name this process
	// has not registered, and ErrStateTypeMismatch when the definition's state
	// type is not T.
	Start(ctx context.Context, def string, initial T) (*Instance[T], error)

	// StartInTransaction is Start using the caller's executor, so the instance
	// commits with the writes that decided to start it.
	//
	// It is the one worth reaching for. A saga started in its own transaction
	// after the caller's has committed is a saga that does not exist if the
	// process dies in between — and the work it was going to coordinate has
	// already been paid for by whatever the caller just wrote.
	StartInTransaction(ctx context.Context, q database.SQLQueryExecutor, def string, initial T) (*Instance[T], error)

	// Get reads one instance. It returns an error wrapping ErrInstanceNotFound
	// when there is no such instance, and ErrStateTypeMismatch when its
	// definition's state type is not T.
	Get(ctx context.Context, id string) (*Instance[T], error)

	// List pages through instances, optionally narrowed to a definition and a
	// set of statuses. Passing only StatusStuck is the operator's query.
	List(ctx context.Context, scope *ListScope, page *filtering.QueryFilter) (*filtering.QueryFilteredResult[Instance[T]], error)

	// Resume re-drives an instance that stopped needing a human: a stuck one,
	// once whatever broke has been fixed.
	//
	// It makes the instance claimable again in the phase it stopped in — a saga
	// stuck compensating resumes compensating, never running, because the
	// decision to unwind has already been taken and re-taking it forward would
	// re-apply the steps that were being undone.
	//
	// It returns an error wrapping ErrNotResumable for an instance that is
	// already running or has finished, and marks the instance stuck (leaving it
	// stuck) when its definition has drifted.
	Resume(ctx context.Context, id string) (*Instance[T], error)
}
