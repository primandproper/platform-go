package redis

import (
	"testing"

	"github.com/primandproper/platform-go/v4/cache"
	"github.com/primandproper/platform-go/v4/testutils/containers"
	"github.com/primandproper/platform-go/v4/testutils/containers/redistest"

	"github.com/shoenig/test/must"
)

type benchCacheItem struct {
	Name string `json:"name"`
}

// BenchmarkRedisCache is container-gated: it runs only when RUN_CONTAINER_TESTS
// is set (e.g. `RUN_CONTAINER_TESTS=true make bench`).
func BenchmarkRedisCache(b *testing.B) {
	containers.SkipIfNotRunning(b)

	container := redistest.Start(b)
	cfg := &Config{QueueAddresses: []string{redistest.Address(b, container)}}

	c, err := NewRedisCache[benchCacheItem](cfg, 0, nil, nil, nil, nil)
	must.NoError(b, err)

	bc, ok := c.(cache.BatchCache[benchCacheItem])
	must.True(b, ok)

	ctx := b.Context()
	val := &benchCacheItem{Name: "value"}
	must.NoError(b, c.Set(ctx, "key", val))

	keys := []string{"k1", "k2", "k3"}
	items := map[string]*benchCacheItem{"k1": val, "k2": val, "k3": val}
	must.NoError(b, bc.SetMany(ctx, items))

	b.Run("Get", func(b *testing.B) {
		for b.Loop() {
			_, _ = c.Get(ctx, "key")
		}
	})

	b.Run("Set", func(b *testing.B) {
		for b.Loop() {
			_ = c.Set(ctx, "key", val)
		}
	})

	b.Run("GetMany", func(b *testing.B) {
		for b.Loop() {
			_, _ = bc.GetMany(ctx, keys)
		}
	})

	b.Run("SetMany", func(b *testing.B) {
		for b.Loop() {
			_ = bc.SetMany(ctx, items)
		}
	})
}
