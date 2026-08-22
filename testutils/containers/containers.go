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

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
)

const (
	defaultMaxAttempts  = 5
	defaultInitialDelay = time.Second

	// DefaultShutdownTimeout bounds how long Run waits for a container to
	// terminate. Termination happens on a fresh context, so a test that has
	// already blown its own deadline still gets its container reaped.
	DefaultShutdownTimeout = 30 * time.Second
)

// RunningTests reports whether RUN_CONTAINER_TESTS=true is set in the
// environment. Container-backed tests across the repo should gate on this
// (typically via `if !containers.RunningTests { t.SkipNow() }`) so a default
// `go test ./...` does not require a Docker daemon. The variable is read once
// at package init.
var RunningTests = strings.TrimSpace(strings.ToLower(os.Getenv("RUN_CONTAINER_TESTS"))) == "true"

// SkipIfNotRunning skips the current test or benchmark (via SkipNow) when
// RunningTests is false, or when -short is set. It is the one-line equivalent
// of the gate every container-backed test and benchmark in the repo needs. It
// accepts testing.TB so both *testing.T and *testing.B can use it.
//
// -short skips even with RUN_CONTAINER_TESTS=true, and deliberately so: -short
// is the caller saying they want a fast answer, and pulling images and standing
// up a database is the slowest thing this repo does. A gate that honored only
// the environment variable would leave -short meaningless for exactly the tests
// it exists to skip.
func SkipIfNotRunning(tb testing.TB) {
	tb.Helper()

	if !RunningTests || testing.Short() {
		tb.SkipNow()
	}
}

// RequireRunning is SkipIfNotRunning's counterpart for a suite whose only
// backend is the thing the container provides. It honors -short — that is the
// caller asking for a fast answer — and consults nothing else, so a host with
// no Docker daemon fails the test rather than passing an empty run.
//
// Both gates are legitimate and which one applies is the suite's call, not this
// package's. RUN_CONTAINER_TESTS defaulting to skip is right for a library
// whose consumers may have no daemon; it is wrong for a service whose Postgres
// tests are the only coverage its Postgres backend has, where skipping quietly
// reports success for code nothing ran.
func RequireRunning(tb testing.TB) {
	tb.Helper()

	if testing.Short() {
		tb.SkipNow()
	}
}

// DefaultRetryConfig returns the retrycfg.Config used by StartWithRetry. Callers
// that need bespoke retry behavior can start from this and tweak individual
// fields before calling retrycfg.NewExponentialBackoffPolicy themselves.
func DefaultRetryConfig() retrycfg.Config {
	return retrycfg.Config{
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

	policy, err := retrycfg.NewExponentialBackoffPolicy(DefaultRetryConfig(), retrycfg.WithName("container_start"))
	if err != nil {
		return container, err
	}

	err = policy.Execute(ctx, func(ctx context.Context) error {
		var startErr error
		container, startErr = start(ctx)
		return startErr
	})
	return container, err
}

// readinessRetryConfig backs PingUntilReady. It is deliberately not
// DefaultRetryConfig: that policy exists to absorb a slow image pull and starts
// at a full second, whereas a server that has already logged its readiness line
// is usually a few hundred milliseconds away, and the delays here add up to
// roughly ten seconds of patience for the stragglers.
func readinessRetryConfig() retrycfg.Config {
	return retrycfg.Config{
		MaxAttempts:  10,
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     2 * time.Second,
		Multiplier:   2,
		UseJitter:    false,
	}
}

// PingWithRetry calls ping until it succeeds, for a caller with no testing.TB —
// a TestMain, or anything else assembling a container outside a test. It is
// PingUntilReady's body with the failure handed back instead of taken.
//
// See PingUntilReady for why the first ping is retried at all.
func PingWithRetry(ctx context.Context, ping func(context.Context) error) error {
	if ping == nil {
		return platformerrors.New("containers: PingWithRetry requires a non-nil ping")
	}

	policy, err := retrycfg.NewExponentialBackoffPolicy(readinessRetryConfig(), retrycfg.WithName("container_readiness"))
	if err != nil {
		return err
	}

	return policy.Execute(ctx, ping)
}

// PingUntilReady calls ping until it succeeds, failing tb if it never does.
//
// A container's readiness log is not the same event as its server accepting
// connections. Both the postgres and MySQL entrypoints run an init pass against
// a temporary server and then restart, and MySQL's real server logs a readiness
// line from the X plugin before the one it logs for port 3306 — so a wait
// strategy counting log occurrences can release the test at any of several
// moments, one of which is a socket that is about to close. What that looks
// like downstream is a "bad connection" or "unexpected EOF" from the very first
// statement, on a container that is perfectly healthy a second later.
//
// Retrying the first ping is the fix that does not depend on reading logs:
// whichever occurrence the wait strategy matched, the pool is not handed to a
// test until a query has actually round-tripped. database/sql discards a
// connection that failed this way, so each attempt dials anew.
func PingUntilReady(tb testing.TB, ctx context.Context, ping func(context.Context) error) {
	tb.Helper()

	if ping == nil {
		tb.Fatal("containers: PingUntilReady requires a non-nil ping")
	}

	must.NoError(tb, PingWithRetry(ctx, ping))
}

// RunOption configures Run.
type RunOption func(*runOptions)

type runOptions struct {
	required bool
}

// Required swaps Run's gate from SkipIfNotRunning to RequireRunning, so a host
// without a Docker daemon fails the test instead of skipping it. Pass it when
// the container is the only backend under test and a skip would report success
// for code nothing ran.
func Required() RunOption {
	return func(o *runOptions) { o.required = true }
}

func newRunOptions(opts []RunOption) *runOptions {
	cfg := &runOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// Terminable is the teardown half of the testcontainers container API — the only
// thing Run needs in order to own a container's lifecycle. Every module container
// type (*postgres.PostgresContainer, *redis.RedisContainer, …) satisfies it, as
// does testcontainers.Container itself.
type Terminable interface {
	Terminate(ctx context.Context, opts ...testcontainers.TerminateOption) error
}

// Run starts a container and hands it to fn, owning everything around the closure
// so the test body only has to say what it wants done with a live container. It is
// to container-backed tests what database.RunInTransaction is to transactions: the
// caller supplies the work, the helper supplies the lifecycle.
//
// Everything a container-backed test in this repo has to remember is handled here:
//
//   - the RUN_CONTAINER_TESTS gate, so a bare `go test ./...` skips instead of
//     demanding a Docker daemon.
//   - startup via StartWithRetry, so the shared backoff policy applies, and a
//     startup failure fails the test rather than yielding a nil container.
//   - termination, once, whatever fn does — return, t.Fatal, or panic.
//
// fn receives the container itself along with tb.Context(); it is not handed a
// shutdown closure, because it does not own shutdown.
//
// Termination is registered with tb.Cleanup rather than deferred until fn returns.
// That distinction is load-bearing: a closure that registers parallel subtests
// returns *before* those subtests execute, and a deferred Terminate would pull the
// container out from under them.
//
// The flip side is that the container lives until the end of tb, not the end of fn,
// so call Run from the narrowest test that needs the container rather than hoisting
// it up to a parent that runs unrelated work afterwards.
func Run[C Terminable](tb testing.TB, start func(ctx context.Context) (C, error), fn func(ctx context.Context, container C), opts ...RunOption) {
	tb.Helper()

	if start == nil || fn == nil {
		tb.Fatal("containers: Run requires a non-nil start and fn")
	}

	if newRunOptions(opts).required {
		RequireRunning(tb)
	} else {
		SkipIfNotRunning(tb)
	}

	ctx := tb.Context()

	container, err := StartWithRetry(ctx, start)
	must.NoError(tb, err)

	tb.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DefaultShutdownTimeout)
		defer cancel()

		if terminateErr := container.Terminate(shutdownCtx); terminateErr != nil {
			tb.Logf("containers: terminating container: %v", terminateErr)
		}
	})

	fn(ctx, container)
}
