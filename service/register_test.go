package service

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/primandproper/platform-go/v13/database"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/profiling"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/outbox"
	outboxcfg "github.com/primandproper/platform-go/v13/outbox/config"
	"github.com/primandproper/platform-go/v13/saga"
	sagacfg "github.com/primandproper/platform-go/v13/saga/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// provided returns the set of service names i can build.
func provided(i do.Injector) map[string]bool {
	services := i.ListProvidedServices()

	names := map[string]bool{}
	for idx := range services {
		names[services[idx].Service] = true
	}

	return names
}

// newInjector registers cfg against a fresh injector, along with the
// context.Context every constructor takes and Register deliberately does not
// invent.
func newInjector(t *testing.T, cfg *Config) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	Register(i, cfg)

	return i
}

func TestRegister(T *testing.T) {
	T.Parallel()

	T.Run("a service configuring nothing is made of nothing but observability", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example"}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		// The four pillars build regardless, each as its own noop.
		logger, err := do.Invoke[logging.Logger](i)
		must.NoError(t, err)
		test.NotNil(t, logger)

		tracerProvider, err := do.Invoke[tracing.Provider](i)
		must.NoError(t, err)
		test.NotNil(t, tracerProvider)

		metricsProvider, err := do.Invoke[metrics.Provider](i)
		must.NoError(t, err)
		test.NotNil(t, metricsProvider)

		profiler, err := do.Invoke[profiling.Provider](i)
		must.NoError(t, err)
		test.NotNil(t, profiler)

		// Everything else reports absent rather than handing back something
		// that looks configured.
		client, err := injection.InvokeOptional[database.Client](i)
		must.NoError(t, err)
		test.Nil(t, client)

		writer, err := injection.InvokeOptional[*outbox.Writer](i)
		must.NoError(t, err)
		test.Nil(t, writer)
	})

	T.Run("registers the config it was given", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example"}
		i := newInjector(t, cfg)

		got, err := do.Invoke[*Config](i)
		must.NoError(t, err)
		test.EqOp(t, cfg, got)
	})

	T.Run("builds a subsystem the config names", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "test.db")
		cfg := &Config{
			Name: "example",
			Database: &databasecfg.Config{
				Provider:        databasecfg.ProviderSQLite,
				ReadConnection:  databasecfg.ConnectionDetails{Database: path},
				WriteConnection: databasecfg.ConnectionDetails{Database: path},
			},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		client, err := do.Invoke[database.Client](i)
		must.NoError(t, err)
		test.NotNil(t, client)
	})

	T.Run("every sub-config field registers something", func(t *testing.T) {
		t.Parallel()

		// The guard against a field being added to Config and never wired into
		// Register: setting one field, and nothing else, has to widen what the
		// injector can build.
		baseline := provided(newInjector(t, &Config{Name: "example"}))

		for _, name := range subConfigFields(t) {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				cfg := &Config{Name: "example"}
				field := reflect.ValueOf(cfg).Elem().FieldByName(name)
				field.Set(reflect.New(field.Type().Elem()))

				var added []string
				for svc := range provided(newInjector(t, cfg)) {
					if !baseline[svc] {
						added = append(added, svc)
					}
				}

				test.SliceNotEmpty(t, added, test.Sprintf("%s is a sub-config Register never reads", name))
			})
		}
	})

	T.Run("the saga outbox publisher needs both ends configured", func(t *testing.T) {
		t.Parallel()

		// saga.NewOutboxPublisher is the seam between two packages, so it is
		// registered only when the config names both of them. With a saga and
		// no outbox the application says what publishes its events.
		publisher := do.NameOf[saga.EventPublisher]()

		sagaOnly := provided(newInjector(t, &Config{Name: "example", Saga: &sagacfg.Config{}}))
		test.MapNotContainsKey(t, sagaOnly, publisher)

		both := provided(newInjector(t, &Config{Name: "example", Saga: &sagacfg.Config{}, Outbox: &outboxcfg.Config{}}))
		test.MapContainsKey(t, both, publisher)
	})
}
