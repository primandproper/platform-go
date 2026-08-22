package retrycfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/retry"
	"github.com/primandproper/platform-go/v13/retry/noop"
)

const (
	// ProviderExponential retries with exponential backoff and optional jitter.
	// It is what an unset provider selects, since a retry policy that does not
	// retry is the surprising answer rather than the default one.
	ProviderExponential = "exponential"
	// ProviderNoop runs an operation exactly once. Disabling retries is
	// supported, but it has to be named.
	ProviderNoop = "noop"
)

// providers are every provider this package implements, plus the empty string,
// which selects exponential backoff. ValidateWithContext and NewPolicy both
// read it, so the set validation accepts cannot drift from the set dispatch
// handles.
var providers = []any{"", ProviderExponential, ProviderNoop}

// NewPolicy provides a retry.Policy from config.
//
// An unrecognized provider is an error rather than a policy that quietly does
// not retry, which is indistinguishable from a working one until the first
// transient failure.
func NewPolicy(ctx context.Context, cfg *Config, opts ...Option) (retry.Policy, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating retry config")
	}

	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ProviderExponential, "":
		// Built into a variable and returned only once its error is known nil:
		// returning the call directly would turn a nil
		// *ExponentialBackoffPolicy into a non-nil retry.Policy on the error
		// path.
		policy, err := NewExponentialBackoffPolicy(*cfg, opts...)
		if err != nil {
			return nil, err
		}

		return policy, nil
	case ProviderNoop:
		return noop.NewPolicy(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "retry provider %q", cfg.Provider)
	}
}
