// Package containers provides shared helpers for starting testcontainers
// with uniform retry behavior. It exists so every container builder in the
// repo can opt into the same backoff policy instead of each rolling its own.
//
// Container startup flakes for many non-deterministic reasons — Docker daemon
// cold starts, port conflicts, image pull stalls, transient network blips —
// and a single attempt is too brittle for a large integration test suite.
package containers

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v5/retry"
)

const (
	defaultMaxAttempts  = 5
	defaultInitialDelay = time.Second
)

// RunningTests reports whether RUN_CONTAINER_TESTS=true is set in the
// environment. Container-backed tests across the repo should gate on this
// (typically via `if !containers.RunningTests { t.SkipNow() }`) so a default
// `go test ./...` does not require a Docker daemon. The variable is read once
// at package init.
var RunningTests = strings.TrimSpace(strings.ToLower(os.Getenv("RUN_CONTAINER_TESTS"))) == "true"

// SkipIfNotRunning skips the current test or benchmark (via SkipNow) when
// RunningTests is false. It is the one-line equivalent of `if
// !containers.RunningTests { tb.SkipNow() }` that every container-backed test
// and benchmark in the repo needs. It accepts testing.TB so both *testing.T
// and *testing.B can use it.
func SkipIfNotRunning(tb testing.TB) {
	tb.Helper()
	if !RunningTests {
		tb.SkipNow()
	}
}

// DefaultRetryConfig returns the retry.Config used by StartWithRetry. Callers
// that need bespoke retry behavior can start from this and tweak individual
// fields before calling retry.NewExponentialBackoffPolicy themselves.
func DefaultRetryConfig() retry.Config {
	return retry.Config{
		MaxAttempts:  defaultMaxAttempts,
		InitialDelay: defaultInitialDelay,
		UseJitter:    false,
	}
}

// StartWithRetry invokes start with exponential backoff retry on failure. It
// is a thin wrapper over the retry package so that every container builder in
// the repo gets the same backoff policy for free.
//
// The callback receives the same ctx that was passed in, and is expected to
// return the concrete container type from its module's Run function (e.g.
// *postgres.PostgresContainer, *redis.RedisContainer). Callers handle the
// error themselves — typically via must.NoError(t, err) — so that this helper
// stays decoupled from the testing package.
func StartWithRetry[C any](ctx context.Context, start func(context.Context) (C, error)) (C, error) {
	var container C
	policy := retry.NewExponentialBackoffPolicy(DefaultRetryConfig())
	err := policy.Execute(ctx, func(ctx context.Context) error {
		var startErr error
		container, startErr = start(ctx)
		return startErr
	})
	return container, err
}
