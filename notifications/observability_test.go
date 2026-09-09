package notifications

import (
	"context"
	"sync"
	"testing"

	"github.com/primandproper/primitives-go/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/observability/metrics/mock"
	metricsnoop "github.com/primandproper/primitives-go/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// recordingCounter is one instrument's worth of what the store added to it.
type recordingCounter struct {
	added []int64
	mu    sync.Mutex
}

var _ metrics.Int64Counter = (*recordingCounter)(nil)

func (c *recordingCounter) Add(_ context.Context, value int64, _ ...metric.AddOption) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.added = append(c.added, value)
}

func (c *recordingCounter) observed() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]int64(nil), c.added...)
}

// TestSQLStore_InvalidatedTokenCounter is the one number nothing above this
// layer can see.
//
// A registry's health is the rate at which it sheds dead tokens against the rate
// it takes new ones, and a step change there is a credential or a bundle
// identifier that has moved — every token the deployment holds being classified
// dead, one push at a time. Without the counter that is indistinguishable from
// ordinary churn.
func TestSQLStore_InvalidatedTokenCounter(T *testing.T) {
	T.Parallel()

	newCountingStore := func(t *testing.T, env *storeEnv) (*SQLStore, *recordingCounter) {
		t.Helper()

		counter := &recordingCounter{}
		base := metricsnoop.NewMetricsProvider()

		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(name string, o ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				if name == storeName+"_invalidated_device_tokens" {
					return counter, nil
				}

				return base.NewInt64Counter(name, o...)
			},
		}

		return env.newStore(t, WithStoreMetricsProvider(provider)), counter
	}

	T.Run("counts the rows the provider feedback destroyed", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, counter := newCountingStore(t, env)

		must.NoError(t, env.register(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a")))
		must.NoError(t, store.InvalidateDeviceToken(t.Context(), "ios", "token-a"))

		test.Eq(t, []int64{1}, counter.observed())
	})

	T.Run("counts nothing for a token that was already gone", func(t *testing.T) {
		t.Parallel()

		// The hook is idempotent, and a second prune must not read as a second
		// dead handset — that is what would turn one bad credential into a graph
		// that looks like churn.
		env := newSQLiteEnv(t)
		store, counter := newCountingStore(t, env)

		must.NoError(t, store.InvalidateDeviceToken(t.Context(), "ios", "never-registered"))

		test.Eq(t, []int64{0}, counter.observed())
	})

	T.Run("reports an instrument it could not build", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return nil, errCounterUnavailable
			},
		}

		_, err := NewSQLStore(env.client, WithStoreMetricsProvider(provider))
		test.ErrorIs(t, err, errCounterUnavailable)
	})
}
