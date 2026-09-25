package grantscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/grants"

	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	"github.com/primandproper/primitives-go/v2/cryptography/encryption/aes"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newClient returns a database.Client that answers Dialect and nothing else,
// which is all NewStore reaches for.
func newClient(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return d }}
}

// newEncryptor is a one-key keyring.
func newEncryptor(t *testing.T) encryption.EncryptorDecryptor {
	t.Helper()

	cipher, err := aes.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	must.NoError(t, err)

	keyring, err := encryption.NewKeyring("k1", []encryption.RingKey{{ID: "k1", Cipher: cipher}})
	must.NoError(t, err)

	return keyring
}

func TestConfig_EnsureDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.EnsureDefaults()
	test.EqOp(t, grants.DefaultTablePrefix, cfg.TablePrefix)

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

		must.Error(t, (&Config{TablePrefix: "has space"}).ValidateWithContext(t.Context()))
		must.Error(t, (&Config{TablePrefix: "trailing_"}).ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Postgres), newEncryptor(t))
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), nil, newClient(dialect.Postgres), newEncryptor(t))
		must.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, nil, newEncryptor(t))
		must.ErrorIs(t, err, grants.ErrNilDatabaseClient)

		// The interface must be nil, not a non-nil interface holding a nil
		// pointer.
		test.Nil(t, store)
	})

	T.Run("refuses a nil encryptor", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Postgres), nil)
		must.ErrorIs(t, err, grants.ErrNilEncryptor)
		test.Nil(t, store)
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "has space"}, newClient(dialect.Postgres), newEncryptor(t))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("applies store options after the ones it derives", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "ddb"}, newClient(dialect.Postgres), newEncryptor(t),
			WithStoreOptions(grants.WithTablePrefix("elsewhere")))
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}
