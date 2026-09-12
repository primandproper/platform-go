package notificationscfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// NewStore's declared return type is notifications.Store rather than the
// implementation it builds. A function value takes the signature exactly, so
// this stops compiling the moment the return narrows back to
// *notifications.SQLStore — which is what a caller would then be entitled to
// depend on, and what could not be taken back inside a major version.
var _ func(context.Context, *Config, database.Client, ...Option) (notifications.Store, error) = NewStore

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
		// narrow it three times without building it three times.
		var _ notifications.Inbox = store
		var _ notifications.Registry = store
	})

	T.Run("carries the configured prefix into the store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "ddb"}, newClient(dialect.MySQL))
		must.NoError(t, err)

		// The prefix is not on either seam, so reading it back means naming the
		// implementation this config builds — which a test in this package may
		// do and a caller holding the interface may not.
		sqlStore, ok := store.(*notifications.SQLStore)
		must.True(t, ok)
		test.EqOp(t, "ddb", sqlStore.TablePrefix())
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), nil, newClient(dialect.Postgres))
		must.ErrorIs(t, err, errors.ErrNilInputParameter)

		// The interface must be nil, not a non-nil interface holding a nil
		// pointer — a caller testing the result against nil would otherwise find
		// a store that panics on first use.
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

		sqlStore, ok := store.(*notifications.SQLStore)
		must.True(t, ok)
		test.EqOp(t, "override", sqlStore.TablePrefix())
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
