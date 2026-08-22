package timerscfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/timers"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterTimers(T *testing.T) {
	T.Parallel()

	T.Run("resolves a set from its prerequisites", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, clientFor(dialect.Postgres))

		RegisterTimers[string](i)

		set, err := do.Invoke[*timers.Timers[string]](i)
		must.NoError(t, err)
		must.NotNil(t, set)

		test.EqOp(t, "trials", set.Name())
	})

	// A container that registers no observability still has to wire up: absent
	// means noop, and only a registered-but-broken pillar is an error.
	T.Run("wires up without any observability registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, clientFor(dialect.Postgres))

		RegisterTimers[string](i)

		_, err := do.Invoke[*timers.Timers[string]](i)
		must.NoError(t, err)
	})

	// Two key types are two sets, and the injector keeps them apart by type
	// rather than by name.
	T.Run("registers one set per key type", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, clientFor(dialect.Postgres))

		RegisterTimers[string](i)
		RegisterTimers[int64](i)

		byString, err := do.Invoke[*timers.Timers[string]](i)
		must.NoError(t, err)

		byInt, err := do.Invoke[*timers.Timers[int64]](i)
		must.NoError(t, err)

		test.NotNil(t, byString)
		test.NotNil(t, byInt)
	})

	T.Run("surfaces a construction failure", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, clientFor(dialect.Postgres))

		RegisterTimers[string](i)

		_, err := do.Invoke[*timers.Timers[string]](i)
		test.Error(t, err)
	})
}

func TestRegisterWorker(T *testing.T) {
	T.Parallel()

	T.Run("resolves a worker over the registered set", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, clientFor(dialect.Postgres))
		do.ProvideValue[timers.Handler[string]](i, noopHandler)

		RegisterTimers[string](i)
		RegisterWorker[string](i)

		worker, err := do.Invoke[*timers.Worker[string]](i)
		must.NoError(t, err)
		test.NotNil(t, worker)
	})

	// The set is shared rather than rebuilt, so a process that schedules and
	// fires reports one set of metrics rather than two.
	T.Run("shares one set with whoever else resolves it", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, clientFor(dialect.Postgres))
		do.ProvideValue[timers.Handler[string]](i, noopHandler)

		RegisterTimers[string](i)
		RegisterWorker[string](i)

		_, err := do.Invoke[*timers.Worker[string]](i)
		must.NoError(t, err)

		first, err := do.Invoke[*timers.Timers[string]](i)
		must.NoError(t, err)

		second, err := do.Invoke[*timers.Timers[string]](i)
		must.NoError(t, err)

		test.EqOp(t, first, second)
	})
}

// database.Client is registered as the interface rather than a concrete type,
// which is what the provider invokes.
var _ database.Client = clientFor(dialect.Postgres)
