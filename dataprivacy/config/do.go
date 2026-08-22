package dataprivacycfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/compression"
	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/shredding"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/dataprivacy"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/uploads"

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
// Packaging follows EnsurePackaging: a registered compression.Compressor
// and/or encryption.EncryptorDecryptor is applied, and their absence means
// uncompressed, unencrypted packages.
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

		_, serviceOpts, err := invokePackaging(i)
		if err != nil {
			return nil, err
		}

		do.MustInvoke[*dataprivacy.Fulfiller](i)

		return NewService(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[dataprivacy.Store](i),
			do.MustInvoke[operations.Service](i),
			WithPillars(pillars),
			WithServiceOptions(serviceOpts...),
		)
	})
}

// RegisterFulfiller registers a *dataprivacy.Fulfiller with the injector, which
// registers this package's operation kinds into the *operations.Registry as it
// is built. Packaging follows EnsurePackaging, and the encrypted flag is derived
// from whether an encryption.EncryptorDecryptor is registered.
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

		fulfillerOpts, _, err := invokePackaging(i)
		if err != nil {
			return nil, err
		}

		encryptorDecryptor, err := injection.InvokeOptional[encryption.EncryptorDecryptor](i)
		if err != nil {
			return nil, err
		}

		keys, err := injection.InvokeOptional[shredding.Keys](i)
		if err != nil {
			return nil, err
		}

		if keys != nil {
			fulfillerOpts = append(fulfillerOpts, dataprivacy.WithFulfillerShredder(keys))
		}

		return NewFulfiller(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[dataprivacy.Store](i),
			do.MustInvoke[*dataprivacy.Registry](i),
			do.MustInvoke[*operations.Registry](i),
			do.MustInvoke[uploads.UploadManager](i),
			encryptorDecryptor != nil,
			WithPillars(pillars),
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

// invokePackaging resolves the optional packaging dependencies and turns them
// into fulfiller and service options via EnsurePackaging.
func invokePackaging(i do.Injector) ([]dataprivacy.FulfillerOption, []dataprivacy.ServiceOption, error) {
	compressor, err := injection.InvokeOptional[compression.Compressor](i)
	if err != nil {
		return nil, nil, err
	}

	encryptorDecryptor, err := injection.InvokeOptional[encryption.EncryptorDecryptor](i)
	if err != nil {
		return nil, nil, err
	}

	fulfillerOpts, serviceOpts := EnsurePackaging(compressor, encryptorDecryptor)

	return fulfillerOpts, serviceOpts, nil
}
