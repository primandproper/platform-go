package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetcfg "github.com/primandproper/platform-go/v14/authentication/passwordreset/config"
	"github.com/primandproper/platform-go/v14/comments"
	commentscfg "github.com/primandproper/platform-go/v14/comments/config"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	mediaregistrycfg "github.com/primandproper/platform-go/v14/mediaregistry/config"
	"github.com/primandproper/platform-go/v14/outbox"
	outboxcfg "github.com/primandproper/platform-go/v14/outbox/config"
	settingscfg "github.com/primandproper/platform-go/v14/settings/config"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	distributedlockcfg "github.com/primandproper/primitives-go/v2/distributedlock/config"
	emailcfg "github.com/primandproper/primitives-go/v2/email/config"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/httpclient"
	"github.com/primandproper/primitives-go/v2/jobs"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"
	messagequeuecfg "github.com/primandproper/primitives-go/v2/messagequeue/config"
	"github.com/primandproper/primitives-go/v2/observability"

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
// noopPublisher is the publisher half alone, which is what a component that
// only publishes declares. The outbox relay is one.
func noopPublisher() messagequeuecfg.MessageQueueConfig {
	return messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop}
}

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
		// outbox relay's last cycle depends on: the relay comes up first and
		// therefore closes last, after the pool and the scheduler that write
		// into the database it drains.
		cfg := &Config{
			Name:     "example",
			Database: sqliteConfig(t),
			Outbox: &outboxcfg.Config{
				Queue: noopPublisher(),
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

	T.Run("reports a provider that was registered, has no lifecycle, and cannot be built", func(t *testing.T) {
		t.Parallel()

		// EMAIL_PROVIDER=sendgird, the typo the README calls a production
		// incident that looks like a healthy process. The emailer holds no
		// connection and has no loop, so nothing in the lifecycle slots asks
		// for one — which is how this used to boot clean, pass readiness, and
		// fail at the first send.
		//
		// The config is assembled in code and not validated, which is the
		// service this catches: Config.ValidateWithContext rejects an unknown
		// provider too, and a caller who skipped it is exactly the caller whose
		// process would otherwise have started.
		cfg := &Config{
			Name:       "example",
			HTTPClient: &httpclient.Config{},
			Email:      &emailcfg.Config{Provider: "sendgird"},
		}

		svc, err := New(newInjector(t, cfg))
		must.Error(t, err)
		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
		test.Nil(t, svc)
	})

	T.Run("builds every type the config named, lifecycle or not", func(t *testing.T) {
		t.Parallel()

		// The guarantee itself, read off the container rather than off a list
		// written here: everything Register registered has been invoked by the
		// time New returns, and most of it — the stores above all — is in none
		// of the lifecycle slots.
		cfg := &Config{
			Name:          "example",
			Database:      sqliteConfig(t),
			Identity:      &identitycfg.Config{TablePrefix: storePrefix},
			Comments:      &commentscfg.Config{TablePrefix: storePrefix},
			Settings:      &settingscfg.Config{TablePrefix: storePrefix},
			MediaRegistry: &mediaregistrycfg.Config{TablePrefix: storePrefix},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)
		do.ProvideValue(i, comments.Targets{comments.TargetType("recipe"): {Description: "a recipe"}})

		svc, err := New(i)
		must.NoError(t, err)

		// Nothing here has a shutdown obligation but the database client, which
		// is the point: of the five subsystems this config names, the walk that
		// orders shutdown reaches exactly one.
		test.Eq(t, []string{"database client"}, names(svc.closers))

		test.SliceEmpty(t, notInvoked(t, i), test.Sprint("every registered type is built by New"))
	})

	T.Run("reports an application type the config named and nobody registered", func(t *testing.T) {
		t.Parallel()

		// The same config as above, less the one value that is genuinely the
		// application's: comments.Targets is what the store resolves a comment's
		// subject through, and no environment variable can express it.
		//
		// It is a characterization rather than a regression: do recovers a
		// provider's panic into an error, so this was already an error when the
		// config packages called do.MustInvoke. What it pins is the promise New
		// makes rather than the mechanism underneath it — that the failure names
		// both ends, the subsystem and the registration it wanted — so a later
		// change to either one has to keep answering the question a consumer
		// actually has, which is which line they did not write.
		cfg := &Config{
			Name:     "example",
			Database: sqliteConfig(t),
			Comments: &commentscfg.Config{TablePrefix: storePrefix},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		svc, err := New(newInjector(t, cfg))
		must.Error(t, err)
		test.Nil(t, svc)

		// Both ends: the subsystem that could not be built, and what it wanted.
		test.StrContains(t, err.Error(), do.NameOf[comments.Store]())
		test.StrContains(t, err.Error(), do.NameOf[comments.Targets]())
	})

	T.Run("reports a password reset block whose application registered no mailer", func(t *testing.T) {
		t.Parallel()

		// The mailer is the application's by definition, so a block that names
		// the flow and a container that supplies no way to deliver a link is a
		// boot failure naming it rather than a surface that fails every request.
		cfg := &Config{
			Name:          "example",
			Database:      sqliteConfig(t),
			Identity:      &identitycfg.Config{TablePrefix: storePrefix},
			PasswordReset: &passwordresetcfg.Config{TablePrefix: storePrefix},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)
		do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())

		svc, err := New(i)
		must.Error(t, err)
		test.Nil(t, svc)

		test.StrContains(t, err.Error(), do.NameOf[*passwordreset.Service]())
		test.StrContains(t, err.Error(), do.NameOf[passwordreset.Mailer]())
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

// notInvoked returns the names Register registered with i that nothing has
// built, which after a successful New is what the guarantee says is empty.
func notInvoked(t *testing.T, i do.Injector) []string {
	t.Helper()

	registered, err := do.Invoke[registrations](i)
	must.NoError(t, err)
	must.SliceNotEmpty(t, registered.names)

	built := invoked(i)

	var missing []string

	for _, name := range registered.names {
		if !built[name] {
			missing = append(missing, name)
		}
	}

	return missing
}
