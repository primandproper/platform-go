package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/jobs"
	jobscfg "github.com/primandproper/platform-go/v13/jobs/config"
	messagequeuecfg "github.com/primandproper/platform-go/v13/messagequeue/config"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/outbox"
	outboxcfg "github.com/primandproper/platform-go/v13/outbox/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// names returns what each component in a lifecycle slice is called, which is
// the only thing about them these tests are asserting.
func names[T any](components []named[T]) []string {
	out := make([]string, 0, len(components))
	for idx := range components {
		out = append(out, components[idx].name)
	}

	return out
}

// noopQueue names the publisher and consumer that go nowhere, which is what a
// lifecycle test wants: the point is that the loop starts and stops, not that
// anything it claims reaches a broker.
func noopQueue() messagequeuecfg.Config {
	return messagequeuecfg.Config{
		Consumer:  messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop},
		Publisher: messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop},
	}
}

// sqliteConfig is the cheapest real database a test can name.
func sqliteConfig(t *testing.T) *databasecfg.Config {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")

	return &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}
}

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("a service configuring nothing is made of nothing to take down", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example"}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		svc, err := New(newInjector(t, cfg))
		must.NoError(t, err)

		test.SliceEmpty(t, svc.runners)
		test.SliceEmpty(t, svc.servers)
		test.SliceEmpty(t, svc.closers)
		test.SliceEmpty(t, svc.flushes)

		// The pillars are still there, because every service has them and
		// something has to flush them on the way out.
		test.NotNil(t, svc.pillars)
	})

	T.Run("collects the loops the config names in start order", func(t *testing.T) {
		t.Parallel()

		// Start order is what shutdown reverses, so this is the assertion the
		// outbox relay's final cycle depends on: the relay comes up first and
		// therefore closes last, after the pool and the scheduler that write
		// into the database it drains.
		cfg := &Config{
			Name:     "example",
			Database: sqliteConfig(t),
			Outbox: &outboxcfg.Config{
				Queue: noopQueue(),
				Relay: outbox.RelayConfig{TablePrefix: "example"},
			},
			JobsPool: &jobscfg.PoolConfig{
				Queue: noopQueue(),
				Pool:  jobs.PoolConfig{Topic: "jobs"},
			},
			JobsScheduler: &jobscfg.SchedulerConfig{
				Scheduler: jobs.SchedulerConfig{DefaultLeaseTTL: time.Minute},
				Lock:      distributedlockcfg.Config{Provider: distributedlockcfg.MemoryProvider},
			},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)
		do.ProvideValue[jobs.Handler](i, func(context.Context, []byte) error { return nil })

		app := newFakeRunner(&journal{}, "app")

		svc, err := New(i, WithRunners(app))
		must.NoError(t, err)

		test.Eq(t, []string{
			"outbox relay",
			"jobs pool",
			"jobs scheduler",
			"*service.fakeRunner",
		}, names(svc.runners))
	})

	T.Run("collects the clients the config names, database first so it closes last", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example", Database: sqliteConfig(t)}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		svc, err := New(newInjector(t, cfg))
		must.NoError(t, err)

		test.Eq(t, []string{"database client"}, names(svc.closers))
	})

	T.Run("reports a component that was registered and cannot be built", func(t *testing.T) {
		t.Parallel()

		// The distinction the whole composition root is built on. Nobody
		// configuring a database is a service without one; configuring one that
		// will not open is a startup failure, and finding it here rather than
		// on the first request is why New builds eagerly.
		errBuild := platformerrors.New("opening the database")

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Name: "example"})
		do.Provide(i, func(do.Injector) (database.Client, error) { return nil, errBuild })

		svc, err := New(i)
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
		test.Nil(t, svc)
	})

	T.Run("reports observability that was registered and cannot be built", func(t *testing.T) {
		t.Parallel()

		// Registering none is fine — every pillar resolves to its noop. An
		// exporter that cannot reach its collector is not, and must not degrade
		// into the noop that absence gets.
		errPillars := platformerrors.New("reaching the collector")

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Name: "example"})
		do.Provide(i, func(do.Injector) (*observability.Pillars, error) { return nil, errPillars })

		svc, err := New(i)
		must.Error(t, err)
		test.ErrorIs(t, err, errPillars)
		test.Nil(t, svc)
	})

	T.Run("needs a config to have been registered", func(t *testing.T) {
		t.Parallel()

		svc, err := New(do.New())
		must.Error(t, err)
		test.Nil(t, svc)
	})

	T.Run("falls back to the default shutdown budget", func(t *testing.T) {
		t.Parallel()

		// A Config assembled in code and never validated has no budget, and a
		// zero one would make every shutdown an expired deadline.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Name: "example"})

		svc, err := New(i)
		must.NoError(t, err)
		test.EqOp(t, DefaultShutdownTimeout, svc.shutdownTimeout)
	})
}

func TestWithRunners(T *testing.T) {
	T.Parallel()

	T.Run("ignores nil entries", func(t *testing.T) {
		t.Parallel()

		j := &journal{}

		o := newOptions([]Option{WithRunners(newFakeRunner(j, "a"), nil, newFakeRunner(j, "b"))})

		test.SliceLen(t, 2, o.runners)
	})
}
