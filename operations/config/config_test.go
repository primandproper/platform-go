package operationscfg

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/operations"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("an empty config becomes a usable one", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, operations.DefaultQueueName, cfg.Operations.QueueName)
		test.EqOp(t, operations.DefaultWorkerBatch, cfg.Worker.Batch)
		test.EqOp(t, operations.DefaultWatcherPoll, cfg.Watcher.Poll)

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	// Two names for one queue is a misconfiguration whose only symptom is a
	// table of pending operations that nothing ever claims, so the queue's name
	// is derived rather than configured.
	T.Run("the queue takes its name from the operations config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.Operations.QueueName = "exports"
		cfg.Queue.Name = "something-else"

		cfg.EnsureDefaults()

		test.EqOp(t, "exports", cfg.Queue.Name)
	})

	T.Run("the derived name survives the default", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, operations.DefaultQueueName, cfg.Queue.Name)
	})
}

func TestConfig_Validate(T *testing.T) {
	T.Parallel()

	// Each nested config is validated through a closure because ozzo
	// dereferences a struct-value field before checking
	// ValidatableWithContext, so it would otherwise be skipped entirely — which
	// is the failure mode this test exists to catch.
	for name, mutate := range map[string]func(*Config){
		"a bad operations config": func(c *Config) { c.Operations.Retention = time.Millisecond },
		"a bad worker config":     func(c *Config) { c.Worker.Batch = -1 },
		"a bad watcher config":    func(c *Config) { c.Watcher.Poll = time.Millisecond },
		"a bad queue config":      func(c *Config) { c.Queue.Name = "" },
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{}
			cfg.EnsureDefaults()
			mutate(cfg)

			test.Error(t, cfg.ValidateWithContext(t.Context()))
		})
	}
}

func TestConstructors_nilConfig(T *testing.T) {
	T.Parallel()

	_, err := NewStore(nil, nil)
	test.ErrorIs(T, err, operations.ErrNilConfig)

	_, err = NewQueue(T.Context(), nil, nil)
	test.ErrorIs(T, err, operations.ErrNilConfig)

	_, _, err = NewService(T.Context(), nil, nil, operations.NewRegistry())
	test.ErrorIs(T, err, operations.ErrNilConfig)

	_, err = NewWorker(T.Context(), nil, nil, nil, operations.NewRegistry())
	test.ErrorIs(T, err, operations.ErrNilConfig)

	_, err = NewWatcher(T.Context(), nil, nil)
	test.ErrorIs(T, err, operations.ErrNilConfig)
}

func TestNewWatcher(T *testing.T) {
	T.Parallel()

	// The one constructor here that needs no database, so it is the one that can
	// prove the option translation works end to end.
	T.Run("builds over a store", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		watcher, err := NewWatcher(t.Context(), cfg, stubStore{})

		must.NoError(t, err)
		must.NotNil(t, watcher)

		t.Cleanup(func() { _ = watcher.Close() })
	})

	T.Run("rejects a nil store", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		_, err := NewWatcher(t.Context(), cfg, nil)

		test.ErrorIs(t, err, operations.ErrNilStore)
	})
}
