package memory

import (
	"testing"

	"github.com/shoenig/test/must"
)

type benchItem struct {
	Name string `json:"name"`
}

func BenchmarkInMemoryCache(b *testing.B) {
	c, err := NewInMemoryCache[benchItem](nil, nil, nil)
	must.NoError(b, err)

	ctx := b.Context()
	val := &benchItem{Name: "value"}
	must.NoError(b, c.Set(ctx, "key", val))

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
}
