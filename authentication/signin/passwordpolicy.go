package signin

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"

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

// AccountPasswordPolicy decides whether a password may replace or join the
// credentials of an account that already exists, knowing which account it is. A
// nil error admits it and any other error refuses it.
//
// It is [PasswordPolicy]'s account-aware sibling, and exists for the rules that
// one cannot express because it is handed a string and nothing else: not the
// password this account already holds, not the username, not one of the last
// few. Where the rule needs no account, [PasswordPolicy] is still the seam —
// it is the only one [Service.Register] asks, because at registration there is
// no account yet — and a service may be built with both. The plain policy runs
// first, so a password it refuses never reaches this one.
//
// It runs on the two doors that write a password onto an existing account,
// [Service.UpdatePassword] and [Service.AttachPassword], and on
// UpdatePassword it runs *after* reauthentication rather than before it,
// which is the opposite of where [PasswordPolicy] sits. That is deliberate. A
// policy that can ask whether a candidate matches the current password is an
// oracle on the current password, and one that ran ahead of the check that
// the caller knows it would answer that question for whoever holds the
// session. After it, the caller has already proven the answer. The cost is
// the one [PasswordPolicy] avoids: a refusal here has spent a hash comparison
// on the current password first.
//
// authentication/passwordreset has no counterpart, and that is not an
// omission. The caller of a reset has proven they hold a link, not that they
// know the password being replaced, and the refusal comes back before the link
// is spent — so a reset door that answered "that is your current password"
// would be an oracle on the old password that could be asked as often as the
// link lives. A deployment whose old passwords are worth protecting is one
// that should not answer that question to anybody who has not already
// answered it.
//
// The error is joined with [ErrPasswordRefused] exactly as [PasswordPolicy]'s
// is, the policy's own error first, and it reaches a gRPC client by the same
// rule. It is logged and traced, so it must carry neither password.
type AccountPasswordPolicy func(ctx context.Context, change *PasswordChange) error

// PasswordChange is what an [AccountPasswordPolicy] is asked about: a password,
// and the account it would be written to.
type PasswordChange struct {
	_ struct{} `json:"-"`

	// User is the account the password would be written to, redacted: it
	// carries no password hash, no second-factor secret and no verification
	// token. A policy that needs to compare against the current password asks
	// MatchesCurrent rather than reading one.
	User *identity.User `json:"-"`

	// MatchesCurrent reports whether a candidate is the password the account
	// holds now, by the service's own Authenticator. It is how a policy refuses
	// "the same as the current one" without the hash ever crossing the seam.
	// It is never nil, and is false for an account that holds no password —
	// which every account reaching AttachPassword is.
	MatchesCurrent func(ctx context.Context, candidate string) (bool, error) `json:"-"`

	// NewPassword is the password that would be written.
	NewPassword string `json:"-"`
}

// checkAccountPassword applies the service's account-aware policy to a password
// about to be written to user, and is nil when the service has none. user is
// the unredacted row: the hash stays here, behind MatchesCurrent.
func (s *Service) checkAccountPassword(ctx context.Context, user *identity.User, password string) error {
	if s.accountPasswordPolicy == nil {
		return nil
	}

	hashed := user.HashedPassword

	change := &PasswordChange{
		User:        user.Redacted(),
		NewPassword: password,
		MatchesCurrent: func(ctx context.Context, candidate string) (bool, error) {
			if hashed == "" {
				return false, nil
			}

			return s.authenticator.PasswordMatches(ctx, hashed, candidate)
		},
	}

	if err := s.accountPasswordPolicy(ctx, change); err != nil {
		return platformerrors.Join(err, ErrPasswordRefused)
	}

	return nil
}
