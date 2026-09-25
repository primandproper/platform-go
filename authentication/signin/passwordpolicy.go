package signin

import (
	"context"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// PasswordPolicy decides whether a password a caller has chosen may be written.
// A nil error admits it and any other error refuses it.
//
// This package still holds no policy — the rule is the consumer's, and nothing
// here ships one. What it holds is the seam, and it holds it here rather than
// leaving the rule to be applied in front of each call because a call is not
// always the consumer's to make. A mounted gRPC server is what calls
// [Service.Register], [Service.UpdatePassword] and [Service.AttachPassword], and
// nothing runs between the wire and the write; an interceptor inspecting three
// request types would be a local copy of this decision that a fourth
// password-writing door would silently miss. Applied inside the service, the
// rule reaches every door that writes a password, over every transport and in
// process alike.
//
// It runs after the request has been read and its state checked, and before
// anything is hashed or written. So a refusal costs the caller nothing: a
// registration wrote nobody, a verification link is still live, and the current
// password was not spent on a hash comparison.
//
// Whatever it returns is a refusal. A policy that consults something that can
// fail — a breached-password service, say — decides for itself whether an
// outage admits the password or refuses it, because this package cannot tell
// the two apart and will not guess.
//
// The error is returned to the caller joined with [ErrPasswordRefused], the
// policy's own error first, and that order is what lets a consumer's words reach
// a client. On gRPC the status message is the first client-safe sentinel in the
// chain: a policy returning an error of the consumer's own, registered with
// errors/grpc.RegisterClientSafeSentinels, is quoted — "use at least twelve
// characters" — and one returning anything else is passed over, so the client is
// told ErrPasswordRefused's words instead. Registering a client-safe *reason*
// for it too would replace PASSWORD_REFUSED as the identifier, which breaks a
// client that switches on PASSWORD_REFUSED; register a message only unless that
// is the intent. The error is also logged and traced, so it must not carry the
// password it is refusing.
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
