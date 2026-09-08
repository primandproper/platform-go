package notificationscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	databasemock "github.com/primandproper/primitives-go/database/mock"
	"github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newClient returns a database.Client that answers Dialect and nothing else,
// which is all NewStore reaches for.
func newClient(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return d }}
}

func TestConfig_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a renderable prefix", func(t *testing.T) {
		t.Parallel()

		// The empty prefix is the default rather than "unset": it renders
		// notifications_inbox and notifications_devices.
		must.NoError(t, (&Config{}).ValidateWithContext(t.Context()))
		must.NoError(t, (&Config{TablePrefix: "ddb"}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		// Vetted against the identifiers it actually produces, so a prefix that
		// is legal alone and yields an over-long index name fails at
		// construction rather than at the first migration.
		must.Error(t, (&Config{TablePrefix: "has space"}).ValidateWithContext(t.Context()))
		must.Error(t, (&Config{TablePrefix: "trailing_"}).ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Postgres))
		must.NoError(t, err)
		must.NotNil(t, store)

		// One value answering both seams, which is what lets RegisterStore
		// narrow it twice without building it twice.
		var _ notifications.Inbox = store
		var _ notifications.Registry = store
	})

	T.Run("carries the configured prefix into the store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "ddb"}, newClient(dialect.MySQL))
		must.NoError(t, err)
		test.EqOp(t, "ddb", store.TablePrefix())
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), nil, newClient(dialect.Postgres))
		must.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, nil)
		must.ErrorIs(t, err, notifications.ErrNilDatabaseClient)
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

		// A caller can override anything the config derived, the table prefix
		// included.
		store, err := NewStore(t.Context(), &Config{TablePrefix: "ddb"}, newClient(dialect.Postgres),
			WithStoreOptions(notifications.WithTablePrefix("override")))
		must.NoError(t, err)
		test.EqOp(t, "override", store.TablePrefix())
	})

	T.Run("takes no observability at all", func(t *testing.T) {
		t.Parallel()

		// Absent means noop: a caller wanting none of the three names none.
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
