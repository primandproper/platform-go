package dataprivacycfg

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/operations"

	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/uploads"
	uploadsnoop "github.com/primandproper/primitives-go/v2/uploads/noop"

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
			func(context.Context, tenancy.Scope, dataprivacy.Subject) (json.RawMessage, error) {
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
			func(context.Context, tenancy.Scope, dataprivacy.Subject) (json.RawMessage, error) {
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

	T.Run("a registered encryptor is the whole of what a container says", func(t *testing.T) {
		t.Parallel()

		i := fulfillerInjector(t, testConfig())

		encryptorDecryptor, err := newTestEncryptorDecryptor([]byte("0123456789abcdef0123456789abcdef"))
		must.NoError(t, err)
		do.ProvideValue(i, encryptorDecryptor)

		RegisterStore(i)
		RegisterFulfiller(i)

		// Registering the encryptor is sufficient, and there is no second
		// thing to set. The container holds the only statement of whether this
		// deployment encrypts, so a wiring that is correct cannot also be
		// refused for failing to repeat itself.
		fulfiller, err := do.Invoke[*dataprivacy.Fulfiller](i)
		must.NoError(t, err)
		test.NotNil(t, fulfiller)
	})

	T.Run("the same registration reaches the Service that reads the artifacts back", func(t *testing.T) {
		t.Parallel()

		i := fulfillerInjector(t, testConfig())

		encryptorDecryptor, err := newTestEncryptorDecryptor([]byte("0123456789abcdef0123456789abcdef"))
		must.NoError(t, err)
		do.ProvideValue(i, encryptorDecryptor)
		do.ProvideValue[operations.Service](i, stubOperations())

		RegisterStore(i)
		RegisterFulfiller(i)
		RegisterService(i)

		// Both providers resolve the same two optional registrations, so the
		// codecs the Fulfiller writes an artifact with are the ones the Service
		// reads it back with — which is what EnsurePackaging used to be for.
		svc, err := do.Invoke[dataprivacy.Service](i)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})
}

// fulfillerInjector registers everything RegisterFulfiller needs but the store
// and the fulfiller themselves.
func fulfillerInjector(t *testing.T, cfg *Config) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue[database.Client](i, testDBClient(t))
	do.ProvideValue(i, cfg)

	domains := dataprivacy.NewRegistry()
	must.NoError(t, domains.RegisterCollector("example", dataprivacy.CollectorFunc(
		func(context.Context, tenancy.Scope, dataprivacy.Subject) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	)))
	do.ProvideValue(i, domains)
	do.ProvideValue[uploads.UploadManager](i, uploadsnoop.NewUploadManager())
	do.ProvideValue(i, operations.NewRegistry())

	return i
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
