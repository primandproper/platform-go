package oauth2clientscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	oauth2clientsmock "github.com/primandproper/platform-go/v14/authentication/oauth2clients/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newClient returns a database.Client that answers Dialect and nothing else,
// which is all NewStore reaches for.
func newClient(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return d }}
}

func TestConfig_EnsureDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.EnsureDefaults()
	test.EqOp(t, oauth2clients.DefaultTablePrefix, cfg.TablePrefix)

	set := &Config{TablePrefix: "ddb"}
	set.EnsureDefaults()
	test.EqOp(t, "ddb", set.TablePrefix)
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a renderable prefix", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, (&Config{}).ValidateWithContext(t.Context()))
		must.NoError(t, (&Config{TablePrefix: "ddb"}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		must.Error(t, (&Config{TablePrefix: "has space"}).ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store carrying the configured prefix", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "ddb"}, newClient(dialect.Postgres))
		must.NoError(t, err)

		sqlStore, ok := store.(*oauth2clients.SQLStore)
		must.True(t, ok)
		test.EqOp(t, "ddb", sqlStore.TablePrefix())
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), nil, newClient(dialect.Postgres))
		must.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		// The interface must be nil, not a non-nil interface holding a nil
		// pointer — a caller testing the result against nil would otherwise find
		// a store that panics on first use.
		store, err := NewStore(t.Context(), &Config{}, nil)
		must.ErrorIs(t, err, oauth2clients.ErrNilDatabaseClient)
		test.Nil(t, store)
	})

	T.Run("refuses an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Dialect("oracle")))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("refuses an invalid prefix", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "has space"}, newClient(dialect.Postgres))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("applies explicit options after the config's", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "ddb"}, newClient(dialect.Postgres),
			WithStoreOptions(oauth2clients.WithTablePrefix("override")))
		must.NoError(t, err)

		sqlStore, ok := store.(*oauth2clients.SQLStore)
		must.True(t, ok)
		test.EqOp(t, "override", sqlStore.TablePrefix())
	})

	T.Run("takes no observability at all", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.SQLite),
			WithPillars(nil),
			WithLogger(nil),
			WithTracerProvider(nil),
			WithMetricsProvider(nil),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("takes pillars", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.MySQL),
			WithPillars(&observability.Pillars{}))
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}

func TestNewService(T *testing.T) {
	T.Parallel()

	T.Run("builds a service", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), &Config{}, newClient(dialect.Postgres), &oauth2clientsmock.StoreMock{},
			WithPillars(&observability.Pillars{}),
			WithHooks(oauth2clients.NoopHooks{}),
			WithServiceOptions(oauth2clients.WithCredentialGenerator(func() (string, string, error) {
				return "id", "secret", nil
			})),
		)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("ignores a nil hooks", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), &Config{}, newClient(dialect.Postgres), &oauth2clientsmock.StoreMock{},
			WithHooks(nil))
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), nil, newClient(dialect.Postgres), &oauth2clientsmock.StoreMock{})
		must.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, svc)
	})

	T.Run("refuses an invalid prefix", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), &Config{TablePrefix: "has space"}, newClient(dialect.Postgres),
			&oauth2clientsmock.StoreMock{})
		must.Error(t, err)
		test.Nil(t, svc)
	})

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), &Config{}, newClient(dialect.Postgres), nil)
		must.ErrorIs(t, err, oauth2clients.ErrNilStore)
		test.Nil(t, svc)
	})
}
