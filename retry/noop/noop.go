// Package noop is the retry.Policy that does not retry: Execute calls the
// operation once and returns whatever the operation returned.
//
// The error a caller sees is therefore the first attempt's, unwrapped and
// unaggregated — no backoff was slept, no jitter strategy consulted, no attempt
// budget consumed. That is precisely what is wanted when the operation is not
// idempotent, when a layer above already owns the retry budget, or in a test
// where real backoff would trade correctness for wall-clock time.
//
// Holding no schedule, it is also the one policy that cannot be misconfigured:
// there is no interval to get wrong and no ceiling to exceed.
// retry/config builds it for the "noop" provider name.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/retry"
)

var _ retry.Policy = (*policy)(nil)

// policy executes the operation exactly once with no retries.
type policy struct{}

// Execute runs the operation once.
func (n *policy) Execute(ctx context.Context, operation func(ctx context.Context) error) error {
	return operation(ctx)
}

// NewPolicy returns a Policy that never retries.
func NewPolicy() retry.Policy {
	return &policy{}
}
