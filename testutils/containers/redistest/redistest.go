// Package redistest provides a single source of truth for the redis
// testcontainer setup that the redis-backed test suites in this repo all
// duplicate. It owns the RUN_CONTAINER_TESTS feature flag and the
// retry/wait-strategy choices, so each caller only has to express what shape
// it wants the cluster in.
package redistest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform/testutils/containers"

	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
	rediscontainers "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// DefaultImage is the redis image Start launches when no override is provided.
const DefaultImage = "docker.io/redis:7-bullseye"

// Option configures Start.
type Option func(*options)

type options struct {
	image          string
	clusterEnabled bool
}

// WithImage overrides DefaultImage.
func WithImage(image string) Option {
	return func(o *options) { o.image = image }
}

// WithClusterEnabled passes --cluster-enabled yes to redis-server. The node
// still has no slots assigned, but CLUSTER subcommands like CLUSTER KEYSLOT
// become available — useful for tests that want Redis as a hash oracle
// without orchestrating a full multi-node cluster.
func WithClusterEnabled() Option {
	return func(o *options) { o.clusterEnabled = true }
}

// Start brings up a redis container with the shared retry policy and wait
// strategy, and registers termination as a t.Cleanup so callers do not have
// to remember to shut it down. The returned container exposes
// ConnectionString and the rest of the rediscontainers.RedisContainer API.
//
// Failures during startup call t.Fatal via must.NoError. Callers that need
// to handle startup failure differently (e.g. skip the test instead of
// failing it) should use Try.
func Start(t *testing.T, opts ...Option) *rediscontainers.RedisContainer {
	t.Helper()

	container, shutdown, err := Try(t.Context(), opts...)
	must.NoError(t, err)
	must.NotNil(t, container)

	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if sErr := shutdown(shutCtx); sErr != nil {
			t.Logf("redistest: container shutdown error: %v", sErr)
		}
	})
	return container
}

// Try brings up a redis container and returns it along with a shutdown
// closure and any startup error. Prefer Start in tests — Try exists for the
// few callers that want to handle container failures specially (skip vs.
// fail) or that need to bring up Redis outside of a *testing.T context.
//
// The shutdown closure is safe to call even when err is non-nil (it is a
// no-op in that case).
func Try(ctx context.Context, opts ...Option) (container *rediscontainers.RedisContainer, shutdown func(context.Context) error, err error) {
	cfg := options{image: DefaultImage}
	for _, opt := range opts {
		opt(&cfg)
	}

	runOpts := []testcontainers.ContainerCustomizer{
		rediscontainers.WithLogLevel(rediscontainers.LogLevelNotice),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForAll(
			wait.ForListeningPort("6379/tcp"),
			wait.ForLog("Ready to accept connections"),
		)),
	}
	if cfg.clusterEnabled {
		runOpts = append(runOpts, testcontainers.WithCmdArgs("--cluster-enabled", "yes"))
	}

	container, err = containers.StartWithRetry(ctx, func(c context.Context) (*rediscontainers.RedisContainer, error) {
		return rediscontainers.Run(c, cfg.image, runOpts...)
	})
	if err != nil {
		return nil, func(context.Context) error { return nil }, err
	}

	shutdown = func(ctx context.Context) error { return container.Terminate(ctx) }
	return container, shutdown, nil
}

// Address returns the container's host:port string, suitable for
// dial-style config (i.e. ConnectionString with the "redis://" scheme
// trimmed). Most callers want this over ConnectionString.
func Address(t *testing.T, container *rediscontainers.RedisContainer) string {
	t.Helper()

	addr, err := container.ConnectionString(t.Context())
	must.NoError(t, err)
	return strings.TrimPrefix(addr, "redis://")
}
