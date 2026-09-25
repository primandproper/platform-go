package grants

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestNewSQLStore covers what construction refuses, and what it settles for a
// caller who named nothing.
func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("a nil client is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil, newTestEncryptor(t))
		must.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("a nil encryptor is refused", func(t *testing.T) {
		t.Parallel()

		// There is no plaintext fallback. A store that stored tokens as they
		// arrived when nobody handed it a key is the table this package exists
		// to replace.
		_, err := NewSQLStore(newSQLiteEnv(t).client, nil)
		must.ErrorIs(t, err, ErrNilEncryptor)
	})

	T.Run("a dialect it has no statements for is refused", func(t *testing.T) {
		t.Parallel()

		client := &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return "oracle" }}

		_, err := NewSQLStore(client, newTestEncryptor(t))
		must.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("a prefix ending in the separator is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(newSQLiteEnv(t).client, newTestEncryptor(t), WithTablePrefix("ddb_"))
		must.Error(t, err)
	})

	T.Run("observability is optional", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(newSQLiteEnv(t).client, newTestEncryptor(t))
		must.NoError(t, err)
		test.NotNil(t, store.o11y)
		test.NotNil(t, store.instruments)
	})

	T.Run("pillars can be handed over and then overridden", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(newSQLiteEnv(t).client, newTestEncryptor(t),
			WithPillars(&observability.Pillars{}),
			WithMetricsProvider(nil),
			WithLogger(nil),
			WithTracerProvider(nil),
			nil,
		)
		must.NoError(t, err)
		test.NotNil(t, store)
	})
}
