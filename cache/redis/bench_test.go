package redis

import (
	"testing"

	"github.com/primandproper/platform-go/v13/testutils/containers/redistest"

	"github.com/shoenig/test/must"
)

type benchCacheItem struct {
	Name string `json:"name"`
}

// BenchmarkRedisCache is container-gated: it runs only when RUN_CONTAINER_TESTS
// is set (e.g. `RUN_CONTAINER_TESTS=true make bench`).
func BenchmarkRedisCache(b *testing.B) {
	container := redistest.Start(b)
	cfg := &Config{Addresses: []string{redistest.Address(b, container)}}

	c, err := NewRedisCache[benchCacheItem](cfg, 0, nil)
	must.NoError(b, err)

	ctx := b.Context()
	val := &benchCacheItem{Name: "value"}
	must.NoError(b, c.Set(ctx, "key", val))

	keys := []string{"k1", "k2", "k3"}
	items := map[string]*benchCacheItem{"k1": val, "k2": val, "k3": val}
	must.NoError(b, c.SetMany(ctx, items))

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
			_, _ = c.GetMany(ctx, keys)
		}
	})

	b.Run("SetMany", func(b *testing.B) {
		for b.Loop() {
			_ = c.SetMany(ctx, items)
		}
	})
}
