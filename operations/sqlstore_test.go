package operations

import (
	"testing"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	databasemock "github.com/primandproper/primitives-go/database/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// storeClient answers Dialect and nothing else, which is all construction
// reaches for.
func storeClient(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return d }}
}

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("builds against Postgres", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(storeClient(dialect.Postgres))
		must.NoError(t, err)
		must.NotNil(t, store)

		// The querier is built from the prefix once it is settled, so a store
		// that reached this line has one.
		must.NotNil(t, store.q)
		test.EqOp(t, DefaultTablePrefix, store.tablePrefix)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(nil)
		must.ErrorIs(t, err, ErrNilDatabaseClient)
		test.Nil(t, store)
	})

	T.Run("refuses a dialect it cannot emit", func(t *testing.T) {
		t.Parallel()

		// The dialect comes off the client, so the two cannot disagree — and
		// this package speaks one of them.
		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite, dialect.Dialect("oracle")} {
			store, err := NewSQLStore(storeClient(d))
			must.Error(t, err, must.Sprintf("%s", d))
			test.Nil(t, store, test.Sprintf("%s", d))
		}
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(storeClient(dialect.Postgres), WithStoreTablePrefix("has space"))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("refuses a notify channel that cannot be listened on", func(t *testing.T) {
		t.Parallel()

		// The channel is bound as text here but has to render into a LISTEN,
		// which takes no parameters — so it is vetted at this end.
		store, err := NewSQLStore(storeClient(dialect.Postgres), WithStoreNotifyChannel("ops; drop"))
		must.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
		test.Nil(t, store)
	})

	T.Run("takes no observability at all", func(t *testing.T) {
		t.Parallel()

		// Absent means noop, so an unconfigured store logs nowhere and traces
		// nowhere rather than requiring three explicit noops — and a nil option
		// in the slice is skipped rather than dereferenced.
		var none SQLStoreOption

		store, err := NewSQLStore(storeClient(dialect.Postgres),
			WithStoreLogger(nil),
			WithStoreTracerProvider(nil),
			WithStoreMetricsProvider(nil),
			none,
		)
		must.NoError(t, err)
		must.NotNil(t, store)
		must.NotNil(t, store.o11y)
	})

	T.Run("every store option is the one type", func(t *testing.T) {
		t.Parallel()

		// Every sibling SQL store in the module spells this SQLStoreOption,
		// and the variadic they are collected into is what makes the name a
		// consumer's problem rather than this file's.
		opts := []SQLStoreOption{
			WithStoreLogger(nil),
			WithStoreTracerProvider(nil),
			WithStoreMetricsProvider(nil),
			WithStoreTablePrefix(DefaultTablePrefix),
			WithStoreNotifyChannel("operations_changed"),
		}

		store, err := NewSQLStore(storeClient(dialect.Postgres), opts...)
		must.NoError(t, err)
		must.NotNil(t, store)
		test.EqOp(t, "operations_changed", store.notifyChannel)
	})
}
