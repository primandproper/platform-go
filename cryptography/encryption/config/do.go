package encryptioncfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterEncryptorDecryptor registers an encryption.EncryptorDecryptor — a
// keyring — with the injector.
//
// Consumers must provide an encryption.Keyset into the container (e.g. via
// do.ProvideValue(i, encryption.Keyset{"k1": material})). The keyset is
// resolved as its named type rather than a bare map so it cannot collide with
// an unrelated map registered in the same container.
//
// Every key the deployment can still decrypt with belongs in that keyset, not
// just the current one. A keyset trimmed to the current key alone makes every
// ciphertext written before the last rotation unreadable, and it does so
// silently until something tries to read one.
func RegisterEncryptorDecryptor(i do.Injector) {
	do.Provide(i, func(i do.Injector) (encryption.EncryptorDecryptor, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewKeyring(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[encryption.Keyset](i),
			WithPillars(pillars),
		)
	})
}
