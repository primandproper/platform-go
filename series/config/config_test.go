package seriescfg

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/series"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/distributedlock"
	"github.com/primandproper/primitives-go/v2/distributedlock/memory"
	"github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newClient returns a database.Client that answers Dialect and the clock, which
// is all NewStore reaches for.
func newClient(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{
		DialectFunc:     func() dialect.Dialect { return d },
		CurrentTimeFunc: time.Now,
	}
}

func newLocker(t *testing.T) distributedlock.ScopedLocker {
	t.Helper()

	raw, err := memory.NewLocker()
	must.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	scoped, err := distributedlock.NewScopedLocker(raw)
	must.NoError(t, err)

	return scoped
}

func TestConfig_EnsureDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.EnsureDefaults()
	test.EqOp(t, series.DefaultTablePrefix, cfg.TablePrefix)
	test.EqOp(t, series.DefaultHorizon, cfg.Worker.Horizon)
	test.EqOp(t, series.DefaultLockKey, cfg.Worker.LockKey)

	set := &Config{TablePrefix: "ddb", Worker: series.WorkerConfig{Horizon: time.Hour}}
	set.EnsureDefaults()
	test.EqOp(t, "ddb", set.TablePrefix)
	test.EqOp(t, time.Hour, set.Worker.Horizon)
}

func TestConfig_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a renderable prefix and a defaulted worker", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{TablePrefix: "ddb"}
		cfg.EnsureDefaults()

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{TablePrefix: "ddb_"}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("validates the worker's half", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Worker: series.WorkerConfig{Horizon: time.Hour, Refill: 2 * time.Hour}}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store from a zero config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Postgres))
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), nil, newClient(dialect.Postgres))
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	// A nil concrete store must not come back as a non-nil interface.
	T.Run("a failed build is a nil store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, nil)
		test.ErrorIs(t, err, series.ErrNilDatabaseClient)
		test.Nil(t, store)
	})
}

func TestNewWorker(T *testing.T) {
	T.Parallel()

	T.Run("builds a worker and its store", func(t *testing.T) {
		t.Parallel()

		worker, store, err := NewWorker(t.Context(), &Config{}, newClient(dialect.SQLite), newLocker(t),
			WithWorkerOptions(series.WithWorkerClock(nil)))
		must.NoError(t, err)
		test.NotNil(t, worker)
		test.NotNil(t, store)
	})

	T.Run("a nil locker is refused", func(t *testing.T) {
		t.Parallel()

		_, _, err := NewWorker(t.Context(), &Config{}, newClient(dialect.SQLite), nil)
		test.ErrorIs(t, err, series.ErrNilLocker)
	})

	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		_, _, err := NewWorker(t.Context(), nil, newClient(dialect.SQLite), newLocker(t))
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})
}
