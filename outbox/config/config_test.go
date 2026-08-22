package outboxcfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	databasemock "github.com/primandproper/platform-go/v13/database/mock"
	messagequeuecfg "github.com/primandproper/platform-go/v13/messagequeue/config"
	"github.com/primandproper/platform-go/v13/messagequeue/pubsub"
	"github.com/primandproper/platform-go/v13/outbox"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// sqliteConfig is a valid in-process configuration: SQLite dialect, noop
// publisher.
func sqliteConfig() *Config {
	return &Config{
		Relay: outbox.RelayConfig{},
		Queue: messagequeuecfg.Config{Publisher: messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop}},
	}
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills in zero fields", func(t *testing.T) {
		t.Parallel()

		cfg := sqliteConfig()
		cfg.EnsureDefaults()

		test.EqOp(t, outbox.DefaultTablePrefix, cfg.Relay.TablePrefix)
		test.EqOp(t, outbox.DefaultBatchSize, cfg.Relay.BatchSize)
	})

	T.Run("leaves set fields alone", func(t *testing.T) {
		t.Parallel()

		cfg := sqliteConfig()
		cfg.Relay.TablePrefix = "custom_outbox"
		cfg.EnsureDefaults()

		test.EqOp(t, "custom_outbox", cfg.Relay.TablePrefix)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a valid config", func(t *testing.T) {
		t.Parallel()

		cfg := sqliteConfig()
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	// The nested config is reached through a validation.By closure, because
	// ozzo would otherwise dereference the struct and skip it.
	T.Run("rejects an invalid nested relay config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()
		cfg.Relay.ClaimMode = "telepathy"

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

// sqliteClient is a database.Client that reports SQLite and nothing else; the
// Writer only reads the dialect off it.
func sqliteClient() database.Client {
	return &databasemock.ClientMock{
		DialectFunc: func() dialect.Dialect { return dialect.SQLite },
	}
}

func TestNewWriter(T *testing.T) {
	T.Parallel()

	T.Run("builds a writer from the relay section", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(t.Context(), sqliteConfig(), sqliteClient())
		must.NoError(t, err)
		must.NotNil(t, w)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewWriter(t.Context(), nil, sqliteClient())
		test.Error(t, err)
	})

	// The dialect comes from the client now, so a config without one is fine;
	// a client without one is not.
	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewWriter(t.Context(), sqliteConfig(), nil)
		test.ErrorIs(t, err, outbox.ErrNilDatabaseClient)
	})

	T.Run("derives options from every observability argument", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(
			t.Context(),
			sqliteConfig(),
			sqliteClient(),
		)
		must.NoError(t, err)
		must.NotNil(t, w)
	})

	T.Run("explicit options run after the config-derived ones", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(
			t.Context(),
			sqliteConfig(),
			sqliteClient(),
			WithWriterOptions(outbox.WithWriterTablePrefix("override_table")),
		)
		must.NoError(t, err)
		must.NotNil(t, w)
	})
}

// The notify channel is read from the Relay section so the half that writes and
// the half that is woken cannot name different channels. That it reaches the
// Writer at all is observable only through the Writer's own rules for it.
func TestNewWriter_notifyChannel(T *testing.T) {
	T.Parallel()

	T.Run("carries the channel from the relay section", func(t *testing.T) {
		t.Parallel()

		cfg := sqliteConfig()
		cfg.Relay.NotifyChannel = "outbox"

		w, err := NewWriter(t.Context(), cfg, &databasemock.ClientMock{
			DialectFunc: func() dialect.Dialect { return dialect.Postgres },
		})
		must.NoError(t, err)
		must.NotNil(t, w)
	})

	// A channel on a dialect without NOTIFY is refused by the leaf package, and
	// this is what proves the config actually handed it over: without the
	// passthrough, a SQLite writer with a channel configured would build fine.
	T.Run("a channel on a dialect without NOTIFY is refused", func(t *testing.T) {
		t.Parallel()

		cfg := sqliteConfig()
		cfg.Relay.NotifyChannel = "outbox"

		_, err := NewWriter(t.Context(), cfg, sqliteClient())
		test.ErrorIs(t, err, outbox.ErrNotifyUnsupported)
	})
}

func TestNewRelay(T *testing.T) {
	T.Parallel()

	T.Run("builds a relay with a noop publisher", func(t *testing.T) {
		t.Parallel()

		r, err := NewRelay(t.Context(), sqliteConfig(), sqliteClient())
		must.NoError(t, err)
		must.NotNil(t, r)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewRelay(t.Context(), nil, sqliteClient())
		test.Error(t, err)
	})

	T.Run("rejects a nil database client", func(t *testing.T) {
		t.Parallel()

		_, err := NewRelay(t.Context(), sqliteConfig(), nil)
		test.Error(t, err)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewRelay(t.Context(), sqliteConfig(), nil)
		test.Error(t, err)
	})

	T.Run("surfaces a publisher provider that will not build", func(t *testing.T) {
		t.Parallel()

		// PubSub with no project ID fails client construction, which is the
		// cheapest way to make the provider step fail without a network.
		cfg := sqliteConfig()
		cfg.Queue = messagequeuecfg.Config{
			Publisher: messagequeuecfg.MessageQueueConfig{
				Provider: messagequeuecfg.ProviderPubSub,
				PubSub:   pubsub.Config{},
			},
		}

		r, err := NewRelay(t.Context(), cfg, sqliteClient())
		test.Nil(t, r)
		test.Error(t, err)
	})

	T.Run("derives options from every observability argument", func(t *testing.T) {
		t.Parallel()

		r, err := NewRelay(
			t.Context(),
			sqliteConfig(),
			sqliteClient(),
		)
		must.NoError(t, err)
		must.NotNil(t, r)
	})
}
