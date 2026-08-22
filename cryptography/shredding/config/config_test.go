package shreddingcfg

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/aes"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/kms/local"
	"github.com/primandproper/platform-go/v13/cryptography/shredding"
	"github.com/primandproper/platform-go/v13/cryptography/shredding/migrations"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var testSubject = shredding.Subject{Type: "user", ID: "user-1"}

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

func newMigratedClient(t *testing.T) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "shredding.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, shredding.DefaultTablePrefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return client
}

func newTestWrapper(t *testing.T) encryption.KeyWrapper {
	t.Helper()

	cipher, err := aes.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	must.NoError(t, err)

	wrapper, err := local.NewKeyWrapper(cipher)
	must.NoError(t, err)

	return wrapper
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills in what was left unset", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, shredding.DefaultTablePrefix, cfg.TablePrefix)
		test.EqOp(t, shredding.DefaultInvalidationTopic, cfg.InvalidationTopic)
		test.EqOp(t, shredding.DefaultKeyTTL, cfg.KeyTTL)
		test.EqOp(t, shredding.DefaultMaxCachedKeys, cfg.MaxCachedKeys)
	})

	T.Run("leaves what was set alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{TablePrefix: "ddb", InvalidationTopic: "shreds", KeyTTL: time.Minute, MaxCachedKeys: 8}
		cfg.EnsureDefaults()

		test.EqOp(t, "ddb", cfg.TablePrefix)
		test.EqOp(t, "shreds", cfg.InvalidationTopic)
		test.EqOp(t, time.Minute, cfg.KeyTTL)
		test.EqOp(t, 8, cfg.MaxCachedKeys)
	})

	T.Run("survives a nil receiver", func(t *testing.T) {
		t.Parallel()

		var cfg *Config
		cfg.EnsureDefaults()
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a defaulted config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a negative TTL", func(t *testing.T) {
		t.Parallel()

		// Zero is a legitimate setting that turns caching off. Negative would
		// do the same while reading like it configured something.
		cfg := &Config{InvalidationTopic: "shreds", KeyTTL: -time.Minute}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an empty invalidation topic", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a working store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newMigratedClient(t))
		must.NoError(t, err)
		must.NotNil(t, store)

		_, err = store.Load(t.Context(), testSubject)
		test.ErrorIs(t, err, shredding.ErrNoKey)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), nil, newMigratedClient(t))
		test.Nil(t, store)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, nil)
		test.Nil(t, store)
		test.ErrorIs(t, err, shredding.ErrNilDatabaseClient)
	})
}

func TestNewKeys(T *testing.T) {
	T.Parallel()

	T.Run("builds a working Keys", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newMigratedClient(t))
		must.NoError(t, err)

		keys, err := NewKeys(t.Context(), &Config{}, store, newTestWrapper(t))
		must.NoError(t, err)

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		opened, err := keys.Decrypt(t.Context(), testSubject, sealed, nil)
		must.NoError(t, err)
		test.Eq(t, []byte("home address"), opened)

		_, err = keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)

		_, err = keys.Decrypt(t.Context(), testSubject, sealed, nil)
		test.ErrorIs(t, err, shredding.ErrSubjectShredded)
	})

	T.Run("refuses a nil wrapper", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newMigratedClient(t))
		must.NoError(t, err)

		keys, err := NewKeys(t.Context(), &Config{}, store, nil)
		test.Nil(t, keys)
		test.ErrorIs(t, err, shredding.ErrNilKeyWrapper)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		keys, err := NewKeys(t.Context(), nil, nil, newTestWrapper(t))
		test.Nil(t, keys)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})
}

func TestNewBroadcaster(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil publisher provider", func(t *testing.T) {
		t.Parallel()

		broadcaster, err := NewBroadcaster(t.Context(), &Config{}, nil)
		test.Nil(t, broadcaster)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})
}

func TestNewInvalidationConsumer(T *testing.T) {
	T.Parallel()

	T.Run("subscribes to the configured topic", func(t *testing.T) {
		t.Parallel()

		var subscribed string

		provider := &messagequeuemock.ConsumerProviderMock{
			NewConsumerFunc: func(_ context.Context, topic string, _ messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
				subscribed = topic

				return &messagequeuemock.ConsumerMock{}, nil
			},
		}

		consumer, err := NewInvalidationConsumer(t.Context(),
			&Config{InvalidationTopic: "shreds"}, provider, &noopInvalidator{})
		must.NoError(t, err)
		test.NotNil(t, consumer)

		// The two halves are a topic name agreeing with itself. A subscriber on
		// the wrong one is a fleet that never hears a shred and reports nothing
		// wrong.
		test.EqOp(t, "shreds", subscribed)
	})

	T.Run("defaults the topic", func(t *testing.T) {
		t.Parallel()

		var subscribed string

		provider := &messagequeuemock.ConsumerProviderMock{
			NewConsumerFunc: func(_ context.Context, topic string, _ messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
				subscribed = topic

				return &messagequeuemock.ConsumerMock{}, nil
			},
		}

		_, err := NewInvalidationConsumer(t.Context(), &Config{}, provider, &noopInvalidator{})
		must.NoError(t, err)
		test.EqOp(t, shredding.DefaultInvalidationTopic, subscribed)
	})

	T.Run("refuses a nil consumer provider", func(t *testing.T) {
		t.Parallel()

		consumer, err := NewInvalidationConsumer(t.Context(), &Config{}, nil, &noopInvalidator{})
		test.Nil(t, consumer)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("refuses a nil invalidator", func(t *testing.T) {
		t.Parallel()

		provider := &messagequeuemock.ConsumerProviderMock{}

		consumer, err := NewInvalidationConsumer(t.Context(), &Config{}, provider, nil)
		test.Nil(t, consumer)
		test.ErrorIs(t, err, shredding.ErrNilInvalidator)
	})

	T.Run("reports a subscription failure", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("bus is down")
		provider := &messagequeuemock.ConsumerProviderMock{
			NewConsumerFunc: func(context.Context, string, messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
				return nil, sentinel
			},
		}

		consumer, err := NewInvalidationConsumer(t.Context(), &Config{}, provider, &noopInvalidator{})
		test.Nil(t, consumer)
		test.ErrorIs(t, err, sentinel)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		consumer, err := NewInvalidationConsumer(t.Context(), nil, &messagequeuemock.ConsumerProviderMock{}, &noopInvalidator{})
		test.Nil(t, consumer)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})
}

// noopInvalidator is something non-nil for the constructor to hold.
type noopInvalidator struct{}

var _ shredding.Invalidator = (*noopInvalidator)(nil)

func (*noopInvalidator) Invalidate(context.Context, shredding.Subject) {}
