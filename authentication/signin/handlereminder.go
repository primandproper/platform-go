package signin

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// HandleReminderMail is what a HandleReminderMailer is handed once somebody has
// asked what they sign in as.
type HandleReminderMail struct {
	_ struct{} `json:"-"`

	// User is who asked, redacted. The handle to put in the mail is its
	// Username, which is the handle the directory is keyed on and the one the
	// password door accepts beside the address the mail is going to.
	User *identity.User `json:"user"`
}

// HandleReminderMailer delivers the one message the handle reminder door sends.
//
// It is a seam rather than a dependency on an email package for
// MagicLinkMailer's reason: the address it comes from, the template and the
// wording are the consumer's, and what this package decides is when — and to
// whom, which is the part a template cannot get wrong because it is never asked.
//
// An error from a Mailer fails [Service.RequestHandleReminder]. Nothing was
// written before it, so a caller retrying is a caller asking again.
type HandleReminderMailer interface {
	SendHandleReminder(ctx context.Context, mail *HandleReminderMail) error
}

// HandleReminderMailerFunc adapts a function to HandleReminderMailer.
type HandleReminderMailerFunc func(ctx context.Context, mail *HandleReminderMail) error

// SendHandleReminder implements HandleReminderMailer.
func (f HandleReminderMailerFunc) SendHandleReminder(ctx context.Context, mail *HandleReminderMail) error {
	return f(ctx, mail)
}

// RequestHandleReminder mails somebody the handle they sign in with.
//
// It is the door a username-and-password deployment has for a person who knows
// their address and has forgotten what they called themselves. A sign-in link is
// not a substitute for it where the deployment does not sign in by link, and a
// password reset is not one either: it answers a forgotten password, and the
// person who has forgotten their handle may know their password perfectly well.
//
// # It answers the same way for everybody
//
// It takes [Service.RequestMagicLink]'s posture whole, for that door's reason.
// An address nobody holds, an address whose owner is banned and an address whose
// owner is terminated are a nil error and no mail, in the same time as the
// address that gets one — see [WithHandleReminderFloor]. Every path is held to
// the floor, the ones that report an error included, and what comes back from
// those is this service failing rather than a fact about the address.
//
// # Who gets one
//
// Somebody whose standing admits a sign-in, and somebody still in
// [github.com/primandproper/platform-go/v14/identity.StatusUnverified], which is
// the reading the sign-in link door takes: a registrant who has forgotten the
// handle they registered with a minute ago is still somebody the registration
// mail already reached, and telling them what it was tells them nothing that
// mail did not. A banned or terminated user is mailed nothing, and
// the caller is told nothing about that.
//
// # Rate limiting is the consumer's
//
// In front of this call, and it is not optional, for the reason
// [Service.RequestMagicLink] gives: this door mails on every request for an
// address somebody holds, and a deployment without a limit in front of it sends
// mail through its own domain at somebody else's direction.
//
// It requires [WithHandleReminderMailer], and refuses with
// [ErrHandleRemindersNotConfigured] until it has one.
func (s *Service) RequestHandleReminder(
	ctx context.Context,
	scope tenancy.Scope,
	emailAddress string,
) (err error) {
	ctx, op, done := s.begin(ctx, opRequestHandleReminder,
		observability.WithValue(scopeKey, scope.String()),
	)
	defer func() { done(err) }()

	// Deferred before the first refusal, as RequestMagicLink's is, so every path
	// through this function is padded, including the ones that return early.
	defer s.padTo(ctx, op, s.clk.Now().Add(s.handleReminderFloor))

	if s.handleReminderMailer == nil {
		return op.Error(ErrHandleRemindersNotConfigured, "requesting a handle reminder")
	}

	if emailAddress == "" {
		return op.Error(ErrEmptyHandle, "requesting a handle reminder")
	}

	user, err := s.directory.GetUserByEmailAddress(ctx, s.client.Reader(), scope, identity.FoldHandle(emailAddress))
	if err != nil {
		if platformerrors.Is(err, identity.ErrUserNotFound) {
			// The enumeration defense: nothing was sent, and the answer is the
			// answer somebody with an account gets.
			op.SpanOnly(reasonKey, identity.ErrUserNotFound.Error())

			return nil
		}

		// The directory failing rather than a fact about this address, and
		// collapsing it into the silent answer would hide an outage behind a
		// door that looks like it worked.
		return op.Error(err, "reading the user a handle reminder was asked for")
	}

	op.Set(userIDKey, user.ID)

	if !admitsMagicLink(user.AccountStatus) {
		// Told to the span and to nobody else, as the sign-in link door tells it.
		op.SpanOnly(reasonKey, statusRefusal(user).Error())

		return nil
	}

	if err = s.handleReminderMailer.SendHandleReminder(ctx, &HandleReminderMail{User: user.Redacted()}); err != nil {
		return op.Error(err, "mailing a handle reminder")
	}

	return nil
}
