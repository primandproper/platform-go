package healthcheck

import (
	"context"
	"sync"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// serviceName scopes this package's spans, logger, and instrument names.
const serviceName = "healthcheck"

// defaultCheckTimeout bounds each individual health check so one slow/hung
// component can't stall the whole probe endpoint. Checks must honor ctx cancellation.
const defaultCheckTimeout = 5 * time.Second

// Span and log attribute keys.
const (
	componentKey      = "healthcheck.component"
	statusKey         = "healthcheck.status"
	previousStatusKey = "healthcheck.previous_status"
	componentCountKey = "healthcheck.component_count"
	downCountKey      = "healthcheck.components_down"
)

// Status represents component health status.
type Status string

const (
	StatusUp   Status = "up"
	StatusDown Status = "down"
)

// ComponentResult is the result of a single component check.
type ComponentResult struct {
	Status  Status `json:"status"`
	Message string `json:"message,omitempty"`
}

// Result is the aggregate result of all health checks.
type Result struct {
	Components map[string]ComponentResult `json:"components"`
	Status     Status                     `json:"status"`
}

// Checker performs a health check for a component.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// Registry holds checkers and runs them.
type Registry interface {
	Register(checker Checker)
	CheckAll(ctx context.Context) *Result
}

var _ Registry = (*CheckerRegistry)(nil)

// CheckerRegistry is the Registry checkers register with, and is what
// NewRegistry returns — a caller that has chosen this registry can depend on
// that choice rather than on the seam.
//
// It remembers what each component reported last time, which is the whole
// difference between a probe endpoint and a signal: the endpoint answers
// whoever asked, once, while a transition between up and down is a thing that
// happened to the service and is worth a log line and a counter increment
// exactly once, no matter how many times the probe repeats the question
// afterwards.
type CheckerRegistry struct {
	o11y observability.Observer

	transitionCounter metrics.Int64Counter
	downGauge         metrics.Int64Gauge

	// last is the status each component reported on the previous run. A
	// component missing from it has not been checked yet, and its first result
	// is reported as a transition — including the ordinary one, so a service
	// says what it found each dependency in at startup rather than only saying
	// something once one of them breaks.
	last map[string]Status

	checkers []Checker

	mu sync.RWMutex
}

// NewRegistry returns a Registry that runs the checkers registered with it.
//
// Give it observability. Without it the registry still answers correctly and
// tells nobody: a component that goes down produces a field in a JSON body read
// by whatever polled the probe, and a service flapping in and out of a load
// balancer's rotation leaves no trace in any of the three pillars.
func NewRegistry(opts ...Option) (*CheckerRegistry, error) {
	o := newOptions(opts)

	r := &CheckerRegistry{
		checkers: make([]Checker, 0),
		last:     make(map[string]Status),
		o11y:     observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error
	if r.transitionCounter, err = mp.NewInt64Counter(serviceName + "_component_transitions"); err != nil {
		return nil, platformerrors.Wrap(err, "creating healthcheck transition counter")
	}

	// A gauge rather than a counter, because the question an operator asks of
	// it is "how much of this process is broken right now" — and because a
	// process that flaps produces a flat counter and a visibly square wave.
	if r.downGauge, err = mp.NewInt64Gauge(serviceName + "_components_down"); err != nil {
		return nil, platformerrors.Wrap(err, "creating healthcheck down component gauge")
	}

	return r, nil
}

// Register adds a checker to the registry.
func (r *CheckerRegistry) Register(checker Checker) {
	if checker == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.checkers = append(r.checkers, checker)
}

// CheckAll runs all registered checkers and returns the aggregate result.
func (r *CheckerRegistry) CheckAll(ctx context.Context) *Result {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	r.mu.RLock()
	checkers := make([]Checker, len(r.checkers))
	copy(checkers, r.checkers)
	r.mu.RUnlock()

	outcomes := r.runChecks(ctx, checkers)

	result := &Result{
		Status:     StatusUp,
		Components: make(map[string]ComponentResult, len(outcomes)),
	}

	var down int64

	for i := range outcomes {
		o := &outcomes[i]
		result.Components[o.name] = o.result

		if o.result.Status == StatusDown {
			result.Status = StatusDown
			down++
		}
	}

	op.SpanOnly(componentCountKey, len(outcomes)).
		SpanOnly(downCountKey, down).
		SpanOnly(statusKey, string(result.Status))

	r.downGauge.Record(ctx, down)

	r.reportTransitions(ctx, op, outcomes)

	return result
}

// outcome is one checker's answer, carrying the error the check returned so a
// transition to down can be logged with the reason rather than with the message
// the JSON body already carries.
type outcome struct {
	err      error
	name     string
	previous Status
	result   ComponentResult
}

// runChecks runs the checks concurrently, each under its own timeout, so a
// single slow check bounds only itself instead of serially stalling the probe.
func (r *CheckerRegistry) runChecks(ctx context.Context, checkers []Checker) []outcome {
	outcomes := make([]outcome, len(checkers))

	var wg sync.WaitGroup

	for i, c := range checkers {
		wg.Go(func() {
			outcomes[i] = r.check(ctx, c)
		})
	}

	wg.Wait()

	return outcomes
}

// check runs one checker under its own span and timeout.
func (r *CheckerRegistry) check(ctx context.Context, c Checker) outcome {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	name := c.Name()
	op.SpanOnly(componentKey, name)

	checkCtx, cancel := context.WithTimeout(ctx, defaultCheckTimeout)
	defer cancel()

	err := c.Check(checkCtx)
	if err == nil {
		op.SpanOnly(statusKey, string(StatusUp))

		return outcome{name: name, result: ComponentResult{Status: StatusUp}}
	}

	// Recorded on the span but not logged here. A component that is down stays
	// down across every probe until somebody fixes it, and a line per check
	// buries the moment it broke under thousands of repetitions of the fact
	// that it is still broken. reportTransitions logs the moment instead.
	op.SpanOnly(statusKey, string(StatusDown))
	tracing.AttachErrorToSpan(op.Span(), "health check failed", err)

	return outcome{name: name, err: err, result: ComponentResult{Status: StatusDown, Message: err.Error()}}
}

// reportTransitions logs and counts the components whose status changed since
// the previous run, and only those.
func (r *CheckerRegistry) reportTransitions(ctx context.Context, op observability.Operation, outcomes []outcome) {
	changed := r.recordStatuses(outcomes)

	for i := range changed {
		c := &changed[i]

		logger := op.Logger().
			WithValue(componentKey, c.name).
			WithValue(statusKey, string(c.result.Status))
		if c.previous != "" {
			logger = logger.WithValue(previousStatusKey, string(c.previous))
		}

		if c.result.Status == StatusDown {
			logger.Error("health check component went down", c.err)
		} else {
			logger.Info("health check component is up")
		}

		r.transitionCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String(componentKey, c.name),
			attribute.String(statusKey, string(c.result.Status)),
		))
	}
}

// recordStatuses folds this run's outcomes into the remembered ones and returns
// the subset that changed, each carrying the status it changed from.
func (r *CheckerRegistry) recordStatuses(outcomes []outcome) []outcome {
	r.mu.Lock()
	defer r.mu.Unlock()

	var changed []outcome

	for i := range outcomes {
		o := &outcomes[i]

		previous, seen := r.last[o.name]
		if seen && previous == o.result.Status {
			continue
		}

		r.last[o.name] = o.result.Status

		transitioned := *o
		transitioned.previous = previous
		changed = append(changed, transitioned)
	}

	return changed
}

// Check runs registry's checkers, tolerating a nil registry: a caller that was
// given no registry has nothing to check, and nothing to check is up.
//
// It exists so the servers that serve probes from an optional registry neither
// invent that answer separately nor build a real one from inside a handler
// factory that has nowhere to report a construction failure.
func Check(ctx context.Context, registry Registry) *Result {
	if registry == nil {
		return &Result{Status: StatusUp, Components: map[string]ComponentResult{}}
	}

	return registry.CheckAll(ctx)
}
