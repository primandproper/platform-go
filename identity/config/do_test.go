package identitycfg

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/database"
	databasecfg "github.com/primandproper/platform-go/v14/database/config"
	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/observability"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func testDBClient(t *testing.T) database.Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	client, err := databasecfg.NewDatabase(t.Context(), &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}, nil)
	must.NoError(t, err)

	return client
}

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterStore(i)

		store, err := do.Invoke[identity.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("with no observability registered", func(t *testing.T) {
		t.Parallel()

		// A container that registers no pillars still wires up: absent is fine,
		// and only a registered one that fails to build is an error.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{TablePrefix: "ddb"})

		RegisterStore(i)

		store, err := do.Invoke[identity.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("surfaces a bad config", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{TablePrefix: "has space"})

		RegisterStore(i)

		_, err := do.Invoke[identity.Store](i)
		must.Error(t, err)
	})
}

// container registers everything the three registrations below need, so each
// case says only what it is about.
func container(t *testing.T) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue[database.Client](i, testDBClient(t))
	do.ProvideValue(i, &Config{})

	RegisterStore(i)
	RegisterService(i)
	RegisterServer(i)

	return i
}

func TestRegisterService(T *testing.T) {
	T.Parallel()

	T.Run("resolves a service", func(t *testing.T) {
		t.Parallel()

		svc, err := do.Invoke[*identity.Service](container(t))
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("resolves without hooks registered", func(t *testing.T) {
		t.Parallel()

		// The asymmetry with the principal extractor below, and the point of
		// resolving Hooks softly: a container that registers none is an
		// application with nothing to commit beside an identity write, which is
		// a configuration rather than a hole.
		i := container(t)

		_, err := do.Invoke[identity.Hooks](i)
		test.Error(t, err, test.Sprint("this case is only meaningful with no Hooks registered"))

		svc, err := do.Invoke[*identity.Service](i)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("uses the hooks the container holds", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue[identity.Hooks](i, identity.NoopHooks{})

		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*identity.Service](i)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("a hooks provider that fails to build fails the service", func(t *testing.T) {
		t.Parallel()

		// The other half of resolving Hooks softly. Absent is a configuration;
		// registered-and-broken is not, and a Service that ran the noop in its
		// place would commit every identity write with none of the audit and
		// outbox companions the consumer registered hooks to get — silently,
		// which is the failure InvokePillars draws the same line against.
		boom := errors.New("audit sink unreachable")

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.Provide(i, func(do.Injector) (identity.Hooks, error) { return nil, boom })

		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*identity.Service](i)
		test.Nil(t, svc)
		test.ErrorIs(t, err, boom)
	})

	T.Run("a hooks provider that needs an unregistered dependency fails the service", func(t *testing.T) {
		t.Parallel()

		// The same failure by a different route, and the one that used to slip
		// through. A consumer's hooks provider invokes the audit recorder it
		// was built to write to; when nothing registered one, do reports the
		// miss with the same not-found sentinel an absent Hooks carries, and a
		// lookup that read the sentinel as "nobody registered hooks" handed
		// the Service the noop — the outcome this registration exists to
		// refuse.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.Provide(i, func(i do.Injector) (identity.Hooks, error) {
			if _, err := do.Invoke[*unregisteredRecorder](i); err != nil {
				return nil, err
			}

			return identity.NoopHooks{}, nil
		})

		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*identity.Service](i)
		test.Nil(t, svc)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
	})
}

// unregisteredRecorder stands in for a dependency a consumer's hooks provider
// asks the container for and nothing registered.
type unregisteredRecorder struct{}

func TestRegisterServer(T *testing.T) {
	T.Parallel()

	T.Run("resolves a server", func(t *testing.T) {
		t.Parallel()

		i := container(t)
		do.ProvideValue[identitygrpc.PrincipalExtractor](i,
			func(context.Context) (identitygrpc.Principal, bool) { return nil, false })

		srv, err := do.Invoke[*identitygrpc.Server](i)
		must.NoError(t, err)
		test.NotNil(t, srv)
	})

	T.Run("refuses to build without a principal extractor", func(t *testing.T) {
		t.Parallel()

		// The container fails loudly rather than handing back a server that
		// answers every read with the zero scope. It is the one dependency here
		// with no safe default.
		srv, err := do.Invoke[*identitygrpc.Server](container(t))
		test.Nil(t, srv)
		test.Error(t, err)
	})
}

// TestAPillarsProviderThatFailsToBuildFailsEveryRegistration is the line
// observability.InvokePillars draws, and all three registrations here are on the
// same side of it: absent observability is a configuration and wires up silently
// — the cases above — while a registered provider that cannot be built is a
// failure. A component that fell back to the noops for it would look configured
// and export nothing, which is the state nobody finds until an incident.
func TestAPillarsProviderThatFailsToBuildFailsEveryRegistration(T *testing.T) {
	T.Parallel()

	boom := errors.New("the collector is unreachable")

	build := func(t *testing.T) do.Injector {
		t.Helper()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.Provide(i, func(do.Injector) (*observability.Pillars, error) { return nil, boom })
		do.ProvideValue[identitygrpc.PrincipalExtractor](i,
			func(context.Context) (identitygrpc.Principal, bool) { return nil, false })

		RegisterStore(i)
		RegisterService(i)
		RegisterServer(i)

		return i
	}

	T.Run("store", func(t *testing.T) {
		t.Parallel()

		store, err := do.Invoke[identity.Store](build(t))
		test.Nil(t, store)
		test.ErrorIs(t, err, boom)
	})

	T.Run("service", func(t *testing.T) {
		t.Parallel()

		svc, err := do.Invoke[*identity.Service](build(t))
		test.Nil(t, svc)
		test.ErrorIs(t, err, boom)
	})

	T.Run("server", func(t *testing.T) {
		t.Parallel()

		srv, err := do.Invoke[*identitygrpc.Server](build(t))
		test.Nil(t, srv)
		test.ErrorIs(t, err, boom)
	})
}
