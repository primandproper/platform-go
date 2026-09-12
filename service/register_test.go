package service

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	oauth2serverstorecfg "github.com/primandproper/platform-go/v14/authentication/oauth2serverstore/config"
	webauthncredentialscfg "github.com/primandproper/platform-go/v14/authentication/webauthncredentials/config"
	"github.com/primandproper/platform-go/v14/operations"
	operationscfg "github.com/primandproper/platform-go/v14/operations/config"
	"github.com/primandproper/platform-go/v14/outbox"
	outboxcfg "github.com/primandproper/platform-go/v14/outbox/config"
	"github.com/primandproper/platform-go/v14/saga"
	sagacfg "github.com/primandproper/platform-go/v14/saga/config"
	"github.com/primandproper/platform-go/v14/workqueue"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	cachecfg "github.com/primandproper/primitives-go/v2/cache/config"
	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/profiling"
	"github.com/primandproper/primitives-go/v2/observability/tracing"

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

// invoked returns the set of service names something has actually built,
// which is how a registration that resolves a dependency is told apart from one
// that quietly builds its own.
func invoked(i do.Injector) map[string]bool {
	services := i.ListInvokedServices()

	names := map[string]bool{}
	for idx := range services {
		names[services[idx].Service] = true
	}

	return names
}

// refusingAuthenticator is an oauth2server.SubjectAuthenticator that identifies
// nobody. The server needs one registered to build, and no test here reaches a
// login.
type refusingAuthenticator struct{}

func (refusingAuthenticator) AuthenticateSubject(context.Context, *http.Request) (*oauth2server.Subject, error) {
	return nil, oauth2server.ErrLoginFailed
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

	T.Run("an operations config registers every loop its own settings configure", func(t *testing.T) {
		t.Parallel()

		// OPERATIONS_WATCHER_* is parsed, defaulted and validated like every
		// other knob on this config, so it has to reach something a process
		// runs. The five registrations are one tier rather than five
		// independently switchable pieces of one, and the watcher is the one
		// this walk used to leave out — an operator who tuned the poll interval
		// got a loop nothing started and no error saying so.
		names := provided(newInjector(t, &Config{Name: "example", Operations: &operationscfg.Config{}}))

		for _, svc := range []string{
			do.NameOf[operations.Store](),
			do.NameOf[*workqueue.Queue[string]](),
			do.NameOf[operations.Service](),
			do.NameOf[*operations.Worker](),
			do.NameOf[*operations.Watcher](),
		} {
			test.MapContainsKey(t, names, svc)
		}
	})

	T.Run("the webauthn relying party is built over the registered ceremony store", func(t *testing.T) {
		t.Parallel()

		// The load-bearing property of the webauthn block, and the one no
		// count of registrations can see. Both halves of the split provide
		// webauthn.SessionStore under one key, so a walk that registered this
		// module's store and this module's relying party would leave the
		// container holding two — the registered one and the one the relying
		// party built privately — with only the second in the ceremonies. Under
		// the cache provider that is a challenge saved into one store and looked
		// for in another, which is a fraction of logins failing on a fleet and
		// nothing at all failing on a laptop.
		//
		// Invoking the store is the evidence: the relying party resolved it
		// rather than building one, so it shows up as invoked without anything
		// in this test asking for it.
		ceremonyTimeout := time.Minute
		cfg := &Config{
			Name: "example",
			WebAuthn: &webauthncredentialscfg.Config{
				Provider: webauthncredentialscfg.ProviderCache,
				RelyingParty: webauthn.Config{
					RPID:            "localhost",
					RPDisplayName:   "Example",
					RPOrigins:       []string{"http://localhost:8080"},
					CeremonyTimeout: ceremonyTimeout,
				},
				Cache: cachecfg.Config{Provider: cachecfg.ProviderMemory},
			},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		relyingParty, err := do.Invoke[*webauthn.RelyingParty](i)
		must.NoError(t, err)
		must.NotNil(t, relyingParty)

		test.MapContainsKey(t, invoked(i), do.NameOf[webauthn.SessionStore]())
	})

	T.Run("the oauth2 server is built over the registered store", func(t *testing.T) {
		t.Parallel()

		// The webauthn subtest's property, a second time, for the second half of
		// the authentication split. The two stores provide one key here too, and
		// a server issuing authorization codes against a store nothing else can
		// read is the same failure wearing a different name.
		cfg := &Config{
			Name: "example",
			OAuth2Server: &oauth2serverstorecfg.Config{
				Provider: oauth2serverstorecfg.ProviderMemory,
				Issuer:   "https://example.com",
			},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)
		// The application's, always: how a deployment identifies a human is not
		// something an environment variable says.
		do.ProvideValue[oauth2server.SubjectAuthenticator](i, refusingAuthenticator{})

		server, err := do.Invoke[*oauth2server.Server](i)
		must.NoError(t, err)
		must.NotNil(t, server)

		test.MapContainsKey(t, invoked(i), do.NameOf[oauth2server.Store]())
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
