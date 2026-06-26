package noop

import (
	"testing"

	"github.com/primandproper/platform-go/cache"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewCache(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil cache", func(t *testing.T) {
		t.Parallel()

		c := NewCache[string]()
		must.NotNil(t, c)
	})
}

func TestCache_Get(T *testing.T) {
	T.Parallel()

	T.Run("returns ErrNotFound", func(t *testing.T) {
		t.Parallel()

		c := NewCache[string]()
		val, err := c.Get(t.Context(), "any-key")

		test.ErrorIs(t, err, cache.ErrNotFound)
		test.Nil(t, val)
	})
}

func TestCache_Set(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		c := NewCache[string]()
		v := "value"
		err := c.Set(t.Context(), "any-key", &v)

		test.NoError(t, err)
	})
}

func TestCache_Delete(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		c := NewCache[string]()
		err := c.Delete(t.Context(), "any-key")

		test.NoError(t, err)
	})
}

func TestCache_GetMany(T *testing.T) {
	T.Parallel()

	T.Run("returns empty map", func(t *testing.T) {
		t.Parallel()

		c := &Cache[string]{}
		vals, err := c.GetMany(t.Context(), []string{"a", "b"})

		test.NoError(t, err)
		test.NotNil(t, vals)
		test.MapEmpty(t, vals)
	})
}

func TestCache_SetMany(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		c := &Cache[string]{}
		v := "value"
		err := c.SetMany(t.Context(), map[string]*string{"any-key": &v})

		test.NoError(t, err)
	})
}

func TestCache_Ping(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		c := NewCache[string]()
		err := c.Ping(t.Context())

		test.NoError(t, err)
	})
}
