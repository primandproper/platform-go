package redis

import (
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v2/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v2/testutils/containers"
	"github.com/primandproper/platform-go/v2/testutils/containers/redistest"

	"github.com/shoenig/test/must"
)

// BenchmarkRedisLocker_AcquireRelease is container-gated: it runs only when
// RUN_CONTAINER_TESTS is set (e.g. `RUN_CONTAINER_TESTS=true make bench`).
func BenchmarkRedisLocker_AcquireRelease(b *testing.B) {
	containers.SkipIfNotRunning(b)

	container := redistest.Start(b)
	cfg := &Config{
		Addresses: []string{redistest.Address(b, container)},
		KeyPrefix: "lock:",
	}

	l, err := NewRedisLocker(cfg, nil, nil, nil, cbnoop.NewCircuitBreaker())
	must.NoError(b, err)

	ctx := b.Context()
	for b.Loop() {
		lock, acqErr := l.Acquire(ctx, "bench-key", time.Minute)
		must.NoError(b, acqErr)
		must.NoError(b, lock.Release(ctx))
	}
}
