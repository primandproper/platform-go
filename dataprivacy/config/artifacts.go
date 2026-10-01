package dataprivacycfg

import (
	"context"

	"github.com/primandproper/primitives-go/v2/config/cfgnorm"
	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	encryptioncfg "github.com/primandproper/primitives-go/v2/cryptography/encryption/config"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/uploads"
	"github.com/primandproper/primitives-go/v2/uploads/objectstorage"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// ErrArtifactStorageUnregistered indicates a Config whose Artifacts block
// configures something, resolved by a container in which RegisterArtifactStorage
// was never called.
//
// It is refused rather than ignored. The block exists so that an export is
// written somewhere other than the application's shared uploads, or sealed
// under a key the application does not otherwise use; silently writing it to
// the shared bucket in the clear is the outcome that configuration was written
// to prevent.
var ErrArtifactStorageUnregistered = errors.New(
	"dataprivacy artifact storage is configured but RegisterArtifactStorage was not called")

// ArtifactsConfig says where export artifacts are kept and what they are sealed
// with, when that is not simply the application's own uploads and keyring.
//
// It exists so a deployment that encrypts its exports — the configuration
// dataprivacy recommends — configures that rather than wiring it: an upload
// manager built for the artifacts alone and a keyring built for them, handed to
// the Fulfiller that writes, the Service that reads and the Sweeper that
// deletes, out of one value so that the three cannot be handed different ones.
//
// Each half is optional. An absent Storage writes artifacts to the
// uploads.UploadManager a container already has, and an absent Encryption
// seals them with the encryption.EncryptorDecryptor it already has, if any.
// That is what a Config with no Artifacts block at all means too.
type ArtifactsConfig struct {
	_ struct{} `json:"-" yaml:"-"`

	// Storage is the bucket artifacts are written to, when it is not the
	// application's shared one. A bucket of their own is what lets its
	// retention and access be set for the most sensitive objects the
	// application makes rather than for its avatars.
	Storage *objectstorage.Config `env:",init" envPrefix:"STORAGE_" json:"storage,omitempty" yaml:"storage,omitempty"`

	// Encryption is the keyring artifacts are sealed under at rest. Its keys
	// are the encryption.Keyset the caller supplies — NewArtifactStorage's
	// argument, or the container's for RegisterArtifactStorage — and
	// CurrentKeyID names which of them new artifacts are written under.
	//
	// Configuring it turns off Service.Download, since a signed URL to
	// ciphertext is a file nobody can open, and makes dataprivacy/http's
	// artifact route the way an export reaches its subject.
	Encryption *encryptioncfg.Config `env:",init" envPrefix:"ENCRYPTION_" json:"encryption,omitempty" yaml:"encryption,omitempty"`
}

var _ validation.ValidatableWithContext = (*ArtifactsConfig)(nil)

// ValidateWithContext validates an ArtifactsConfig.
//
// It releases the halves env parsing allocated and nobody filled in first, so
// that a present half means somebody configured it — the same normalization
// the Config above it applies to the block as a whole.
func (cfg *ArtifactsConfig) ValidateWithContext(ctx context.Context) error {
	if err := cfgnorm.UnconfiguredToNil(cfg); err != nil {
		return err
	}

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Storage),
		validation.Field(&cfg.Encryption),
	)
}

// configured reports whether the block asks for anything this package builds.
func (cfg *ArtifactsConfig) configured() bool {
	return cfg != nil && (cfg.Storage != nil || cfg.Encryption != nil)
}

// ArtifactStorage is where export artifacts are written and read, and what they
// are sealed with: the two things the Fulfiller, the Service and the Sweeper
// must agree on, as one value.
type ArtifactStorage struct {
	// Manager holds the artifacts. It is nil only for a storage built with
	// nothing to build it from, which NewFulfiller reads as an erasure-only
	// deployment.
	Manager uploads.UploadManager

	// Encryptor seals the artifacts at rest. Nil means they are not
	// encrypted, and is the one reading of that fact there is — see
	// WithEncryptor.
	Encryptor encryption.EncryptorDecryptor

	// owned is the manager this package built, which is the only one Close
	// releases. A manager taken from the container is the container's to
	// close.
	owned uploads.UploadManager
}

// Close releases the upload manager this package built for the artifacts, and
// nothing it was handed.
func (s *ArtifactStorage) Close() error {
	if s == nil || s.owned == nil {
		return nil
	}

	return s.owned.Close()
}

// NewArtifactStorage builds what cfg.Artifacts configures: an upload manager
// from Storage and a keyring over keys from Encryption. A half that is not
// configured is left nil, for a caller to fill from what it already has; a
// Config with no Artifacts block builds nothing.
//
// keys is read only when Encryption is configured, and must then hold every key
// an artifact still in storage was written under, not just the current one —
// an artifact sealed before a rotation is otherwise unreadable, and is found to
// be so by the subject who asked for it.
func NewArtifactStorage(
	ctx context.Context,
	cfg *Config,
	keys encryption.Keyset,
	opts ...Option,
) (*ArtifactStorage, error) {
	o := newOptions(opts)

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	out := &ArtifactStorage{}

	if cfg.Artifacts == nil {
		return out, nil
	}

	if cfg.Artifacts.Storage != nil {
		uploader, err := objectstorage.NewUploadManager(ctx, cfg.Artifacts.Storage,
			objectstorage.WithLogger(o.logger),
			objectstorage.WithTracerProvider(o.tracerProvider),
			objectstorage.WithMetricsProvider(o.metricsProvider),
		)
		if err != nil {
			return nil, errors.Wrap(err, "building dataprivacy artifact storage")
		}

		out.Manager, out.owned = uploader, uploader
	}

	if cfg.Artifacts.Encryption != nil {
		keyring, err := encryptioncfg.NewKeyring(ctx, cfg.Artifacts.Encryption, keys,
			encryptioncfg.WithLogger(o.logger),
			encryptioncfg.WithTracerProvider(o.tracerProvider),
			encryptioncfg.WithMetricsProvider(o.metricsProvider),
		)
		if err != nil {
			return nil, errors.Join(errors.Wrap(err, "building dataprivacy artifact keyring"), out.Close())
		}

		out.Encryptor = keyring
	}

	return out, nil
}
