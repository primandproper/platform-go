package sessions

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/random"
)

// NewID mints a session identifier: DefaultIDByteLength bytes from the
// process's secure random source, base64url-encoded so it travels in a cookie
// unescaped.
//
// It deliberately does not use identifiers.New. That mints an xid, which is a
// timestamp, a machine identifier, a process identifier, and a counter — sortable
// by design and therefore guessable by construction. Everywhere else in this
// module that is a feature; here it would mean an attacker who holds one
// identifier can enumerate the ones minted around it. A session identifier is a
// bearer credential and has to come from crypto/rand.
//
// The generator is package-level rather than injected. There is exactly one
// correct source of randomness for this value, and an option to replace it
// would be an option to weaken it.
func NewID(ctx context.Context) (string, error) {
	id, err := random.GenerateBase64EncodedString(ctx, DefaultIDByteLength)
	if err != nil {
		return "", platformerrors.Wrap(err, "minting session identifier")
	}

	return id, nil
}
