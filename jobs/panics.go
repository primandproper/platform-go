package jobs

import (
	"context"
	stderrors "errors"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/panicking"

	"go.opentelemetry.io/otel/metric"
)

// containedPanic turns a panic that panicking.Contain caught into one of this
// package's sentinels, counting it and putting its stack on the span. Anything
// that is not a contained panic is returned untouched.
//
// The Pool and the Scheduler both need this and neither can borrow the other's:
// they count against different instruments and report different sentinels. What
// they must not differ on is the order — the stack has to reach the span before
// the PanicError is replaced, since the wrapped sentinel no longer carries it —
// which is why the sequence lives here once rather than in two deferred blocks
// that could drift.
//
// Call it from a deferred closure over a named error result:
//
//	defer func() { err = containedPanic(ctx, op, err, p.panicCounter, p.topicAttr, ErrHandlerPanicked) }()
func containedPanic(
	ctx context.Context,
	op observability.Operation,
	err error,
	counter metrics.Int64Counter,
	attrs metric.MeasurementOption,
	sentinel error,
) error {
	pe, ok := stderrors.AsType[*panicking.PanicError](err)
	if !ok {
		return err
	}

	counter.Add(ctx, 1, attrs)
	op.SpanOnly(panicStackKey, string(pe.Stack))

	return platformerrors.Wrapf(sentinel, "%v", pe.Value)
}
