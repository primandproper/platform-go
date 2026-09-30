package dataprivacycfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/shredding"

	"github.com/primandproper/primitives-go/v2/compression"
	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/uploads"

	"github.com/samber/do/v2"
)

// RegisterStore registers a dataprivacy.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Store is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (dataprivacy.Store, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewStore(ctx, cfg, client, WithPillars(pillars))
	})
}

// RegisterService registers a dataprivacy.Service with the injector.
//
// A registered compression.Compressor and/or encryption.EncryptorDecryptor is
// what the Service reads artifacts with; their absence means uncompressed,
// unencrypted packages. Both are optional registrations, and both reach the
// Service as [WithCompressor] and [WithEncryptor] — the same two options
// RegisterFulfiller hands the Fulfiller, out of the same container, so the
// codecs an artifact is written with are the ones it is read with. Where
// RegisterArtifactStorage was called, its encryptor is the one used instead —
// see invokeArtifacts.
//
// It depends on *dataprivacy.Fulfiller rather than only on the operations
// Service, and the dependency is there to be ordered rather than used: the
// Fulfiller is what registers this package's kinds, and starting an operation
// resolves its kind at submission. Without the ordering, a container that
// happened to build the Service first would refuse every submission with
// operations.ErrUnknownKind.
//
// It reads artifacts from the uploads.UploadManager RegisterFulfiller writes
// them to, out of the same container, so Download and Open reach what the
// Fulfiller stored — the *ArtifactStorage's where RegisterArtifactStorage was
// called, and the container's own otherwise. RegisterFulfiller already requires
// one, so it is required here too rather than absorbed: a Service without it
// answers every download with dataprivacy.ErrArtifactUnavailable.
//
// A registered audit.Recorder and dataprivacy.ActorResolver are attached, as
// they are to the Fulfiller — see invokeAudit.
//
// Prerequisites: *Config, dataprivacy.Store (see RegisterStore),
// *dataprivacy.Fulfiller (see RegisterFulfiller), operations.Service and
// uploads.UploadManager must be registered in the injector before the Service
// is invoked.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (dataprivacy.Service, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		compressor, err := invokeCompressor(i)
		if err != nil {
			return nil, err
		}

		uploadManager, encryptor, err := invokeArtifacts(i)
		if err != nil {
			return nil, err
		}

		// Resolved for the ordering rather than for the value: the Fulfiller is
		// what registers this package's kinds, and an operation's kind is
		// resolved at submission. The blank is what says so — there is nothing
		// to hold, only something that has to have happened first.
		if _, err = do.Invoke[*dataprivacy.Fulfiller](i); err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		store, err := do.Invoke[dataprivacy.Store](i)
		if err != nil {
			return nil, err
		}

		service, err := do.Invoke[operations.Service](i)
		if err != nil {
			return nil, err
		}

		recorder, actor, err := invokeAudit(i)
		if err != nil {
			return nil, err
		}

		serviceOpts := []dataprivacy.ServiceOption{dataprivacy.WithServiceUploadManager(uploadManager)}
		if recorder != nil {
			serviceOpts = append(serviceOpts, dataprivacy.WithServiceAuditRecorder(recorder))
		}
		if actor != nil {
			serviceOpts = append(serviceOpts, dataprivacy.WithActorResolver(actor))
		}

		return NewService(
			ctx,
			cfg,
			client,
			store,
			service,
			WithPillars(pillars),
			WithCompressor(compressor),
			WithEncryptor(encryptor),
			WithServiceOptions(serviceOpts...),
		)
	})
}

// RegisterFulfiller registers a *dataprivacy.Fulfiller with the injector, which
// registers this package's operation kinds into the *operations.Registry as it
// is built. A registered compression.Compressor and/or
// encryption.EncryptorDecryptor is what the Fulfiller writes artifacts with,
// and the encryptor is also what decides whether a completion notification may
// carry a download link — see [WithEncryptor]. Their absence means
// uncompressed, unencrypted packages and a link that works.
//
// Where RegisterArtifactStorage was called, the *ArtifactStorage it registered
// is where artifacts are written and what they are sealed with, in place of the
// container's own upload manager and encryptor — see invokeArtifacts.
//
// A registered shredding.Keys makes every erasure destroy the subject's data
// key, which is what carries an erasure into backups already taken. Its absence
// means erasure deletes rows and nothing more — the older, narrower guarantee,
// and the right one for an application that encrypts nothing per subject.
//
// A registered dataprivacy.Notifier is who the Fulfiller tells when a request
// finishes, and it is the only way an export's link reaches the subject: with
// none, the export is produced and nobody is told. A registered audit.Recorder
// and dataprivacy.ActorResolver are attached, as they are to the Service.
//
// Prerequisites: *Config, dataprivacy.Store (see RegisterStore),
// *dataprivacy.Registry (the application's collectors and erasers),
// *operations.Registry, and uploads.UploadManager must be registered in the
// injector before the Fulfiller is invoked.
func RegisterFulfiller(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*dataprivacy.Fulfiller, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		compressor, err := invokeCompressor(i)
		if err != nil {
			return nil, err
		}

		uploadManager, encryptor, err := invokeArtifacts(i)
		if err != nil {
			return nil, err
		}

		keys, err := injection.InvokeOptional[shredding.Keys](i)
		if err != nil {
			return nil, err
		}

		var fulfillerOpts []dataprivacy.FulfillerOption
		if keys != nil {
			fulfillerOpts = append(fulfillerOpts, dataprivacy.WithFulfillerShredder(keys))
		}

		notifier, err := injection.InvokeOptional[dataprivacy.Notifier](i)
		if err != nil {
			return nil, platformerrors.Wrap(err, "invoking the data privacy notifier")
		}

		if notifier != nil {
			fulfillerOpts = append(fulfillerOpts, dataprivacy.WithFulfillerNotifier(notifier))
		}

		recorder, actor, err := invokeAudit(i)
		if err != nil {
			return nil, err
		}

		if recorder != nil {
			fulfillerOpts = append(fulfillerOpts, dataprivacy.WithFulfillerAuditRecorder(recorder))
		}
		if actor != nil {
			fulfillerOpts = append(fulfillerOpts, dataprivacy.WithFulfillerActorResolver(actor))
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		store, err := do.Invoke[dataprivacy.Store](i)
		if err != nil {
			return nil, err
		}

		dataprivacyRegistry, err := do.Invoke[*dataprivacy.Registry](i)
		if err != nil {
			return nil, err
		}

		operationsRegistry, err := do.Invoke[*operations.Registry](i)
		if err != nil {
			return nil, err
		}

		return NewFulfiller(
			ctx,
			cfg,
			client,
			store,
			dataprivacyRegistry,
			operationsRegistry,
			uploadManager,
			WithPillars(pillars),
			WithCompressor(compressor),
			WithEncryptor(encryptor),
			WithFulfillerOptions(fulfillerOpts...),
		)
	})
}

// RegisterSweeper registers a *dataprivacy.Sweeper with the injector.
//
// It deletes artifacts from where RegisterFulfiller wrote them: the
// *ArtifactStorage's upload manager where RegisterArtifactStorage was called,
// and the container's own otherwise.
//
// Prerequisites: *Config, dataprivacy.Store (see RegisterStore), and
// uploads.UploadManager must be registered in the injector before the Sweeper
// is invoked.
func RegisterSweeper(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*dataprivacy.Sweeper, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		store, err := do.Invoke[dataprivacy.Store](i)
		if err != nil {
			return nil, err
		}

		uploadManager, _, err := invokeArtifacts(i)
		if err != nil {
			return nil, err
		}

		return NewSweeper(ctx, cfg, store, uploadManager, WithPillars(pillars))
	})
}

// RegisterArtifactStorage registers the *ArtifactStorage the Fulfiller writes
// artifacts to, the Service reads them from and the Sweeper deletes them from,
// built from Config.Artifacts.
//
// What the block configures is built: an upload manager of the artifacts' own
// from Storage, and from Encryption a keyring over the container's
// encryption.Keyset, which must then be registered. What it leaves out is taken
// from the container as it would be without this call — its
// uploads.UploadManager, required, and its encryption.EncryptorDecryptor, if
// any — so calling this changes nothing for a Config with no Artifacts block.
//
// It is registered once and resolved by all three, which is the point: a
// manager built three times is three buckets' worth of clients, and three
// separate buckets under the memory provider.
//
// Prerequisites: *Config, and uploads.UploadManager unless Artifacts.Storage is
// configured, and encryption.Keyset when Artifacts.Encryption is.
func RegisterArtifactStorage(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*ArtifactStorage, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		// Normalized before it is read, so an Artifacts block env parsing
		// allocated and nobody filled in does not ask for a keyset.
		if err = cfg.prepare(ctx); err != nil {
			return nil, err
		}

		var keys encryption.Keyset
		if cfg.Artifacts != nil && cfg.Artifacts.Encryption != nil {
			if keys, err = do.Invoke[encryption.Keyset](i); err != nil {
				return nil, platformerrors.Wrap(err, "invoking the data privacy artifact keyset")
			}
		}

		storage, err := NewArtifactStorage(ctx, cfg, keys, WithPillars(pillars))
		if err != nil {
			return nil, err
		}

		if storage.Manager == nil {
			if storage.Manager, err = do.Invoke[uploads.UploadManager](i); err != nil {
				return nil, platformerrors.Join(err, storage.Close())
			}
		}

		if storage.Encryptor == nil {
			if storage.Encryptor, err = injection.InvokeOptional[encryption.EncryptorDecryptor](i); err != nil {
				return nil, platformerrors.Join(err, storage.Close())
			}
		}

		return storage, nil
	})
}

// invokeCompressor resolves the optional codec an artifact is compressed with.
// Absent is uncompressed.
func invokeCompressor(i do.Injector) (compression.Compressor, error) {
	return injection.InvokeOptional[compression.Compressor](i)
}

// invokeArtifacts resolves where artifacts are kept and what they are sealed
// with, for the Fulfiller, the Service and the Sweeper alike.
//
// Where RegisterArtifactStorage was called its *ArtifactStorage answers both.
// Otherwise the answer is the container's own, as it always was: its
// uploads.UploadManager, required, and its encryption.EncryptorDecryptor, if
// any. There is nothing to check the two against each other: whether artifacts
// are encrypted is whether this returned an encryptor, and no second statement
// of that fact exists to disagree with it.
//
// A Config whose Artifacts block configures something, in a container that
// never registered the storage built from it, is refused with
// ErrArtifactStorageUnregistered rather than quietly written to the shared
// bucket in the clear.
func invokeArtifacts(i do.Injector) (uploads.UploadManager, encryption.EncryptorDecryptor, error) {
	registered, err := injection.InvokeOptional[*ArtifactStorage](i)
	if err != nil {
		return nil, nil, err
	}

	if registered != nil {
		return registered.Manager, registered.Encryptor, nil
	}

	ctx, err := do.Invoke[context.Context](i)
	if err != nil {
		return nil, nil, err
	}

	cfg, err := do.Invoke[*Config](i)
	if err != nil {
		return nil, nil, err
	}

	if err = cfg.prepare(ctx); err != nil {
		return nil, nil, err
	}

	if cfg.Artifacts.configured() {
		return nil, nil, ErrArtifactStorageUnregistered
	}

	uploadManager, err := do.Invoke[uploads.UploadManager](i)
	if err != nil {
		return nil, nil, err
	}

	encryptorDecryptor, err := injection.InvokeOptional[encryption.EncryptorDecryptor](i)
	if err != nil {
		return nil, nil, err
	}

	return uploadManager, encryptorDecryptor, nil
}

// invokeAudit resolves the audit log this package records to and who its
// entries name, for the Service and the Fulfiller alike. Either may be absent:
// no recorder records nothing, and no resolver attributes every entry to
// audit.ActorSystem, as dataprivacy.ActorResolver describes.
//
// Only absence is absorbed. A recorder that is registered and fails to build is
// returned, because the package calls auditing "not decoration" — an export is
// the most sensitive object an application produces, and a Service that
// quietly ran without the log it was configured with would produce one with
// no record of who asked.
func invokeAudit(i do.Injector) (audit.Recorder, dataprivacy.ActorResolver, error) {
	recorder, err := injection.InvokeOptional[audit.Recorder](i)
	if err != nil {
		return nil, nil, platformerrors.Wrap(err, "invoking the audit recorder")
	}

	actor, err := injection.InvokeOptional[dataprivacy.ActorResolver](i)
	if err != nil {
		return nil, nil, platformerrors.Wrap(err, "invoking the data privacy actor resolver")
	}

	return recorder, actor, nil
}
