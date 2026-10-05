package dataprivacycfg

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v15/audit"
	auditmock "github.com/primandproper/platform-go/v15/audit/mock"
	"github.com/primandproper/platform-go/v15/dataprivacy"
	dataprivacymock "github.com/primandproper/platform-go/v15/dataprivacy/mock"
	"github.com/primandproper/platform-go/v15/operations"

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

// signingUploads is an upload manager that signs, so Service.Download has a
// link to mint through whichever manager the container handed it.
type signingUploads struct {
	uploads.UploadManager
}

func (signingUploads) SignedURL(_ context.Context, path string, _ *uploads.SignedURLOptions) (string, error) {
	return "https://storage.example/" + path, nil
}

func TestRegisterService_ReadsWhatTheFulfillerWrote(T *testing.T) {
	T.Parallel()

	// container is fulfillerInjector over a store holding one completed export
	// and an upload manager that signs, with the Service registered.
	container := func(t *testing.T) do.Injector {
		t.Helper()

		i := fulfillerInjector(t, testConfig())
		do.OverrideValue[uploads.UploadManager](i, signingUploads{UploadManager: uploadsnoop.NewUploadManager()})
		do.ProvideValue[operations.Service](i, stubOperations())
		do.ProvideValue[dataprivacy.Store](i, &dataprivacymock.StoreMock{
			GetFunc: func(_ context.Context, _ database.SQLQueryExecutor, _ *tenancy.Scope, id string) (*dataprivacy.Request, error) {
				return &dataprivacy.Request{
					ID:          id,
					Type:        dataprivacy.RequestExport,
					Status:      dataprivacy.StatusCompleted,
					ArtifactRef: "exports/" + id,
				}, nil
			},
		})

		RegisterFulfiller(i)
		RegisterService(i)

		return i
	}

	T.Run("downloads through the upload manager the Fulfiller writes to", func(t *testing.T) {
		t.Parallel()

		svc, err := do.Invoke[dataprivacy.Service](container(t))
		must.NoError(t, err)

		// Without it the Service answers ErrArtifactUnavailable for an
		// artifact that is sitting in the bucket.
		url, err := svc.Download(t.Context(), nil, "req")
		must.NoError(t, err)
		test.EqOp(t, "https://storage.example/exports/req", url)
	})

	T.Run("records to the registered audit log, naming the registered actor", func(t *testing.T) {
		t.Parallel()

		var recorded []*audit.Entry

		i := container(t)
		do.ProvideValue[audit.Recorder](i, &auditmock.RecorderMock{
			RecordFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, entries ...*audit.Entry) error {
				recorded = append(recorded, entries...)

				return nil
			},
		})
		do.ProvideValue(i, dataprivacy.ActorResolver(func(context.Context) audit.Actor {
			return audit.Actor{ID: "support-agent", Type: audit.ActorUser}
		}))

		svc, err := do.Invoke[dataprivacy.Service](i)
		must.NoError(t, err)

		_, err = svc.Download(t.Context(), nil, "req")
		must.NoError(t, err)

		must.SliceLen(t, 1, recorded)
		test.EqOp(t, "req", recorded[0].ResourceID)
		test.EqOp(t, "support-agent", recorded[0].Actor.ID)
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

func TestRegisterFulfiller_OptionalRegistrations(T *testing.T) {
	T.Parallel()

	// Only absence is absorbed. A registration the application meant to have
	// and could not build fails the Fulfiller rather than leaving it running
	// without — an export nobody is told about, or one with no record of who
	// asked for it.
	for name, register := range map[string]func(do.Injector, error){
		"notifier": func(i do.Injector, err error) {
			do.Provide(i, func(do.Injector) (dataprivacy.Notifier, error) { return nil, err })
		},
		"audit recorder": func(i do.Injector, err error) {
			do.Provide(i, func(do.Injector) (audit.Recorder, error) { return nil, err })
		},
		"actor resolver": func(i do.Injector, err error) {
			do.Provide(i, func(do.Injector) (dataprivacy.ActorResolver, error) { return nil, err })
		},
	} {
		T.Run("a registered "+name+" that fails to build fails it", func(t *testing.T) {
			t.Parallel()

			boom := errors.New("could not build the " + name)

			i := fulfillerInjector(t, testConfig())
			register(i, boom)
			RegisterStore(i)
			RegisterFulfiller(i)

			fulfiller, err := do.Invoke[*dataprivacy.Fulfiller](i)
			test.Nil(t, fulfiller)
			test.ErrorIs(t, err, boom)
		})
	}

	T.Run("each one registered is accepted", func(t *testing.T) {
		t.Parallel()

		i := fulfillerInjector(t, testConfig())
		do.ProvideValue[dataprivacy.Notifier](i, dataprivacy.NotifierFunc(func(context.Context, *dataprivacy.Notification) error { return nil }))
		do.ProvideValue[audit.Recorder](i, &auditmock.RecorderMock{})
		do.ProvideValue(i, dataprivacy.ActorResolver(func(context.Context) audit.Actor { return audit.Actor{ID: "system"} }))
		RegisterStore(i)
		RegisterFulfiller(i)

		fulfiller, err := do.Invoke[*dataprivacy.Fulfiller](i)
		must.NoError(t, err)
		test.NotNil(t, fulfiller)
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
