package noop

import (
	"context"

	"github.com/primandproper/platform-go/v4/retry"
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
