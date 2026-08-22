package requestsigning

import (
	"context"
	stderrors "errors"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/secrets"
)

// KeySource resolves the keyring a signature is minted or checked under, at the
// moment it is needed.
//
// It is resolved per operation rather than captured at construction, and that
// is the whole point of the indirection: a key read once at boot and held for
// the life of the process is a key that cannot be rotated without a restart,
// which is why signing material tends to sit in config files and never change.
type KeySource interface {
	// Keyring returns the keys in force right now.
	Keyring(ctx context.Context) (Keyring, error)
}

// KeySourceFunc adapts a function to KeySource. It is the seam for a keyring
// assembled some way this package does not ship — a per-tenant lookup, a
// base64-encoded secret, a keyring derived from a database row.
type KeySourceFunc func(ctx context.Context) (Keyring, error)

var _ KeySource = KeySourceFunc(nil)

// Keyring calls f.
func (f KeySourceFunc) Keyring(ctx context.Context) (Keyring, error) { return f(ctx) }

// StaticKeyring returns a KeySource that always answers with keyring.
//
// It is for tests and for a keyring a caller already holds in memory. It cannot
// rotate: nothing re-reads it, so a process using it keeps signing under the
// same key until it restarts. Reach for NewSecretKeySource in anything
// long-lived.
func StaticKeyring(keyring Keyring) KeySource {
	return KeySourceFunc(func(context.Context) (Keyring, error) { return keyring, nil })
}

// SecretKeySource reads a keyring out of a secrets.SecretSource by name. It is
// exported, and returned by NewSecretKeySource, so a caller can depend on the
// source it built rather than on the KeySource seam.
type SecretKeySource struct {
	source       secrets.SecretSource
	currentName  string
	previousName string
}

var _ KeySource = (*SecretKeySource)(nil)

// NewSecretKeySource reads the keyring from source on every operation, so
// signing material lives where secrets live rather than in a config file.
//
// currentName is required. previousName names the outgoing key of a rotation
// window and may be empty, which is the steady state; a previousName that
// resolves to secrets.ErrSecretNotFound, or to an empty value, is treated the
// same way — as a window that is not open. A missing *current* key is an error,
// because a signer with no key is a signer that cannot sign and a verifier with
// no key would accept nothing.
//
// # Rotation
//
// Reading per operation is what makes rotation a secret-store change rather
// than a deploy, and it is only affordable because secrets.NewCachingSource
// exists: wrap the provider in one and these reads are answered from memory,
// with the backend consulted once per TTL. Pair it with secrets.WithRefresh so
// a rotation is noticed on a timer instead of on the next request, and with
// OnChange if something else in the process must be told.
//
// The values are used as key material verbatim, with no decoding. A secret
// stored base64- or hex-encoded should be wrapped in a KeySourceFunc that
// decodes it, so that what the store holds and what the HMAC consumes cannot
// drift apart silently.
func NewSecretKeySource(source secrets.SecretSource, currentName, previousName string) (*SecretKeySource, error) {
	if source == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil secret source")
	}

	if currentName == "" {
		return nil, platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "current signing key name")
	}

	return &SecretKeySource{
		source:       source,
		currentName:  currentName,
		previousName: previousName,
	}, nil
}

// Keyring resolves both names.
func (s *SecretKeySource) Keyring(ctx context.Context) (Keyring, error) {
	current, err := s.source.GetSecret(ctx, s.currentName)
	if err != nil {
		return Keyring{}, platformerrors.Wrapf(err, "resolving signing key %q", s.currentName)
	}

	if current == "" {
		return Keyring{}, platformerrors.Wrapf(ErrNoSigningKey, "secret %q is empty", s.currentName)
	}

	keyring := Keyring{Current: []byte(current)}

	if s.previousName == "" {
		return keyring, nil
	}

	previous, err := s.source.GetSecret(ctx, s.previousName)
	if err != nil {
		// An absent previous key is the steady state, not a failure: the
		// rotation window is simply closed. Every other error is a store this
		// process could not reach, and answering with a half-resolved keyring
		// would silently narrow what the verifier accepts.
		if stderrors.Is(err, secrets.ErrSecretNotFound) {
			return keyring, nil
		}

		return Keyring{}, platformerrors.Wrapf(err, "resolving previous signing key %q", s.previousName)
	}

	keyring.Previous = []byte(previous)

	return keyring, nil
}
