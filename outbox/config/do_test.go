package outboxcfg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v15/outbox"

	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/errors"
	messagequeuecfg "github.com/primandproper/primitives-go/v2/messagequeue/config"

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

func TestRegisterWriter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterWriter(i)

		writer, err := do.Invoke[*outbox.Writer](i)
		must.NoError(t, err)
		test.NotNil(t, writer)
	})

	// A notify channel on SQLite is refused by the leaf package, which is what
	// proves the registered options reached the Writer: without the
	// passthrough, this would build fine.
	T.Run("applies registered writer options", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, WriterOptions{outbox.WithWriterNotifyChannel("outbox")})

		RegisterWriter(i)

		_, err := do.Invoke[*outbox.Writer](i)
		test.ErrorIs(t, err, outbox.ErrNotifyUnsupported)
	})

	T.Run("registered writer options run after the config-derived ones", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.Relay.NotifyChannel = "outbox"

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, cfg)
		do.ProvideValue(i, WriterOptions{outbox.WithWriterNotifyChannel("")})

		RegisterWriter(i)

		writer, err := do.Invoke[*outbox.Writer](i)
		must.NoError(t, err)
		test.NotNil(t, writer)
	})

	T.Run("writer options that fail to build are an error, not an absence", func(t *testing.T) {
		t.Parallel()

		errBuild := errors.New("building writer options")

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.Provide(i, func(do.Injector) (WriterOptions, error) { return nil, errBuild })

		RegisterWriter(i)

		_, err := do.Invoke[*outbox.Writer](i)
		test.ErrorIs(t, err, errBuild)
	})
}

func TestRegisterRelay(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{
			Queue: messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop},
		})

		RegisterRelay(i)

		relay, err := do.Invoke[*outbox.Relay](i)
		must.NoError(t, err)
		test.NotNil(t, relay)
	})
}
