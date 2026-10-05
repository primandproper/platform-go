package passwordreset

import (
	"context"

	"github.com/primandproper/platform-go/v15/authentication/signin"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// PasswordPolicy decides whether the password a reset would write may be
// written. A nil error admits it and any other error refuses it.
//
// The rule is the consumer's and this package ships none. It holds the seam
// because Service.Complete is not always the consumer's to call: a mounted gRPC
// server calls it, and nothing runs between the wire and the write.
//
// It is an alias of authentication/signin.PasswordPolicy rather than a type of
// its own, because a deployment whose reset door admitted what its
// password-change door refused has a policy with a way around it. One type is
// one key in a container: an application that registers its policy once has it
// resolved by signincfg and passwordresetcfg alike, and a second, separately
// named type would be a registration that reached every door but this one.
// signin.PasswordPolicy states the whole of the contract — the joined
// ErrPasswordRefused, whose words reach a gRPC client, and that the error must
// not carry the password it refuses — and this door keeps all of it.
//
// It runs before the password is hashed and before the token is spent, so a
// refusal costs the caller nothing: the link is still live, and they choose
// another password and send it again.
//
// signin.AccountPasswordPolicy has no counterpart here; that type's
// documentation says why a reset door must not answer it.
type PasswordPolicy = signin.PasswordPolicy

// checkPassword applies the service's policy to a password about to be written,
// and is nil when the service has none.
func (s *Service) checkPassword(ctx context.Context, password string) error {
	if s.passwordPolicy == nil {
		return nil
	}

	if err := s.passwordPolicy(ctx, password); err != nil {
		return platformerrors.Join(err, ErrPasswordRefused)
	}

	return nil
}
