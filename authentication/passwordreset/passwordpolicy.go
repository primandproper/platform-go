package passwordreset

import (
	"context"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// PasswordPolicy decides whether the password a reset would write may be
// written. A nil error admits it and any other error refuses it.
//
// The rule is the consumer's and this package ships none. It holds the seam
// because Service.Complete is not always the consumer's to call: a mounted gRPC
// server calls it, and nothing runs between the wire and the write. It is the
// same seam, and the same shape, as authentication/signin.PasswordPolicy, so one
// function serves both — a deployment whose reset door admitted what its
// password-change door refused has a policy with a way around it.
//
// It runs before the password is hashed and before the token is spent, so a
// refusal costs the caller nothing: the link is still live, and they choose
// another password and send it again.
//
// Whatever it returns is a refusal; a policy that consults something that can
// fail decides for itself which way an outage goes. The error is returned
// joined with [ErrPasswordRefused], the policy's own error first, so a policy
// returning an error the consumer registered with
// errors/grpc.RegisterClientSafeSentinels has its own words quoted on the wire
// ahead of this package's — see authentication/signin.PasswordPolicy, which
// states the whole of that. The error is logged and traced, so it must not
// carry the password it is refusing.
type PasswordPolicy func(ctx context.Context, password string) error

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
