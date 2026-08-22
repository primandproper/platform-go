package workqueuecfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/workqueue"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterQueue(T *testing.T) {
	T.Parallel()

	T.Run("resolves a queue from its prerequisites", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, clientFor(dialect.Postgres))

		RegisterQueue[string](i)

		q, err := do.Invoke[*workqueue.Queue[string]](i)
		must.NoError(t, err)
		must.NotNil(t, q)
		t.Cleanup(func() { _ = q.Close(t.Context()) })

		test.EqOp(t, "jobs", q.Name())
	})

	// A container that registers no observability still has to wire up: absent
	// means noop, and only a registered-but-broken pillar is an error.
	T.Run("wires up without any observability registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, clientFor(dialect.Postgres))

		RegisterQueue[string](i)

		q, err := do.Invoke[*workqueue.Queue[string]](i)
		must.NoError(t, err)
		t.Cleanup(func() { _ = q.Close(t.Context()) })
	})

	// Two key types are two queues, and the injector keeps them apart by type
	// rather than by name.
	T.Run("registers one queue per key type", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, clientFor(dialect.Postgres))

		RegisterQueue[string](i)
		RegisterQueue[int64](i)

		byString, err := do.Invoke[*workqueue.Queue[string]](i)
		must.NoError(t, err)
		t.Cleanup(func() { _ = byString.Close(t.Context()) })

		byInt, err := do.Invoke[*workqueue.Queue[int64]](i)
		must.NoError(t, err)
		t.Cleanup(func() { _ = byInt.Close(t.Context()) })

		test.NotNil(t, byString)
		test.NotNil(t, byInt)
	})

	T.Run("surfaces a construction failure", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &workqueue.Config{})
		do.ProvideValue(i, clientFor(dialect.Postgres))

		RegisterQueue[string](i)

		_, err := do.Invoke[*workqueue.Queue[string]](i)
		test.Error(t, err)
	})
}

// database.Client is registered as the interface rather than a concrete type,
// which is what the provider invokes.
var _ database.Client = clientFor(dialect.Postgres)
