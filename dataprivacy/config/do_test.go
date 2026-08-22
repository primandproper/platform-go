package dataprivacycfg

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v13/database"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/dataprivacy"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/uploads"
	uploadsnoop "github.com/primandproper/platform-go/v13/uploads/noop"

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

func testConfig() *Config {
	return &Config{Dialect: dialect.SQLite}
}

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, testConfig())

		RegisterStore(i)

		store, err := do.Invoke[dataprivacy.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})
}

func TestRegisterService(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, testConfig())
		do.ProvideValue[operations.Service](i, stubOperations())
		do.ProvideValue(i, operations.NewRegistry())

		domains := dataprivacy.NewRegistry()
		must.NoError(t, domains.RegisterCollector("example", dataprivacy.CollectorFunc(
			func(context.Context, dataprivacy.Subject) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		)))
		do.ProvideValue(i, domains)
		do.ProvideValue[uploads.UploadManager](i, uploadsnoop.NewUploadManager())

		RegisterStore(i)
		RegisterFulfiller(i)
		RegisterService(i)

		service, err := do.Invoke[dataprivacy.Service](i)
		must.NoError(t, err)
		test.NotNil(t, service)
	})

	// The Service depends on the Fulfiller to be ordered rather than used: the
	// Fulfiller registers the kinds, and starting an operation resolves its kind
	// at submission.
	T.Run("without a fulfiller", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, testConfig())
		do.ProvideValue[operations.Service](i, stubOperations())

		RegisterStore(i)
		RegisterService(i)

		_, err := do.Invoke[dataprivacy.Service](i)
		test.Error(t, err)
	})
}

func TestRegisterFulfiller(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, testConfig())

		domains := dataprivacy.NewRegistry()
		must.NoError(t, domains.RegisterCollector("example", dataprivacy.CollectorFunc(
			func(context.Context, dataprivacy.Subject) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		)))
		do.ProvideValue(i, domains)
		do.ProvideValue[uploads.UploadManager](i, uploadsnoop.NewUploadManager())

		kinds := operations.NewRegistry()
		do.ProvideValue(i, kinds)

		RegisterStore(i)
		RegisterFulfiller(i)

		fulfiller, err := do.Invoke[*dataprivacy.Fulfiller](i)
		must.NoError(t, err)
		test.NotNil(t, fulfiller)

		// Registered as it was built, so an operations.Worker over the same
		// registry can run it.
		test.Eq(t, []string{dataprivacy.KindExport}, kinds.Kinds())
	})
}

func TestRegisterSweeper(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, testConfig())
		do.ProvideValue[uploads.UploadManager](i, uploadsnoop.NewUploadManager())

		RegisterStore(i)
		RegisterSweeper(i)

		sweeper, err := do.Invoke[*dataprivacy.Sweeper](i)
		must.NoError(t, err)
		test.NotNil(t, sweeper)
	})
}
