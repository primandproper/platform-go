package mail

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"

	"github.com/primandproper/primitives-go/v2/email"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

const (
	// serviceName scopes this package's spans, logger and instruments.
	serviceName = "mail"

	kindKey   = serviceName + ".kind"
	testIDKey = serviceName + ".test_id"
)

// The sentinels this package returns.
var (
	// ErrNilDatabaseClient indicates NewQueuedMailer was handed no client to
	// open its enqueue transactions on.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil queued mailer database client")

	// ErrNilEnqueuer indicates NewQueuedMailer was handed no outbox writer.
	ErrNilEnqueuer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil queued mailer outbox enqueuer")

	// ErrNilEmailer indicates NewDrainer was handed no email.Emailer.
	ErrNilEmailer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil mail drainer emailer")

	// ErrNilRenderer indicates NewDrainer was handed no Renderer.
	ErrNilRenderer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil mail drainer renderer")

	// ErrNilMail indicates a Send was handed a nil mail, or one missing the
	// part its kind cannot be rendered without — the user, the invitation, the
	// issuance or the signup and its links.
	ErrNilMail = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil or incomplete queued mail")

	// ErrUndecodableMail indicates a message on the topic that is not a mail
	// this package wrote: it does not parse, names a kind this binary does not
	// know, or is missing the part its kind needs. The Drainer returns it
	// wrapped in retry.Unretryable, because it will be exactly as undecodable on
	// every later attempt.
	ErrUndecodableMail = platformerrors.New("undecodable queued mail")

	// ErrNothingRendered indicates a Renderer that returned no message and no
	// error. A deployment that does not send one kind of mail does not wire
	// the seam that produces it; a renderer that answers nil is one that was
	// never told about a kind, and the Drainer refuses rather than dropping the
	// mail as though it had been sent.
	ErrNothingRendered = platformerrors.New("mail renderer produced no message")
)

// Kind names which of this module's mails a message is.
type Kind string

// The six kinds, one per seam. The values are the wire's, so a drain built from
// one release reads a message enqueued by another; they are never renamed.
const (
	// KindInvitation is identity.InvitationMailer's mail.
	KindInvitation Kind = "identity.invitation"
	// KindVerification is signin.VerificationMailer's mail.
	KindVerification Kind = "signin.verification"
	// KindHandleReminder is signin.HandleReminderMailer's mail.
	KindHandleReminder Kind = "signin.handle_reminder"
	// KindMagicLink is signin.MagicLinkMailer's mail.
	KindMagicLink Kind = "signin.magic_link"
	// KindPasswordReset is passwordreset.Mailer's mail.
	KindPasswordReset Kind = "passwordreset.reset"
	// KindWaitlistConfirmation is waitlistsgrpc.ConfirmationMailer's mail.
	KindWaitlistConfirmation Kind = "waitlists.confirmation"
)

// String implements fmt.Stringer.
func (k Kind) String() string {
	return string(k)
}

// WaitlistConfirmation is a waitlist confirmation mail with the scope its seam
// was handed beside it. It is the one seam that takes a scope as an argument
// rather than carrying it on the value; every other mail's scope is on the
// user or invitation it carries.
type WaitlistConfirmation struct {
	// Mail is what the seam was handed.
	Mail *waitlistsgrpc.ConfirmationMail
	// Scope is the tenant the signup was written in.
	Scope tenancy.Scope
}

// Mail is one queued mail as the Drainer hands it to a Renderer: exactly the
// value the synchronous seam would have been given, secret included, under the
// field its Kind names. Every other field is nil.
type Mail struct {
	Invitation           *identity.InvitationMail
	Verification         *signin.VerificationMail
	HandleReminder       *signin.HandleReminderMail
	MagicLink            *signin.MagicLinkMail
	PasswordReset        *passwordreset.Mail
	WaitlistConfirmation *WaitlistConfirmation

	// Kind is which of the fields above is set.
	Kind Kind

	// TestID is the correlation identifier the Send was made under, or empty;
	// see ContextWithTestID.
	TestID string
}

// Renderer builds the message for one mail: the sender, the subject, the body,
// and the URL the secret is rendered into. It is the consumer's, as every word
// this module sends is.
//
// An error is retried by the jobs.Pool the Drainer runs under, so a renderer
// whose failure is about the mail rather than the moment — a kind it does not
// render — wraps it in retry.Unretryable.
type Renderer interface {
	Render(ctx context.Context, m *Mail) (*email.OutboundEmailMessage, error)
}

// RendererFunc adapts a function to Renderer.
type RendererFunc func(ctx context.Context, m *Mail) (*email.OutboundEmailMessage, error)

// Render implements Renderer.
func (f RendererFunc) Render(ctx context.Context, m *Mail) (*email.OutboundEmailMessage, error) {
	return f(ctx, m)
}

// message is the wire form of one mail.
//
// It is a struct of its own rather than the seams' values marshaled as they
// stand, because every one of those hides its secret from encoding/json — that
// is what keeps it out of a log line — and a message that dropped it would
// deliver a link nobody can answer. Secret names the credential explicitly, and
// decode puts it back where the seam's value keeps it.
type message struct {
	ExpiresAt   *time.Time           `json:"expiresAt,omitempty"`
	User        *identity.User       `json:"user,omitempty"`
	Invitation  *identity.Invitation `json:"invitation,omitempty"`
	MagicLink   *signin.MagicLink    `json:"magicLink,omitempty"`
	ResetToken  *passwordreset.Token `json:"resetToken,omitempty"`
	Signup      *waitlists.Signup    `json:"signup,omitempty"`
	Confirm     *links.Link          `json:"confirm,omitempty"`
	Unsubscribe *links.Link          `json:"unsubscribe,omitempty"`
	Scope       *tenancy.Scope       `json:"scope,omitempty"`
	Kind        Kind                 `json:"kind"`
	TestID      string               `json:"testID,omitempty"`
	Secret      string               `json:"secret,omitempty"`
}

// decode rebuilds the seam's value from a message, refusing one whose kind is
// unknown or whose required parts are missing.
func (msg *message) decode() (*Mail, error) {
	m := &Mail{Kind: msg.Kind, TestID: msg.TestID}

	switch msg.Kind {
	case KindInvitation:
		if msg.Invitation == nil {
			return nil, platformerrors.Wrapf(ErrUndecodableMail, "%s carries no invitation", msg.Kind)
		}

		m.Invitation = &identity.InvitationMail{Invitation: msg.Invitation, Token: msg.Secret}
	case KindVerification:
		if msg.User == nil || msg.ExpiresAt == nil {
			return nil, platformerrors.Wrapf(ErrUndecodableMail, "%s carries no user or deadline", msg.Kind)
		}

		m.Verification = &signin.VerificationMail{User: msg.User, Token: msg.Secret, ExpiresAt: *msg.ExpiresAt}
	case KindHandleReminder:
		if msg.User == nil {
			return nil, platformerrors.Wrapf(ErrUndecodableMail, "%s carries no user", msg.Kind)
		}

		m.HandleReminder = &signin.HandleReminderMail{User: msg.User}
	case KindMagicLink:
		if msg.User == nil || msg.MagicLink == nil {
			return nil, platformerrors.Wrapf(ErrUndecodableMail, "%s carries no user or link", msg.Kind)
		}

		m.MagicLink = &signin.MagicLinkMail{
			User:     msg.User,
			Issuance: &signin.MagicLinkIssuance{Link: msg.MagicLink, Secret: msg.Secret},
		}
	case KindPasswordReset:
		if msg.User == nil || msg.ResetToken == nil {
			return nil, platformerrors.Wrapf(ErrUndecodableMail, "%s carries no user or token", msg.Kind)
		}

		m.PasswordReset = &passwordreset.Mail{
			User:     msg.User,
			Issuance: &passwordreset.Issuance{Token: msg.ResetToken, Secret: msg.Secret},
		}
	case KindWaitlistConfirmation:
		if msg.Signup == nil || msg.Confirm == nil || msg.Unsubscribe == nil || msg.Scope == nil {
			return nil, platformerrors.Wrapf(ErrUndecodableMail, "%s carries no signup, links or scope", msg.Kind)
		}

		m.WaitlistConfirmation = &WaitlistConfirmation{
			Scope: *msg.Scope,
			Mail: &waitlistsgrpc.ConfirmationMail{
				Signup:      msg.Signup,
				Confirm:     msg.Confirm,
				Unsubscribe: msg.Unsubscribe,
			},
		}
	default:
		return nil, platformerrors.Wrapf(ErrUndecodableMail, "unknown kind %q", msg.Kind)
	}

	return m, nil
}

type testIDContextKey struct{}

// ContextWithTestID returns a context under which every Send carries id on its
// message, for a deliverability canary to recognize its own mail by. An empty
// id carries nothing.
func ContextWithTestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, testIDContextKey{}, id)
}

// TestIDFromContext returns the identifier ContextWithTestID put on ctx, or
// empty.
func TestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(testIDContextKey{}).(string); ok {
		return id
	}

	return ""
}
