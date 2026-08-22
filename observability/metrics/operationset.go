package metrics

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"go.opentelemetry.io/otel/metric"
)

// Instrument name suffixes. They are constants rather than format strings at
// each call site because the convention only works if everybody spells it the
// same way: a dashboard that charts errors over requests across a deployment
// joins on these suffixes, and a package that named its counter `_failures`
// looks instrumented from the inside while being absent from every panel.
const (
	requestsSuffix = "_requests"
	errorsSuffix   = "_errors"
	latencySuffix  = "_latency_ms"
)

// OperationSet is the three instruments nearly every component in this module
// records: how often it was asked to do something, how often that failed, and
// how long it took.
//
// The three belong together because none of them means anything alone. A
// climbing error count is a crisis or a rounding error depending on the request
// count beside it; a latency histogram with no request count cannot distinguish
// "fast" from "idle". Twenty-five constructors had built this trio by hand, and
// what varied between them was only which suffixes they chose — which is the
// one thing that should not vary.
//
// The zero value is usable and records nothing, so a component that has no
// metrics provider does not need a nil check at each call site.
type OperationSet struct {
	// Requests counts attempts — incremented before the work runs, not after.
	// Counting successes here and calling it "requests" is what makes
	// requests-minus-errors a number with no interpretation.
	Requests Int64Counter

	// Errors counts the attempts that failed.
	Errors Int64Counter

	// Latency records how long an attempt took, in milliseconds. Milliseconds
	// because that is what every other histogram in this module reports, and a
	// unit that varies per package is a unit nobody can chart together.
	Latency Float64Histogram
}

// NewOperationSet builds the trio under name, which is the component's
// instrument prefix: "cache", "llm_openai", "webhooks_dispatcher".
//
// A nil provider resolves to the noop one, so a caller that was given no
// metrics provider gets a working set rather than an error.
func NewOperationSet(mp Provider, name string) (*OperationSet, error) {
	mp = EnsureMetricsProvider(mp)

	var (
		set OperationSet
		err error
	)

	if set.Requests, err = mp.NewInt64Counter(name + requestsSuffix); err != nil {
		return nil, platformerrors.Wrapf(err, "creating %s request counter", name)
	}

	if set.Errors, err = mp.NewInt64Counter(name + errorsSuffix); err != nil {
		return nil, platformerrors.Wrapf(err, "creating %s error counter", name)
	}

	if set.Latency, err = mp.NewFloat64Histogram(name + latencySuffix); err != nil {
		return nil, platformerrors.Wrapf(err, "creating %s latency histogram", name)
	}

	return &set, nil
}

// Attempt counts one attempt.
func (s *OperationSet) Attempt(ctx context.Context, opts ...metric.AddOption) {
	if s != nil && s.Requests != nil {
		s.Requests.Add(ctx, 1, opts...)
	}
}

// Failed counts one failed attempt.
//
// It does not also count an attempt. Errors is a subset of Requests, not a
// series beside it, which is what makes their ratio the error rate rather than
// a number that exceeds one.
func (s *OperationSet) Failed(ctx context.Context, opts ...metric.AddOption) {
	if s != nil && s.Errors != nil {
		s.Errors.Add(ctx, 1, opts...)
	}
}
