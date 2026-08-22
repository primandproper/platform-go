package saga

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// runnerName scopes the runner's spans, logger, and metrics.
const runnerName = serviceName + "_runner"

var _ Runner[struct{}] = (*StoreRunner[struct{}])(nil)

// StoreRunner is the typed surface over a non-generic Store and Registry. It is
// exported, and returned by NewRunner, so a caller can depend on the runner it
// built rather than on the Runner seam.
//
// It holds no state of its own beyond its dependencies: everything a saga knows
// is in the row. That is what makes it safe for the DI container to hold one
// StoreRunner per state type over one shared Store, and for a Worker that has never
// heard of T to advance what they start.
type StoreRunner[T any] struct {
	store     Store
	registry  *Registry
	publisher EventPublisher
	clock     clock.Clock
	o11y      observability.Observer

	startedCounter metrics.Int64Counter
	resumedCounter metrics.Int64Counter

	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewRunner builds a Runner over a Store and a Registry.
//
// There is no config: a Runner writes one row and reads others, and every knob
// that could exist — how often to poll, how long a step may take, how many
// times to retry — belongs to the Worker that does the advancing.
//
// T is the state type. It must match the type the named definition was
// registered with; Start and Get report ErrStateTypeMismatch rather than
// decoding a saga's state into a struct that merely happens to parse.
func NewRunner[T any](store Store, registry *Registry, opts ...RunnerOption) (*StoreRunner[T], error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if registry == nil {
		return nil, ErrNilRegistry
	}

	o := &runnerOptions{clock: clock.NewClock()}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	r := &StoreRunner[T]{
		store:           store,
		registry:        registry,
		publisher:       o.publisher,
		clock:           o.clock,
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
	}

	r.o11y = observability.NewObserver(runnerName, o.logger, r.tracerProvider)

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	var err error
	if r.startedCounter, err = mp.NewInt64Counter(serviceName + "_instances_started"); err != nil {
		return nil, platformerrors.Wrap(err, "creating saga instances started counter")
	}
	if r.resumedCounter, err = mp.NewInt64Counter(serviceName + "_instances_resumed"); err != nil {
		return nil, platformerrors.Wrap(err, "creating saga instances resumed counter")
	}

	return r, nil
}

// Start implements Runner.
func (r *StoreRunner[T]) Start(ctx context.Context, def string, initial T) (*Instance[T], error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	var inst *Instance[T]

	if err := r.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		var txErr error
		inst, txErr = r.start(ctx, q, def, initial)

		return txErr
	}); err != nil {
		return nil, op.Error(err, "starting saga instance")
	}

	return inst, nil
}

// StartInTransaction implements Runner.
func (r *StoreRunner[T]) StartInTransaction(
	ctx context.Context,
	q database.SQLQueryExecutor,
	def string,
	initial T,
) (*Instance[T], error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "starting saga instance")
	}

	inst, err := r.start(ctx, q, def, initial)
	if err != nil {
		return nil, op.Error(err, "starting saga instance")
	}

	return inst, nil
}

// start writes the instance and its started event through the given executor.
func (r *StoreRunner[T]) start(
	ctx context.Context,
	q database.SQLQueryExecutor,
	name string,
	initial T,
) (*Instance[T], error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(definitionKey, name))
	defer op.End()

	def, err := r.definitionFor(name)
	if err != nil {
		return nil, err
	}

	state, err := json.Marshal(initial)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding initial saga state")
	}

	now := r.clock.Now().UTC()

	rec := &Record{
		StartedAt: now,
		UpdatedAt: now,
		State:     state,
		// Cloned, so the instance's record of the step list cannot alias the
		// registry's and change with it. That list is the whole of the drift
		// check, and a drift check against a shared slice checks nothing.
		StepNames:   slices.Clone(def.stepNames),
		ID:          identifiers.New(),
		Definition:  name,
		Status:      StatusRunning,
		CurrentStep: 0,
	}

	op.Set(instanceIDKey, rec.ID).Set(stepCountKey, def.steps())

	// The first step's delay is honored the same way every other step's is: as
	// a time the row is not claimable before, rather than as a sleep somewhere.
	if err = r.store.Save(ctx, q, rec, now.Add(def.delays[0])); err != nil {
		return nil, err
	}

	if err = publish(ctx, r.publisher, q,
		newEvent(EventStarted, rec, def.stepNames[0], 0, now, ""),
	); err != nil {
		return nil, platformerrors.Wrap(err, "publishing saga started event")
	}

	r.startedCounter.Add(ctx, 1, definitionAttr(name))

	return decodeInstance[T](rec)
}

// Get implements Runner.
func (r *StoreRunner[T]) Get(ctx context.Context, id string) (*Instance[T], error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(instanceIDKey, id))
	defer op.End()

	rec, err := r.store.Get(ctx, id)
	if err != nil {
		return nil, op.Error(err, "reading saga instance")
	}

	inst, err := r.decode(rec)
	if err != nil {
		return nil, op.Error(err, "decoding saga instance")
	}

	return inst, nil
}

// List implements Runner.
func (r *StoreRunner[T]) List(
	ctx context.Context,
	scope *ListScope,
	page *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Instance[T]], error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	result, err := r.store.List(ctx, scope, page)
	if err != nil {
		return nil, op.Error(err, "listing saga instances")
	}

	decoded := make([]*Instance[T], 0, len(result.Data))
	for _, rec := range result.Data {
		inst, decodeErr := r.decode(rec)
		if decodeErr != nil {
			return nil, op.Error(decodeErr, "decoding listed saga instance")
		}

		decoded = append(decoded, inst)
	}

	return &filtering.QueryFilteredResult[Instance[T]]{
		Data:       decoded,
		Pagination: result.Pagination,
	}, nil
}

// Resume implements Runner.
func (r *StoreRunner[T]) Resume(ctx context.Context, id string) (*Instance[T], error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(instanceIDKey, id))
	defer op.End()

	rec, err := r.store.Get(ctx, id)
	if err != nil {
		return nil, op.Error(err, "reading saga instance to resume")
	}

	op.Set(statusKey, string(rec.Status)).Set(definitionKey, rec.Definition)

	if rec.Status != StatusStuck {
		// Deliberately narrow. A running instance needs no help — a worker will
		// get to it — and re-driving a completed or compensated one would run
		// steps against a saga that is over.
		return nil, op.Error(
			platformerrors.Wrapf(ErrNotResumable, "saga instance %q is %s", id, rec.Status),
			"resuming saga instance",
		)
	}

	def, err := r.definitionFor(rec.Definition)
	if err != nil {
		// Left stuck. A definition this process cannot see is not one it can
		// safely resume, and clearing the status would hand the instance to a
		// worker that would only mark it stuck again a moment later.
		return nil, op.Error(err, "resuming saga instance")
	}

	if !slices.Equal(def.stepNames, rec.StepNames) {
		return nil, op.Error(driftError(rec, def), "resuming saga instance")
	}

	// Back into the phase it stopped in. A saga stuck compensating resumes
	// compensating: the decision to unwind was taken before it broke, and
	// re-taking it forward would re-apply the very steps that were being undone.
	to := rec.ResumeStatus
	if !to.Valid() || to.Terminal() {
		// An instance stuck by a build that predates resume_status, or one
		// whose column was hand-edited. Compensating is the safe reading:
		// unwinding a saga that had not started unwinding costs a set of
		// no-op Undo calls, while running one that had costs the effects it
		// was in the middle of taking back.
		to = StatusCompensating
	}

	updated, err := r.store.Requeue(ctx, id, []Status{StatusStuck}, to, r.clock.Now().UTC())
	if err != nil {
		return nil, op.Error(err, "requeuing saga instance")
	}

	r.resumedCounter.Add(ctx, 1, definitionAttr(rec.Definition))

	r.o11y.Logger().WithValues(map[string]any{
		instanceIDKey: id,
		definitionKey: rec.Definition,
		statusKey:     string(to),
	}).Info("saga instance resumed by operator")

	inst, err := r.decode(updated)
	if err != nil {
		return nil, op.Error(err, "decoding resumed saga instance")
	}

	return inst, nil
}

// definitionFor resolves a definition and checks that its state type is T.
func (r *StoreRunner[T]) definitionFor(name string) (*definition, error) {
	def, ok := r.registry.lookup(name)
	if !ok {
		return nil, platformerrors.Wrapf(ErrUnknownDefinition, "saga definition %q", name)
	}

	if want := reflect.TypeFor[T](); def.stateType != want {
		return nil, platformerrors.Wrapf(
			ErrStateTypeMismatch,
			"saga definition %q holds %s, runner holds %s", name, def.stateType, want,
		)
	}

	return def, nil
}

// decode turns a stored record into a typed instance.
//
// A record whose definition this process has not registered is decoded anyway.
// Reads are how an operator finds out why a saga is stuck, and a process that
// only reports on sagas — an admin API, a support tool — has no reason to
// register the code that runs them. What it cannot do is check T, so the
// mismatch that Start and Resume refuse is here merely a struct decoded from
// JSON that did not describe it: zero fields rather than a wrong answer.
func (r *StoreRunner[T]) decode(rec *Record) (*Instance[T], error) {
	def, ok := r.registry.lookup(rec.Definition)
	if !ok {
		return decodeInstance[T](rec)
	}

	if want := reflect.TypeFor[T](); def.stateType != want {
		return nil, platformerrors.Wrapf(
			ErrStateTypeMismatch,
			"saga instance %q holds %s, runner holds %s", rec.ID, def.stateType, want,
		)
	}

	return decodeInstance[T](rec)
}

// decodeInstance copies a record into a typed instance, decoding its state.
func decodeInstance[T any](rec *Record) (*Instance[T], error) {
	inst := &Instance[T]{
		StartedAt:    rec.StartedAt,
		UpdatedAt:    rec.UpdatedAt,
		StepNames:    slices.Clone(rec.StepNames),
		ID:           rec.ID,
		Definition:   rec.Definition,
		LastError:    rec.LastError,
		Status:       rec.Status,
		ResumeStatus: rec.ResumeStatus,
		CurrentStep:  rec.CurrentStep,
		Attempts:     rec.Attempts,
	}

	if len(rec.State) > 0 {
		if err := json.Unmarshal(rec.State, &inst.State); err != nil {
			return nil, platformerrors.Wrapf(err, "decoding state of saga instance %q", rec.ID)
		}
	}

	return inst, nil
}

// driftError renders the difference between the steps an instance started with
// and the steps this build registers.
//
// The two lists are both in the message, because "the definition changed" is
// not actionable and "it had four steps and now has five, and the third is
// called something else" is.
func driftError(rec *Record, def *definition) error {
	return platformerrors.Wrapf(
		ErrDefinitionDrift,
		"saga instance %q started with steps %v, definition %q now has %v",
		rec.ID, rec.StepNames, def.name, def.stepNames,
	)
}

// definitionAttr labels a measurement with its definition. Cardinality is
// bounded by the registry, which is a fixed list written at wiring time.
func definitionAttr(name string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(definitionKey, name))
}
