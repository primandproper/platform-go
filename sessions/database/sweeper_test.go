package database

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v14/sessions"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/v2/observability/metrics/mock"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestWithSweeper(T *testing.T) {
	T.Parallel()

	// The wall clock is deliberate rather than the fake one the other tests
	// use: inside a synctest bubble clock.NewClock reads the bubble's time, so
	// the sweeper's ticker advances with time.Sleep and needs no test double.
	T.Run("removes expired rows with no read to discover them", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			backend, err := NewBackend[principal](&Config{}, client, WithSweeper(t.Context(), 10*time.Second))
			must.NoError(t, err)

			must.NoError(t, backend.Create(t.Context(), "id-1", wallRecord(), time.Minute))
			test.EqOp(t, 1, rowCount(t, backend))

			// Past the row's deadline and across at least one tick.
			time.Sleep(time.Minute + 10*time.Second)
			synctest.Wait()

			test.EqOp(t, 0, rowCount(t, backend))
		})
	})

	T.Run("leaves live rows alone", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			backend, err := NewBackend[principal](&Config{}, client, WithSweeper(t.Context(), time.Second))
			must.NoError(t, err)

			must.NoError(t, backend.Create(t.Context(), "id-1", wallRecord(), time.Hour))

			time.Sleep(time.Minute)
			synctest.Wait()

			test.EqOp(t, 1, rowCount(t, backend))
		})
	})

	// The goroutine's life is the caller's to bound. A sweeper that outlived
	// the scope that started it would keep a client alive after Close.
	T.Run("stops when its context is done", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())

			client := newTestClient(t)

			backend, err := NewBackend[principal](&Config{}, client, WithSweeper(ctx, time.Second))
			must.NoError(t, err)

			cancel()
			synctest.Wait()

			must.NoError(t, backend.Create(t.Context(), "id-1", wallRecord(), time.Second))

			time.Sleep(time.Minute)
			synctest.Wait()

			// Still there, because nothing is sweeping any more.
			test.EqOp(t, 1, rowCount(t, backend))
		})
	})

	T.Run("starts nothing without a context or an interval", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithSweeper(nil, time.Second)}) //nolint:staticcheck // deliberate nil context
		must.Nil(t, o.sweepCtx)

		o = newOptions([]Option{WithSweeper(context.Background(), 0)})
		must.Nil(t, o.sweepCtx)
	})

	T.Run("a second sweeper ends the context the first derived", func(t *testing.T) {
		t.Parallel()

		// The context is the option's rather than the caller's, so the copy a
		// replaced option left behind is the option's to end — nothing is
		// running on it, and it would otherwise sit on the caller's context
		// until that one was cancelled.
		o := newOptions(nil)
		WithSweeper(t.Context(), time.Second)(o)

		superseded := o.sweepCtx

		WithSweeper(t.Context(), time.Minute)(o)

		test.ErrorIs(t, superseded.Err(), context.Canceled)
		test.NoError(t, o.sweepCtx.Err())
		test.EqOp(t, time.Minute, o.sweepInterval)
	})
}

// wallRecord is a record stamped from the wall clock, for the synctest bubbles
// above where the fake clock is not in play.
func wallRecord() *sessions.Record[principal] {
	now := time.Now().UTC().Truncate(time.Microsecond)

	return &sessions.Record[principal]{
		CreatedAt:  now,
		LastSeenAt: now,
		Data:       &principal{UserID: "u_1"},
		Holder:     sessions.Holder{Scope: tenancy.Global()},
		Version:    1,
	}
}

// rowCount counts what is actually in the table, which is what a sweeper
// changes and a Load cannot see.
func rowCount(t *testing.T, backend *Backend[principal]) int {
	t.Helper()

	var count int
	must.NoError(t, backend.db.Writer().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sessions").Scan(&count))

	return count
}

// Close is this backend's shutdown and nobody else's.
//
// The client it was built over belongs to whoever opened it, which in a service
// is the composition root and every other store in the process. What Close has
// to release is the one thing this backend started for itself.
func TestBackend_Close(T *testing.T) {
	T.Parallel()

	T.Run("leaves the caller's client open", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client := newTestClient(t)

		backend, err := NewBackend[principal](&Config{}, client)
		must.NoError(t, err)

		must.NoError(t, backend.Close())

		// The pool still answers, which is what the audit log, the webhook
		// queue and everything else sharing this handle depend on.
		var count int
		must.NoError(t, client.Writer().
			QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&count))
		test.EqOp(t, 0, count)

		// A backend built afterwards writes through it, and the closed one
		// reads what that backend wrote.
		other, err := NewBackend[principal](&Config{}, client)
		must.NoError(t, err)

		must.NoError(t, other.Create(ctx, "id-1", wallRecord(), time.Minute))

		got, err := backend.Load(ctx, "id-1")
		must.NoError(t, err)
		must.NotNil(t, got)
		test.EqOp(t, "u_1", got.Data.UserID)
	})

	// The wall clock is deliberate rather than the fake one the other tests
	// use: inside a synctest bubble clock.NewClock reads the bubble's time, so
	// the sweeper's ticker advances with time.Sleep and needs no test double.
	T.Run("stops the sweeper", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			backend, err := NewBackend[principal](&Config{}, client, WithSweeper(t.Context(), time.Second))
			must.NoError(t, err)

			must.NoError(t, backend.Close())
			synctest.Wait()

			must.NoError(t, backend.Create(t.Context(), "id-1", wallRecord(), time.Second))

			time.Sleep(time.Minute)
			synctest.Wait()

			// Still there, well past its deadline and past several ticks,
			// because nothing is sweeping any more. Left running it would also
			// be sweeping through a client the caller may since have closed,
			// logging a failure every interval for the rest of the process.
			test.EqOp(t, 1, rowCount(t, backend))
		})
	})

	T.Run("is safe more than once, and on a backend with no sweeper", func(t *testing.T) {
		t.Parallel()

		backend, _ := newTestBackend(t)
		test.Nil(t, backend.stopSweeper)

		must.NoError(t, backend.Close())
		must.NoError(t, backend.Close())

		swept, err := NewBackend[principal](&Config{}, newTestClient(t), WithSweeper(t.Context(), time.Minute))
		must.NoError(t, err)

		must.NoError(t, swept.Close())
		must.NoError(t, swept.Close())
	})
}

// A constructor that fails after WithSweeper has run ends the context it
// derived.
//
// The cancel is kept by the option rather than made in NewBackend — see
// WithSweeper for why — so between the option running and the backend taking
// ownership of it there are three returns that could leave a live child of the
// caller's context with nothing left to end it.
func TestNewBackend_sweeperContextOnTheErrorPath(T *testing.T) {
	T.Parallel()

	// derivedBy applies WithSweeper and hands back the context it made, which
	// is otherwise the options struct's alone.
	derivedBy := func(ctx context.Context, into *context.Context) Option {
		return func(o *options) {
			WithSweeper(ctx, time.Minute)(o)
			*into = o.sweepCtx
		}
	}

	T.Run("a failed constructor cancels it", func(t *testing.T) {
		t.Parallel()

		var derived context.Context

		backend, err := NewBackend[principal](&Config{}, newTestClient(t),
			derivedBy(t.Context(), &derived),
			WithMetricsProvider(failingMetricsProvider(serviceName+"_rows_swept")))

		test.Nil(t, backend)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "creating swept session rows counter")

		must.NotNil(t, derived)
		test.ErrorIs(t, derived.Err(), context.Canceled)
	})

	T.Run("a backend that was built keeps it", func(t *testing.T) {
		t.Parallel()

		// The other half, and the reason the cancel is conditional: a defer
		// that ended the context unconditionally would hand back a backend
		// whose sweeper stopped before it ticked once.
		var derived context.Context

		backend, err := NewBackend[principal](&Config{}, newTestClient(t), derivedBy(t.Context(), &derived))
		must.NoError(t, err)

		must.NotNil(t, derived)
		test.NoError(t, derived.Err())

		must.NoError(t, backend.Close())
		test.ErrorIs(t, derived.Err(), context.Canceled)
	})
}

// failingMetricsProvider serves the noop provider's instruments for every name
// except failOn, which reports an error.
func failingMetricsProvider(failOn string) metrics.Provider {
	base := metricsnoop.NewMetricsProvider()
	boom := platformerrors.New("instrument unavailable")

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failOn {
				return nil, boom
			}

			return base.NewInt64Counter(name, opts...)
		},
	}
}
