package dataprivacycfg

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	encryptioncfg "github.com/primandproper/primitives-go/v2/cryptography/encryption/config"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/uploads"
	uploadsnoop "github.com/primandproper/primitives-go/v2/uploads/noop"
	"github.com/primandproper/primitives-go/v2/uploads/objectstorage"

	"github.com/caarlos0/env/v11"
	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testArtifactKey is the one key the artifact keyrings below are built over.
var testArtifactKey = encryption.MasterKey("0123456789abcdef0123456789abcdef")

// artifactsConfig is a Config whose Artifacts block configures both halves.
func artifactsConfig() *Config {
	cfg := testConfig()
	cfg.Artifacts = &ArtifactsConfig{
		Storage: &objectstorage.Config{
			Provider:   objectstorage.MemoryProvider,
			BucketName: "privacy-exports",
		},
		Encryption: &encryptioncfg.Config{
			Provider:     encryptioncfg.ProviderAES,
			CurrentKeyID: "artifacts",
		},
	}

	return cfg
}

// closeCounter is an upload manager that counts its Close calls, so a test can
// say whose manager was released.
type closeCounter struct {
	uploads.UploadManager

	closed int
}

func (c *closeCounter) Close() error {
	c.closed++

	return nil
}

func TestConfig_artifacts(T *testing.T) {
	T.Parallel()

	T.Run("an Artifacts block env parsing allocated and nobody filled in is released", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{Environment: map[string]string{"DIALECT": "sqlite"}}))
		must.NotNil(t, cfg.Artifacts, must.Sprint("env parsing did not allocate the block; the release below proves nothing"))

		cfg.EnsureDefaults()
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		test.Nil(t, cfg.Artifacts)
	})

	T.Run("a half filled in from the environment is kept, and the other released", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{Environment: map[string]string{
			"DIALECT":                             "sqlite",
			"ARTIFACTS_ENCRYPTION_PROVIDER":       "aes",
			"ARTIFACTS_ENCRYPTION_CURRENT_KEY_ID": "artifacts",
		}}))

		cfg.EnsureDefaults()
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		must.NotNil(t, cfg.Artifacts)
		must.NotNil(t, cfg.Artifacts.Encryption)
		test.EqOp(t, "artifacts", cfg.Artifacts.Encryption.CurrentKeyID)
		test.Nil(t, cfg.Artifacts.Storage)
	})

	T.Run("a half that is configured is validated", func(t *testing.T) {
		t.Parallel()

		cfg := artifactsConfig()
		cfg.Artifacts.Encryption.CurrentKeyID = ""

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewArtifactStorage(T *testing.T) {
	T.Parallel()

	T.Run("a Config with no block builds nothing", func(t *testing.T) {
		t.Parallel()

		storage, err := NewArtifactStorage(t.Context(), testConfig(), nil)
		must.NoError(t, err)

		test.Nil(t, storage.Manager)
		test.Nil(t, storage.Encryptor)
		test.NoError(t, storage.Close())
	})

	T.Run("builds the bucket and the keyring the block names", func(t *testing.T) {
		t.Parallel()

		storage, err := NewArtifactStorage(t.Context(), artifactsConfig(), encryption.Keyset{"artifacts": testArtifactKey})
		must.NoError(t, err)

		must.NotNil(t, storage.Manager)
		must.NotNil(t, storage.Encryptor)

		sealed, err := storage.Encryptor.Encrypt(t.Context(), []byte("everything about somebody"), nil)
		must.NoError(t, err)
		test.NotEq(t, []byte("everything about somebody"), sealed)

		test.NoError(t, storage.Close())
	})

	T.Run("a keyring with no keys is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewArtifactStorage(t.Context(), artifactsConfig(), nil)
		test.Error(t, err)
	})

	T.Run("a nil Config is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewArtifactStorage(t.Context(), nil, nil)
		test.Error(t, err)
	})
}

func TestRegisterArtifactStorage(T *testing.T) {
	T.Parallel()

	injector := func(t *testing.T, cfg *Config) do.Injector {
		t.Helper()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, cfg)

		return i
	}

	T.Run("with no block it is the container's own manager and encryptor", func(t *testing.T) {
		t.Parallel()

		i := injector(t, testConfig())

		shared := &closeCounter{UploadManager: uploadsnoop.NewUploadManager()}
		do.ProvideValue[uploads.UploadManager](i, shared)

		encryptor, err := newTestEncryptorDecryptor([]byte(testArtifactKey))
		must.NoError(t, err)
		do.ProvideValue(i, encryptor)

		RegisterArtifactStorage(i)

		storage, err := do.Invoke[*ArtifactStorage](i)
		must.NoError(t, err)

		test.EqOp[uploads.UploadManager](t, shared, storage.Manager)
		test.EqOp(t, encryptor, storage.Encryptor)

		// The container's manager is the container's to close.
		test.NoError(t, storage.Close())
		test.EqOp(t, 0, shared.closed)
	})

	T.Run("a configured block is built rather than taken from the container", func(t *testing.T) {
		t.Parallel()

		i := injector(t, artifactsConfig())

		shared := &closeCounter{UploadManager: uploadsnoop.NewUploadManager()}
		do.ProvideValue[uploads.UploadManager](i, shared)
		do.ProvideValue(i, encryption.Keyset{"artifacts": testArtifactKey})

		RegisterArtifactStorage(i)

		storage, err := do.Invoke[*ArtifactStorage](i)
		must.NoError(t, err)

		test.NotEqOp[uploads.UploadManager](t, shared, storage.Manager)
		test.NotNil(t, storage.Encryptor)

		test.NoError(t, storage.Close())
		test.EqOp(t, 0, shared.closed)
	})

	T.Run("a configured keyring with no keyset registered fails", func(t *testing.T) {
		t.Parallel()

		i := injector(t, artifactsConfig())

		RegisterArtifactStorage(i)

		_, err := do.Invoke[*ArtifactStorage](i)
		test.Error(t, err)
	})

	T.Run("the three constructors take what it registered", func(t *testing.T) {
		t.Parallel()

		i := injector(t, artifactsConfig())
		do.ProvideValue(i, encryption.Keyset{"artifacts": testArtifactKey})
		do.ProvideValue[database.Client](i, testDBClient(t))

		RegisterArtifactStorage(i)

		storage, err := do.Invoke[*ArtifactStorage](i)
		must.NoError(t, err)

		manager, encryptor, err := invokeArtifacts(i)
		must.NoError(t, err)

		test.EqOp(t, storage.Manager, manager)
		test.EqOp(t, storage.Encryptor, encryptor)

		RegisterStore(i)
		RegisterSweeper(i)

		_, err = do.Invoke[*dataprivacy.Sweeper](i)
		test.NoError(t, err, test.Sprint("the Sweeper needed an upload manager of the container's own"))
	})

	T.Run("a configured block nobody registered is refused rather than written to the shared bucket", func(t *testing.T) {
		t.Parallel()

		i := injector(t, artifactsConfig())
		do.ProvideValue[uploads.UploadManager](i, uploadsnoop.NewUploadManager())
		do.ProvideValue[database.Client](i, testDBClient(t))

		RegisterStore(i)
		RegisterSweeper(i)

		_, err := do.Invoke[*dataprivacy.Sweeper](i)
		test.True(t, errors.Is(err, ErrArtifactStorageUnregistered), test.Sprintf("got %v", err))
	})
}
