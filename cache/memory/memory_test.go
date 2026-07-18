package memory

import (
	"testing"

	"github.com/primandproper/platform-go/v5/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	exampleKey = "example"
)

type example struct {
	Name string `json:"name"`
}

// newRecordingCache builds an in-memory cache with a RecordingObserver swapped
// in, so a test can drive a method and assert that it opened and ended an
// operation.
func newRecordingCache(t *testing.T) (*inMemoryCacheImpl[example], *observability.RecordingObserver) {
	t.Helper()

	c, err := NewInMemoryCache[example](nil, nil, nil)
	must.NoError(t, err)

	obs := observability.NewRecordingObserver()
	impl := c.(*inMemoryCacheImpl[example])
	impl.o11y = obs

	return impl, obs
}

func Test_newInMemoryCache(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		actual, err := NewInMemoryCache[example](nil, nil, nil)
		must.NoError(t, err)
		test.NotNil(t, actual)
	})
}

func Test_inMemoryCacheImpl_Get(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](nil, nil, nil)
		must.NoError(t, err)

		expected := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, expected))

		actual, err := c.Get(ctx, exampleKey)
		test.Eq(t, expected, actual)
		test.NoError(t, err)
	})

	T.Run("observes operation", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, obs := newRecordingCache(t)

		expected := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, expected))

		actual, err := c.Get(ctx, exampleKey)
		test.Eq(t, expected, actual)
		test.NoError(t, err)

		// The cache methods attach no values, so assert the operation
		// lifecycle: Get opened and ended an operation with no errors.
		op := obs.ObservedOperationWithKeys(t)
		must.SliceEmpty(t, op.Errors)
	})
}

func Test_inMemoryCacheImpl_Set(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](nil, nil, nil)
		must.NoError(t, err)

		test.MapLen(t, 0, c.(*inMemoryCacheImpl[example]).cache)
		test.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))
		test.MapLen(t, 1, c.(*inMemoryCacheImpl[example]).cache)
	})
}

func Test_inMemoryCacheImpl_Delete(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](nil, nil, nil)
		must.NoError(t, err)

		test.MapLen(t, 0, c.(*inMemoryCacheImpl[example]).cache)
		test.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))
		test.MapLen(t, 1, c.(*inMemoryCacheImpl[example]).cache)
		test.NoError(t, c.Delete(ctx, exampleKey))
		test.MapLen(t, 0, c.(*inMemoryCacheImpl[example]).cache)
	})
}

func Test_inMemoryCacheImpl_GetMany(T *testing.T) {
	T.Parallel()

	T.Run("returns only hits", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](nil, nil, nil)
		must.NoError(t, err)

		hit := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, "hit", hit))

		bc := c.(*inMemoryCacheImpl[example])
		out, getErr := bc.GetMany(ctx, []string{"hit", "miss"})
		test.NoError(t, getErr)
		test.MapLen(t, 1, out)
		test.Eq(t, hit, out["hit"])
	})

	T.Run("empty keys", func(t *testing.T) {
		t.Parallel()

		c, err := NewInMemoryCache[example](nil, nil, nil)
		must.NoError(t, err)

		out, getErr := c.(*inMemoryCacheImpl[example]).GetMany(t.Context(), nil)
		test.NoError(t, getErr)
		test.MapLen(t, 0, out)
	})
}

func Test_inMemoryCacheImpl_SetMany(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](nil, nil, nil)
		must.NoError(t, err)

		bc := c.(*inMemoryCacheImpl[example])
		test.MapLen(t, 0, bc.cache)

		test.NoError(t, bc.SetMany(ctx, map[string]*example{
			"a": {Name: "a"},
			"b": {Name: "b"},
		}))
		test.MapLen(t, 2, bc.cache)
	})
}

func Test_inMemoryCacheImpl_Ping(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		c, err := NewInMemoryCache[example](nil, nil, nil)
		must.NoError(t, err)
		test.NoError(t, c.Ping(t.Context()))
	})
}
