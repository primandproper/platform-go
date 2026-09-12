package dataprivacycfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/shredding"

	"github.com/primandproper/primitives-go/v2/compression"
	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	"github.com/primandproper/primitives-go/v2/database"
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

		return NewStore(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}

// RegisterService registers a dataprivacy.Service with the injector.
//
// A registered compression.Compressor and/or encryption.EncryptorDecryptor is
// what the Service reads artifacts with; their absence means uncompressed,
// unencrypted packages. Both are optional registrations, and both reach the
// Service as [WithCompressor] and [WithEncryptor] — the same two options
// RegisterFulfiller hands the Fulfiller, out of the same container, so the
// codecs an artifact is written with are the ones it is read with.
//
// It depends on *dataprivacy.Fulfiller rather than only on the operations
// Service, and the dependency is there to be ordered rather than used: the
// Fulfiller is what registers this package's kinds, and starting an operation
// resolves its kind at submission. Without the ordering, a container that
// happened to build the Service first would refuse every submission with
// operations.ErrUnknownKind.
//
// Prerequisites: *Config, dataprivacy.Store (see RegisterStore),
// *dataprivacy.Fulfiller (see RegisterFulfiller), and operations.Service must be
// registered in the injector before the Service is invoked.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (dataprivacy.Service, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		compressor, encryptor, err := invokeCodecs(i)
		if err != nil {
			return nil, err
		}

		do.MustInvoke[*dataprivacy.Fulfiller](i)

		return NewService(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			do.MustInvoke[dataprivacy.Store](i),
			do.MustInvoke[operations.Service](i),
			WithPillars(pillars),
			WithCompressor(compressor),
			WithEncryptor(encryptor),
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
// A registered shredding.Keys makes every erasure destroy the subject's data
// key, which is what carries an erasure into backups already taken. Its absence
// means erasure deletes rows and nothing more — the older, narrower guarantee,
// and the right one for an application that encrypts nothing per subject.
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

		compressor, encryptor, err := invokeCodecs(i)
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

		return NewFulfiller(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			do.MustInvoke[dataprivacy.Store](i),
			do.MustInvoke[*dataprivacy.Registry](i),
			do.MustInvoke[*operations.Registry](i),
			do.MustInvoke[uploads.UploadManager](i),
			WithPillars(pillars),
			WithCompressor(compressor),
			WithEncryptor(encryptor),
			WithFulfillerOptions(fulfillerOpts...),
		)
	})
}

// RegisterSweeper registers a *dataprivacy.Sweeper with the injector.
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

		return NewSweeper(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[dataprivacy.Store](i),
			do.MustInvoke[uploads.UploadManager](i),
			WithPillars(pillars),
		)
	})
}

// invokeCodecs resolves the two optional codecs an artifact is written with and
// read with. Either may be absent, which is uncompressed and unencrypted.
//
// Both registrations are resolved here rather than in each provider so that the
// Fulfiller and the Service are configured out of the same two lookups. There
// is nothing to check them against each other: whether artifacts are encrypted
// is whether this returned an encryptor, and no second statement of that fact
// exists to disagree with it.
func invokeCodecs(i do.Injector) (compression.Compressor, encryption.EncryptorDecryptor, error) {
	compressor, err := injection.InvokeOptional[compression.Compressor](i)
	if err != nil {
		return nil, nil, err
	}

	encryptorDecryptor, err := injection.InvokeOptional[encryption.EncryptorDecryptor](i)
	if err != nil {
		return nil, nil, err
	}

	return compressor, encryptorDecryptor, nil
}
