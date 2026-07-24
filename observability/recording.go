package observability

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"sync"

	"github.com/primandproper/platform-go/v6/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
)

var (
	_ Observer  = (*RecordingObserver)(nil)
	_ Operation = (*RecordingOperation)(nil)
)

// TestingT is the minimal subset of *testing.T the assertion helpers need. It
// lets RecordingObserver offer ergonomic assertions without importing the
// testing package into production builds; *testing.T satisfies it structurally.
type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Pillar identifies where an observation landed.
type Pillar uint8

const (
	// PillarBoth marks a value recorded via Set (span and logger).
	PillarBoth Pillar = iota
	// PillarSpan marks a value recorded via SpanOnly.
	PillarSpan
	// PillarLog marks a value recorded via LogOnly.
	PillarLog
)

// Observation is a single recorded attachment, in the order it occurred. Seq is a
// monotonic counter shared across every Operation of the owning observer, so the
// relative order of observations from different operations is recoverable.
type Observation struct {
	Value  any
	Key    string
	Seq    int
	Pillar Pillar
}

// RecordingObserver is an Observer implementation for unit tests. It captures, in
// order, every value attached to the Operations it hands out, so a test can
// assert which fields a unit observed, in what order, and on which pillar. It
// performs no real logging or tracing.
type RecordingObserver struct {
	Operations []*RecordingOperation
	seq        int
	mu         sync.Mutex
}

// NewRecordingObserver builds a RecordingObserver for use in unit tests.
func NewRecordingObserver() *RecordingObserver {
	return &RecordingObserver{}
}

func (o *RecordingObserver) newOperation() *RecordingOperation {
	o.mu.Lock()
	defer o.mu.Unlock()

	op := &RecordingOperation{
		owner:      o,
		Values:     map[string]any{},
		SpanValues: map[string]any{},
		LogValues:  map[string]any{},
	}
	o.Operations = append(o.Operations, op)

	return op
}

// record appends an observation under lock, assigning the next global sequence.
func (o *RecordingObserver) record(op *RecordingOperation, key string, value any, pillar Pillar) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.seq++
	op.Observations = append(op.Observations, Observation{Seq: o.seq, Key: key, Value: value, Pillar: pillar})

	switch pillar {
	case PillarBoth:
		op.Values[key] = value
		op.SpanValues[key] = value
		op.LogValues[key] = value
	case PillarSpan:
		op.SpanValues[key] = value
	case PillarLog:
		op.LogValues[key] = value
	}
}

// Begin returns a RecordingOperation and leaves the context untouched.
func (o *RecordingObserver) Begin(ctx context.Context) (context.Context, Operation) {
	return ctx, o.newOperation()
}

// BeginCustom returns a RecordingOperation and leaves the context untouched.
func (o *RecordingObserver) BeginCustom(ctx context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, Operation) {
	return ctx, o.newOperation()
}

// Logger returns a noop logger.
func (o *RecordingObserver) Logger() logging.Logger {
	return loggingnoop.NewLogger()
}

// Tracer returns a noop tracer.
func (o *RecordingObserver) Tracer() tracing.Tracer {
	return tracing.NewTracerForTest("recording")
}

// Stream returns every observation across all operations, in global order.
func (o *RecordingObserver) Stream() []Observation {
	o.mu.Lock()
	defer o.mu.Unlock()

	var all []Observation
	for _, op := range o.Operations {
		all = append(all, op.Observations...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })

	return all
}

// ObservedInOrder asserts that the given matchers occur, in order, somewhere in
// the global observation stream. Gaps between matches are allowed, so it reads as
// "this happened, then later that happened" across any operations.
func (o *RecordingObserver) ObservedInOrder(t TestingT, matchers ...Matcher) {
	t.Helper()
	assertInOrder(t, o.Stream(), matchers)
}

// ObservedOperationWithKeys asserts that some recorded operation observed all of
// the given keys (on either pillar) and that that operation ended, returning the
// matched operation so callers can make further assertions (e.g. on its Errors).
func (o *RecordingObserver) ObservedOperationWithKeys(t TestingT, keys ...string) *RecordingOperation {
	t.Helper()
	return o.assertObserved(t, "keys", keys, func(observed map[string]any) bool {
		for _, k := range keys {
			if _, ok := observed[k]; !ok {
				return false
			}
		}
		return true
	})
}

// ObservedOperationWithValues asserts that some recorded operation observed all
// of the given values (under any key) and that that operation ended, returning
// the matched operation.
func (o *RecordingObserver) ObservedOperationWithValues(t TestingT, values ...any) *RecordingOperation {
	t.Helper()
	return o.assertObserved(t, "values", values, func(observed map[string]any) bool {
		for _, want := range values {
			if !containsValue(observed, want) {
				return false
			}
		}
		return true
	})
}

// ObservedOperationWithData asserts that some recorded operation observed all of
// the given key/value pairs and that that operation ended, returning the matched
// operation. It does not care whether the operation also recorded an error, so it
// holds equally on success and failure paths; assert on the returned op's Errors
// to verify an error path specifically.
func (o *RecordingObserver) ObservedOperationWithData(t TestingT, data map[string]any) *RecordingOperation {
	t.Helper()
	return o.assertObserved(t, "data", data, func(observed map[string]any) bool {
		for k, want := range data {
			got, ok := observed[k]
			if !ok || !reflect.DeepEqual(got, want) {
				return false
			}
		}
		return true
	})
}

func (o *RecordingObserver) assertObserved(t TestingT, what string, wanted any, match func(map[string]any) bool) *RecordingOperation {
	t.Helper()
	for _, op := range o.Operations {
		if match(op.observed()) {
			if !op.Ended {
				t.Fatalf("recorded operation observed %s %v but did not end", what, wanted)
			}
			return op
		}
	}
	t.Fatalf("no recorded operation observed %s %v (%d operation(s) recorded)", what, wanted, len(o.Operations))

	return nil
}

func containsValue(observed map[string]any, want any) bool {
	for _, got := range observed {
		if reflect.DeepEqual(got, want) {
			return true
		}
	}
	return false
}

// RecordingOperation is the Operation handed out by RecordingObserver. Its
// exported maps record which keys reached which pillar (Values = Set, SpanValues
// = Set + SpanOnly, LogValues = Set + LogOnly), and Observations records the same
// attachments in order for precise, order-sensitive assertions.
type RecordingOperation struct {
	owner *RecordingObserver

	Observations []Observation
	Values       map[string]any
	SpanValues   map[string]any
	LogValues    map[string]any
	// Errors holds every error passed to Error, Acknowledge, or GRPCStatus.
	Errors []error
	// Ended reports whether End was called.
	Ended bool
}

// observed returns the union of everything that reached either pillar.
func (op *RecordingOperation) observed() map[string]any {
	merged := make(map[string]any, len(op.SpanValues)+len(op.LogValues))
	maps.Copy(merged, op.LogValues)
	maps.Copy(merged, op.SpanValues)

	return merged
}

// Observed asserts that each matcher matches some observation in this operation,
// regardless of order.
func (op *RecordingOperation) Observed(t TestingT, matchers ...Matcher) {
	t.Helper()
	for _, m := range matchers {
		if !slices.ContainsFunc(op.Observations, m.match) {
			t.Fatalf("operation did not observe %s", m.desc)
		}
	}
}

// ObservedInOrder asserts that the given matchers occur, in order, within this
// operation. Gaps between matches are allowed.
func (op *RecordingOperation) ObservedInOrder(t TestingT, matchers ...Matcher) {
	t.Helper()
	assertInOrder(t, op.Observations, matchers)
}

// Set records a value to both pillars.
func (op *RecordingOperation) Set(key string, value any) Operation {
	op.owner.record(op, key, value, PillarBoth)

	return op
}

// SetValues records every value via Set.
func (op *RecordingOperation) SetValues(values map[string]any) Operation {
	for k, v := range values {
		op.Set(k, v)
	}

	return op
}

// SpanOnly records a value to the span pillar only.
func (op *RecordingOperation) SpanOnly(key string, value any) Operation {
	op.owner.record(op, key, value, PillarSpan)

	return op
}

// LogOnly records a value to the logger pillar only.
func (op *RecordingOperation) LogOnly(key string, value any) Operation {
	op.owner.record(op, key, value, PillarLog)

	return op
}

// Logger returns a noop logger.
func (op *RecordingOperation) Logger() logging.Logger {
	return loggingnoop.NewLogger()
}

// Span returns nil; recording operations have no real span.
func (op *RecordingOperation) Span() tracing.Span {
	return nil
}

// Error records err and returns it wrapped, matching the production Operation's
// returned-error shape (without logging or tracing).
func (op *RecordingOperation) Error(err error, descriptionFmt string, descriptionArgs ...any) error {
	if err != nil {
		op.Errors = append(op.Errors, err)
	}

	return PrepareError(err, nil, descriptionFmt, descriptionArgs...)
}

// Acknowledge records err.
func (op *RecordingOperation) Acknowledge(err error, _ string, _ ...any) {
	if err != nil {
		op.Errors = append(op.Errors, err)
	}
}

// GRPCStatus records err and returns it as a gRPC status error, matching the
// production Operation's returned-error shape.
func (op *RecordingOperation) GRPCStatus(err error, code codes.Code, descriptionFmt string, descriptionArgs ...any) error {
	if err != nil {
		op.Errors = append(op.Errors, err)
	}

	return PrepareAndLogGRPCStatus(err, nil, nil, code, descriptionFmt, descriptionArgs...)
}

// End marks the operation ended.
func (op *RecordingOperation) End() {
	op.Ended = true
}

// Matcher describes a predicate over a single Observation, used by the ordered
// and per-operation assertion helpers.
type Matcher struct {
	match func(Observation) bool
	desc  string
}

// ObservedKey matches any observation with the given key, whatever its value or
// pillar. Use it when a test knows a key was observed but not the value.
func ObservedKey(key string) Matcher {
	return Matcher{
		desc:  fmt.Sprintf("key %q", key),
		match: func(o Observation) bool { return o.Key == key },
	}
}

// ObservedKeyValue matches an observation with the given key and a value that is
// deeply equal to the given value.
func ObservedKeyValue(key string, value any) Matcher {
	return Matcher{
		desc:  fmt.Sprintf("key %q = %v", key, value),
		match: func(o Observation) bool { return o.Key == key && reflect.DeepEqual(o.Value, value) },
	}
}

// ObservedValue matches an observation with a value deeply equal to the given
// value, under any key.
func ObservedValue(value any) Matcher {
	return Matcher{
		desc:  fmt.Sprintf("value %v", value),
		match: func(o Observation) bool { return reflect.DeepEqual(o.Value, value) },
	}
}

// ObservedKeyFunc matches an observation with the given key whose value satisfies
// the predicate. Use it to assert a key was observed with a value of some shape
// without pinning the exact value.
func ObservedKeyFunc(key string, pred func(value any) bool) Matcher {
	return Matcher{
		desc:  fmt.Sprintf("key %q matching predicate", key),
		match: func(o Observation) bool { return o.Key == key && pred(o.Value) },
	}
}

// OnSpan refines a matcher to require the observation reached the span pillar
// (Set or SpanOnly).
func (m Matcher) OnSpan() Matcher {
	inner := m.match
	return Matcher{
		desc:  m.desc + " (on span)",
		match: func(o Observation) bool { return (o.Pillar == PillarBoth || o.Pillar == PillarSpan) && inner(o) },
	}
}

// OnLog refines a matcher to require the observation reached the logger pillar
// (Set or LogOnly).
func (m Matcher) OnLog() Matcher {
	inner := m.match
	return Matcher{
		desc:  m.desc + " (on log)",
		match: func(o Observation) bool { return (o.Pillar == PillarBoth || o.Pillar == PillarLog) && inner(o) },
	}
}

// assertInOrder asserts the matchers occur as an ordered subsequence of stream.
func assertInOrder(t TestingT, stream []Observation, matchers []Matcher) {
	t.Helper()

	i := 0
	for _, m := range matchers {
		matched := false
		for i < len(stream) {
			o := stream[i]
			i++
			if m.match(o) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("expected to observe %s in order, but it did not occur after the prior matches", m.desc)
		}
	}
}
