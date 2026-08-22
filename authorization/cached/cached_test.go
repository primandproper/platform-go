package cached

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authorization"
	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/cache/memory"
	cachemock "github.com/primandproper/platform-go/v13/cache/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

const (
	permRead  authorization.Permission = "read.things"
	permWrite authorization.Permission = "write.things"
)

// countingResolver records how often the inner resolver was consulted, which is
// the only way to tell a hit from a miss from the outside.
type countingResolver struct {
	set   *authorization.PermissionSet
	err   error
	calls atomic.Int64
}

func (c *countingResolver) PermissionsForRoles(context.Context, ...string) (*authorization.PermissionSet, error) {
	c.calls.Add(1)
	if c.err != nil {
		return nil, c.err
	}

	return c.set, nil
}

func (c *countingResolver) Roles(context.Context) ([]authorization.Role, error) {
	c.calls.Add(1)

	return []authorization.Role{{Name: "member"}}, c.err
}

func newMemoryCache(t *testing.T) cache.Cache[authorization.PermissionSet] {
	t.Helper()

	c, err := memory.NewInMemoryCache[authorization.PermissionSet](0)
	must.NoError(t, err)

	return c
}

// newEncodingCache returns a cache that stores the encoded form of a value
// rather than the value itself, through the codec a serializing provider would
// use by default.
//
// The memory cache cannot stand in for this. It hands back the same pointer it
// was given, so a PermissionSet that no codec can encode round-trips through it
// perfectly — which is how a redis deployment came to deny every permission for
// a TTL while every test in this package passed.
func newEncodingCache() *cachemock.CacheMock[authorization.PermissionSet] {
	codec := cache.NewDefaultCodec[authorization.PermissionSet]()

	var mu sync.Mutex
	entries := map[string][]byte{}

	return &cachemock.CacheMock[authorization.PermissionSet]{
		SetFunc: func(_ context.Context, key string, value *authorization.PermissionSet, _ ...cache.WriteOption) error {
			encoded, err := codec.Encode(value)
			if err != nil {
				return err
			}

			mu.Lock()
			defer mu.Unlock()
			entries[key] = encoded

			return nil
		},
		GetFunc: func(_ context.Context, key string) (*authorization.PermissionSet, error) {
			mu.Lock()
			encoded, ok := entries[key]
			mu.Unlock()

			if !ok {
				return nil, cache.ErrNotFound
			}

			return codec.Decode(encoded)
		},
	}
}

func newTestResolver(t *testing.T, inner authorization.PolicyResolver) *Resolver {
	t.Helper()

	r, err := NewResolver(inner, newMemoryCache(t), WithLogger(loggingnoop.NewLogger()))
	must.NoError(t, err)

	return r
}

func TestNewResolver(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil inner resolver", func(t *testing.T) {
		t.Parallel()

		_, err := NewResolver(nil, newMemoryCache(t))

		test.True(t, errors.Is(err, platformerrors.ErrNilInputParameter))
	})

	T.Run("rejects a nil cache", func(t *testing.T) {
		t.Parallel()

		_, err := NewResolver(&countingResolver{}, nil)

		test.True(t, errors.Is(err, platformerrors.ErrNilInputParameter))
	})

	T.Run("a non-positive TTL falls back to the default", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(&countingResolver{}, newMemoryCache(t), WithTTL(-1))
		must.NoError(t, err)

		test.EqOp(t, DefaultTTL, r.ttl)
	})

	T.Run("a positive TTL is honored", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(&countingResolver{}, newMemoryCache(t), WithTTL(90*time.Second))
		must.NoError(t, err)

		test.EqOp(t, 90*time.Second, r.ttl)
	})

	T.Run("a nil option is ignored", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(&countingResolver{}, newMemoryCache(t), nil)

		must.NoError(t, err)
		test.NotNil(t, r)
	})

	T.Run("a nil logger is replaced rather than dereferenced", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(&countingResolver{}, newMemoryCache(t), WithLogger(nil))

		must.NoError(t, err)
		test.NotNil(t, r.o11y.Logger())
	})
}

func TestResolver_Caching(T *testing.T) {
	T.Parallel()

	T.Run("resolves once and serves the rest from cache", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r := newTestResolver(t, inner)

		for range 5 {
			set, err := r.PermissionsForRoles(t.Context(), "member")
			must.NoError(t, err)
			test.True(t, set.Has(permRead))
		}

		test.EqOp(t, int64(1), inner.calls.Load())
	})

	// The key is sorted, so the same roles in a different order share an entry.
	T.Run("role order shares a cache entry", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r := newTestResolver(t, inner)

		_, err := r.PermissionsForRoles(t.Context(), "admin", "member")
		must.NoError(t, err)

		_, err = r.PermissionsForRoles(t.Context(), "member", "admin")
		must.NoError(t, err)

		test.EqOp(t, int64(1), inner.calls.Load())
	})

	T.Run("different role sets do not share an entry", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r := newTestResolver(t, inner)

		_, err := r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)

		_, err = r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)

		test.EqOp(t, int64(2), inner.calls.Load())
	})

	T.Run("no roles short-circuits without consulting anything", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r := newTestResolver(t, inner)

		set, err := r.PermissionsForRoles(t.Context())
		must.NoError(t, err)

		test.True(t, set.IsEmpty())
		test.EqOp(t, int64(0), inner.calls.Load())
	})

	T.Run("cached sets survive the round trip intact", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead, permWrite)}
		r := newTestResolver(t, inner)

		first, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)

		second, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)

		test.EqOp(t, 2, second.Len())
		test.True(t, first.Equal(second))
	})

	// The same assertion against a cache that actually encodes. This is the one
	// that matters: a PermissionSet's only field is unexported, so a cache that
	// stores bytes serves back whatever the codec could see of it. When that was
	// nothing, the hit path below returned an empty set — a silent denial of
	// every permission, indistinguishable from a working cache — which is
	// exactly what the memory-backed sibling above cannot detect.
	T.Run("cached sets survive a serializing cache intact", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead, permWrite)}
		r, err := NewResolver(inner, newEncodingCache(), WithLogger(loggingnoop.NewLogger()))
		must.NoError(t, err)

		miss, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)

		hit, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)

		// Resolved once: the second call is a hit, so it is the decoded set
		// being asserted on rather than the inner resolver's own pointer.
		test.EqOp(t, int64(1), inner.calls.Load())

		test.EqOp(t, 2, hit.Len())
		test.True(t, hit.Has(permRead))
		test.True(t, hit.Has(permWrite))
		test.True(t, miss.Equal(hit))
	})

	T.Run("propagates an inner resolution error", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("policy store is down")
		r := newTestResolver(t, &countingResolver{err: sentinel})

		_, err := r.PermissionsForRoles(t.Context(), "member")

		test.True(t, errors.Is(err, sentinel))
	})

	T.Run("Roles is not cached", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r := newTestResolver(t, inner)

		_, err := r.Roles(t.Context())
		must.NoError(t, err)

		_, err = r.Roles(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(2), inner.calls.Load())
	})
}

// Authorization must not fail because a cache did. The inner resolver is still
// authoritative and still reachable, so degrading to it is both correct and the
// only answer that keeps requests flowing.
func TestResolver_CacheFaultsDegradeToMisses(T *testing.T) {
	T.Parallel()

	faultyCache := func(getErr, setErr error) cache.Cache[authorization.PermissionSet] {
		return &cachemock.CacheMock[authorization.PermissionSet]{
			GetFunc: func(context.Context, string) (*authorization.PermissionSet, error) {
				return nil, getErr
			},
			SetFunc: func(context.Context, string, *authorization.PermissionSet, ...cache.WriteOption) error {
				return setErr
			},
		}
	}

	T.Run("an unreadable entry falls back to the resolver", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r, err := NewResolver(inner, faultyCache(errors.New("decode failure"), nil),
			WithLogger(loggingnoop.NewLogger()))
		must.NoError(t, err)

		set, err := r.PermissionsForRoles(t.Context(), "member")

		must.NoError(t, err)
		test.True(t, set.Has(permRead))
		test.EqOp(t, int64(1), inner.calls.Load())
	})

	T.Run("an unwritable cache still returns the right answer", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r, err := NewResolver(inner, faultyCache(cache.ErrNotFound, errors.New("cache is full")),
			WithLogger(loggingnoop.NewLogger()))
		must.NoError(t, err)

		set, err := r.PermissionsForRoles(t.Context(), "member")

		must.NoError(t, err)
		test.True(t, set.Has(permRead))
	})
}

// recordingResolver swaps in a RecordingObserver so a test can read which
// values the Resolver attached, and on which pillar. The Observer is built
// inside NewResolver rather than injected, because a test hook on the
// production constructor would be the only caller that ever passed one.
func recordingResolver(
	t *testing.T,
	inner authorization.PolicyResolver,
	c cache.Cache[authorization.PermissionSet],
) (*Resolver, *observability.RecordingObserver) {
	t.Helper()

	r, err := NewResolver(inner, c, WithLogger(loggingnoop.NewLogger()))
	must.NoError(t, err)

	rec := observability.NewRecordingObserver()
	r.o11y = rec

	return r, rec
}

func TestResolver_ObservesCacheOutcome(T *testing.T) {
	T.Parallel()

	T.Run("a miss then a hit", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r, rec := recordingResolver(t, inner, newMemoryCache(t))

		_, err := r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)
		_, err = r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)

		must.SliceLen(t, 2, rec.Operations)
		test.EqOp(t, outcomeMiss, rec.Operations[0].SpanValues[keys.AuthorizationCacheOutcomeKey])
		test.EqOp(t, outcomeHit, rec.Operations[1].SpanValues[keys.AuthorizationCacheOutcomeKey])

		// The roles reach both pillars; the key is verbose enough that it is
		// deliberately kept off the span.
		roles, isSlice := rec.Operations[0].Values[keys.AuthorizationRolesKey].([]string)
		must.True(t, isSlice)
		test.Eq(t, []string{"member"}, roles)
		_, onSpan := rec.Operations[0].SpanValues["cache.key"]
		test.False(t, onSpan)
		_, inLog := rec.Operations[0].LogValues["cache.key"]
		test.True(t, inLog)
	})

	T.Run("a read fault records a fault without failing the span", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		faulty := &cachemock.CacheMock[authorization.PermissionSet]{
			GetFunc: func(context.Context, string) (*authorization.PermissionSet, error) {
				return nil, errors.New("decode failure")
			},
			SetFunc: func(context.Context, string, *authorization.PermissionSet, ...cache.WriteOption) error {
				return nil
			},
		}

		r, rec := recordingResolver(t, inner, faulty)

		set, err := r.PermissionsForRoles(t.Context(), "member")

		must.NoError(t, err)
		test.True(t, set.Has(permRead))
		must.SliceLen(t, 1, rec.Operations)
		test.EqOp(t, outcomeFault, rec.Operations[0].SpanValues[keys.AuthorizationCacheOutcomeKey])
		// The request succeeded, so the span must not be marked failed — a cache
		// blip has to stay distinguishable from a denial in a trace search.
		test.SliceEmpty(t, rec.Operations[0].Errors)
	})

	T.Run("an inner failure does fail the span", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{err: errors.New("database is down")}
		r, rec := recordingResolver(t, inner, newMemoryCache(t))

		_, err := r.PermissionsForRoles(t.Context(), "member")

		must.Error(t, err)
		must.SliceLen(t, 1, rec.Operations)
		test.SliceLen(t, 1, rec.Operations[0].Errors)
	})
}

func TestResolver_FaultCountersAreConstructed(T *testing.T) {
	T.Parallel()

	T.Run("both counters are ready to use", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r, err := NewResolver(inner, newMemoryCache(t),
			WithLogger(loggingnoop.NewLogger()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)
		must.NoError(t, err)

		must.NotNil(t, r.readFaultsCounter)
		must.NotNil(t, r.writeFaultsCounter)
	})
}

var errBoom = errors.New("boom")

func TestNewResolver_MetricsFailure(T *testing.T) {
	T.Parallel()

	for _, failOn := range []string{
		serviceName + "_read_faults",
		serviceName + "_write_faults",
	} {
		T.Run("surfaces a failure creating "+failOn, func(t *testing.T) {
			t.Parallel()

			mp := &metricsmock.ProviderMock{
				NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
					if name == failOn {
						return nil, errBoom
					}

					return &metricsmock.Int64CounterMock{}, nil
				},
			}

			inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
			_, err := NewResolver(inner, newMemoryCache(t), WithMetricsProvider(mp))

			test.ErrorIs(t, err, errBoom)
		})
	}
}

func TestResolver_Invalidate(T *testing.T) {
	T.Parallel()

	T.Run("drops an exact role set", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r := newTestResolver(t, inner)

		_, err := r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)

		must.NoError(t, r.Invalidate(t.Context(), "member"))

		_, err = r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)

		test.EqOp(t, int64(2), inner.calls.Load())
	})

	T.Run("invalidating nothing is a no-op", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t, &countingResolver{set: authorization.NewPermissionSet()})

		test.NoError(t, r.Invalidate(t.Context()))
	})

	// Unlike a read fault, a failed invalidation is surfaced: the caller asked
	// for stale authority to stop being served and it did not happen.
	T.Run("surfaces a delete failure", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("cache is unreachable")
		r, err := NewResolver(
			&countingResolver{set: authorization.NewPermissionSet(permRead)},
			&cachemock.CacheMock[authorization.PermissionSet]{
				DeleteFunc: func(context.Context, string) error { return sentinel },
			},
			WithLogger(loggingnoop.NewLogger()),
		)
		must.NoError(t, err)

		test.ErrorIs(t, r.Invalidate(t.Context(), "member"), sentinel)
	})

	T.Run("InvalidateAll makes prior entries unreachable", func(t *testing.T) {
		t.Parallel()

		inner := &countingResolver{set: authorization.NewPermissionSet(permRead)}
		r := newTestResolver(t, inner)

		_, err := r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)

		r.InvalidateAll()

		_, err = r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)

		test.EqOp(t, int64(2), inner.calls.Load())
	})
}

func TestResolver_Key(T *testing.T) {
	T.Parallel()

	// Entries written by an older binary must become unreachable rather than
	// mis-decoded when the encoding changes, so the version rides the key.
	T.Run("carries the format version", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t, &countingResolver{})

		key, err := r.key([]string{"member"})
		must.NoError(t, err)
		test.True(t, strings.HasPrefix(key, keyFormatVersion+":"))
	})

	T.Run("changes with the generation", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t, &countingResolver{})

		before, err := r.key([]string{"member"})
		must.NoError(t, err)

		r.InvalidateAll()

		after, err := r.key([]string{"member"})
		must.NoError(t, err)

		test.NotEq(t, before, after)
	})

	T.Run("does not mutate the caller's slice", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t, &countingResolver{})

		roles := []string{"zebra", "aardvark"}
		_, err := r.key(roles)
		must.NoError(t, err)

		test.Eq(t, []string{"zebra", "aardvark"}, roles)
	})

	// A NUL in a role name would make {"a\x00b"} and {"a", "b"} collide on one
	// key, and a collision here serves one principal another's permissions.
	T.Run("rejects a role name containing NUL", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t, &countingResolver{})

		_, err := r.key([]string{"admin\x00member"})
		test.Error(t, err)
	})
}
