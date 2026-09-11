package commentscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testTargets is the application declaration these tests build stores against.
var testTargets = comments.Targets{
	comments.TargetType("recipe"): {Description: "a recipe"},
}

// newClient returns a database.Client that answers Dialect and nothing else,
// which is all NewStore reaches for.
func newClient(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return d }}
}

func TestConfig_EnsureDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.EnsureDefaults()
	test.EqOp(t, comments.DefaultTablePrefix, cfg.TablePrefix)

	set := &Config{TablePrefix: "ddb"}
	set.EnsureDefaults()
	test.EqOp(t, "ddb", set.TablePrefix)
}

func TestConfig_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a renderable prefix", func(t *testing.T) {
		t.Parallel()

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

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Postgres), testTargets)
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("builds one with an empty catalog", func(t *testing.T) {
		t.Parallel()

		// Not refused here, because it is the leaf package's ruling: a store with
		// no targets accepts no writes, which fails on the first comment rather
		// than storing rows nothing will list.
		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Postgres), nil)
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), nil, newClient(dialect.Postgres), testTargets)
		must.ErrorIs(t, err, errors.ErrNilInputParameter)

		// The interface must be nil, not a non-nil interface holding a nil
		// pointer — a caller testing the result against nil would otherwise find
		// a store that panics on first use.
		test.Nil(t, store)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, nil, testTargets)
		must.ErrorIs(t, err, comments.ErrNilDatabaseClient)
		test.Nil(t, store)
	})

	T.Run("refuses an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Dialect("oracle")), testTargets)
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("refuses an invalid prefix", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "has space"},
			newClient(dialect.Postgres), testTargets)
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("applies explicit options after the config's", func(t *testing.T) {
		t.Parallel()

		// A caller can override anything the config derived, the table prefix
		// included.
		store, err := NewStore(t.Context(), &Config{TablePrefix: "ddb"},
			newClient(dialect.Postgres), testTargets,
			WithStoreOptions(comments.WithTablePrefix("override")))
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("takes no observability at all", func(t *testing.T) {
		t.Parallel()

		// Absent means noop: a caller wanting none of the three names none.
		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.SQLite), testTargets,
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

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.MySQL), testTargets,
			WithPillars(&observability.Pillars{}))
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}
