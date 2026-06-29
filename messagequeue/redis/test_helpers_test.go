package redis

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v2/testutils/containers/redistest"
)

func BuildContainerBackedRedisConfigForTest(t *testing.T) (config *Config, shutdownFunc func(context.Context) error, err error) {
	t.Helper()
	return BuildContainerBackedRedisConfig(t.Context())
}

func BuildContainerBackedRedisConfig(ctx context.Context) (config *Config, shutdownFunc func(context.Context) error, err error) {
	container, shutdown, err := redistest.Try(ctx)
	if err != nil {
		return nil, nil, err
	}

	addr, err := container.ConnectionString(ctx)
	if err != nil {
		_ = shutdown(ctx)
		return nil, nil, err
	}

	cfg := &Config{
		QueueAddresses: []string{trimRedisScheme(addr)},
	}

	return cfg, shutdown, nil
}

func trimRedisScheme(addr string) string {
	const scheme = "redis://"
	if len(addr) >= len(scheme) && addr[:len(scheme)] == scheme {
		return addr[len(scheme):]
	}
	return addr
}
