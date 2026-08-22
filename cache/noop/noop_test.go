package noop

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cache"

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

func TestCache_SetIfPresent(T *testing.T) {
	T.Parallel()

	T.Run("refuses, because nothing is ever present", func(t *testing.T) {
		t.Parallel()

		// The one write this cache does not pretend to perform. A nil here would
		// tell a caller its conditional write succeeded against a store that
		// holds nothing, which is the assertion such a caller is relying on.
		c := NewCache[string]()
		v := "value"

		test.ErrorIs(t, c.SetIfPresent(t.Context(), "any-key", &v), cache.ErrNotFound)
	})

	T.Run("refuses even directly after a Set", func(t *testing.T) {
		t.Parallel()

		c := NewCache[string]()
		v := "value"
		must.NoError(t, c.Set(t.Context(), "any-key", &v))

		test.ErrorIs(t, c.SetIfPresent(t.Context(), "any-key", &v), cache.ErrNotFound)
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
