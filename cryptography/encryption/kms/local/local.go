package local

import (
	"context"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
)

const name = "local_key_wrapper"

// KeyWrapper is the in-process encryption.KeyWrapper implementation: it wraps
// key material with a Cipher held in this process. It is exported, and returned
// by NewKeyWrapper, so a caller who has chosen local wrapping can depend on that
// choice rather than on the interface every wrapper shares.
type KeyWrapper struct {
	o11y   observability.Observer
	cipher encryption.Cipher
}

var _ encryption.KeyWrapper = (*KeyWrapper)(nil)

// NewKeyWrapper builds a KeyWrapper over cipher.
//
// The wrapping key lives in this process, which is the whole difference
// between this and the cloud implementations: it is reachable in a heap dump,
// in a core file, and by anything that can read this process's memory. That
// makes it the right choice for development and for deployments with no KMS,
// and the wrong choice anywhere the point of wrapping is that the wrapping key
// is somewhere the application is not.
//
// It is still meaningfully better than storing data keys in the clear. An
// attacker with the database and not the process still gets nothing.
func NewKeyWrapper(cipher encryption.Cipher, opts ...Option) (*KeyWrapper, error) {
	if cipher == nil {
		return nil, errors.Wrap(encryption.ErrNilCipher, "local key wrapper")
	}

	o := newOptions(opts)

	return &KeyWrapper{
		o11y:   observability.NewObserver(name, o.logger, o.tracerProvider),
		cipher: cipher,
	}, nil
}

func (w *KeyWrapper) Wrap(ctx context.Context, key, associatedData []byte) ([]byte, error) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	// Deliberately no length or content logging. Everything passing through
	// here is key material, and the observability that is routine one layer up
	// is a leak at this one.
	wrapped, err := w.cipher.Seal(ctx, key, associatedData)
	if err != nil {
		return nil, op.Error(err, "wrapping key")
	}

	return wrapped, nil
}

func (w *KeyWrapper) Unwrap(ctx context.Context, wrapped, associatedData []byte) ([]byte, error) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	key, err := w.cipher.Open(ctx, wrapped, associatedData)
	if err != nil {
		return nil, op.Error(err, "unwrapping key")
	}

	return key, nil
}
