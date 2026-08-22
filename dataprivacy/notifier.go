package dataprivacy

import (
	"context"
	"fmt"
	"html"
	"time"

	"github.com/primandproper/platform-go/v13/email"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Notification is what a Notifier is handed when a request reaches a terminal
// state.
type Notification struct {
	// ExpiresAt is when DownloadURL stops working. Zero when there is no URL.
	ExpiresAt time.Time
	// Request is the request that reached a terminal state.
	Request *Request
	// DownloadURL is a freshly minted, expiring URL for the artifact. Empty for
	// an erasure, for a failure, and whenever the Service cannot mint one — an
	// encrypted artifact or a provider that cannot sign.
	//
	// It is minted at notification time rather than at completion so that its
	// short expiry starts when the subject is told, not when the runner
	// finished. A URL that expired before the mail was delivered is a support
	// ticket the subject opens on day 29.
	DownloadURL string
}

// Notifier tells somebody a request is done.
//
// It is an interface rather than a fixed email template because the library
// cannot write the message. It does not know the subject's email address — a
// Subject is an opaque ID — nor the tone, the language, or the legal boilerplate
// the jurisdiction requires. What it does know is when to send, and that is what
// this seam supplies.
//
// A Notifier's error does not fail the request. The export was produced and the
// erasure ran; a mail server being down does not undo either, and retrying the
// fulfillment to retry the mail would re-run the collectors. Failures are logged
// and counted.
type Notifier interface {
	Notify(ctx context.Context, notification *Notification) error
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(ctx context.Context, notification *Notification) error

// Notify implements Notifier.
func (f NotifierFunc) Notify(ctx context.Context, notification *Notification) error {
	return f(ctx, notification)
}

// Recipient is who to mail, resolved from a Subject.
type Recipient struct {
	// Address is the email address. Required.
	Address string
	// Name is the display name, if there is one.
	Name string
}

// RecipientResolver maps a Subject to who should be told about it.
//
// It is required by NewEmailNotifier and has no default, because the mapping is
// the one piece of this that only the application has: a Subject carries an
// opaque ID, and turning that into an address is a database read this package
// has no business performing. Returning a nil Recipient with a nil error means
// "do not mail anyone about this", which is the right answer for a subject who
// has just been erased.
type RecipientResolver func(ctx context.Context, subject Subject) (*Recipient, error)

// EmailNotifier sends completion mail through an email.Emailer.
//
// It is deliberately plain. The message it renders is a serviceable default
// rather than a good one, and an application with a template system should
// implement Notifier directly instead — this exists so that wiring up the
// package end to end does not require writing one first.
type EmailNotifier struct {
	emailer  email.Emailer
	resolve  RecipientResolver
	renderer MessageRenderer
	from     Recipient
}

// MessageRenderer builds the subject line and HTML body for a notification.
type MessageRenderer func(notification *Notification, to Recipient) (subject, htmlBody string)

var _ Notifier = (*EmailNotifier)(nil)

// NewEmailNotifier builds a Notifier over an email.Emailer.
//
// from is the sender. resolve turns a Subject into an address; it is required,
// and see RecipientResolver for why the library cannot supply one.
func NewEmailNotifier(
	emailer email.Emailer,
	from Recipient,
	resolve RecipientResolver,
	opts ...EmailNotifierOption,
) (*EmailNotifier, error) {
	if emailer == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil emailer")
	}

	if resolve == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy recipient resolver")
	}

	if from.Address == "" {
		return nil, platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty dataprivacy notification sender address")
	}

	n := &EmailNotifier{
		emailer:  emailer,
		from:     from,
		resolve:  resolve,
		renderer: renderDefaultMessage,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(n)
		}
	}

	return n, nil
}

// Notify implements Notifier.
func (n *EmailNotifier) Notify(ctx context.Context, notification *Notification) error {
	if notification == nil || notification.Request == nil {
		return ErrNilRequest
	}

	to, err := n.resolve(ctx, notification.Request.Subject)
	if err != nil {
		return platformerrors.Wrap(err, "resolving dataprivacy notification recipient")
	}

	// A nil recipient is a decision, not a failure. An erased subject may no
	// longer have an address to write to, which is the system working.
	if to == nil || to.Address == "" {
		return nil
	}

	subject, htmlBody := n.renderer(notification, *to)

	return n.emailer.SendEmail(ctx, &email.OutboundEmailMessage{
		ToAddress:   to.Address,
		ToName:      to.Name,
		FromAddress: n.from.Address,
		FromName:    n.from.Name,
		Subject:     subject,
		HTMLContent: htmlBody,
	})
}

// renderDefaultMessage builds the fallback notification.
//
// The download URL is stated with its expiry beside it, because a link that has
// silently stopped working is the single most common complaint about this whole
// workflow. Nothing collected about the subject appears in the mail: the
// artifact is behind the link, and mail is not a confidential channel.
func renderDefaultMessage(notification *Notification, to Recipient) (subject, htmlBody string) {
	req := notification.Request

	// Everything interpolated below lands in an HTML mail body, so it is escaped
	// first. The display name in particular is subject-supplied in most
	// deployments — it is whatever they typed into a profile form — and pasting
	// it in raw makes every one of these notifications an injection sink.
	greeting := "Hello"
	if to.Name != "" {
		greeting = "Hello " + html.EscapeString(to.Name)
	}

	requestID := html.EscapeString(req.ID)
	requestType := html.EscapeString(string(req.Type))
	downloadURL := html.EscapeString(notification.DownloadURL)

	switch {
	case req.Status == StatusFailed:
		return "Your data request could not be completed",
			fmt.Sprintf(
				"<p>%s,</p><p>We were unable to complete your %s request (reference %s). "+
					"Our team has been notified and will follow up.</p>",
				greeting, requestType, requestID,
			)

	case req.Type == RequestErasure:
		return "Your data has been erased",
			fmt.Sprintf(
				"<p>%s,</p><p>Your erasure request (reference %s) has been completed. "+
					"Some records may have been retained where we are legally required to keep them.</p>",
				greeting, requestID,
			)

	case notification.DownloadURL == "":
		return "Your data export is ready",
			fmt.Sprintf(
				"<p>%s,</p><p>Your data export (reference %s) is ready. "+
					"Please sign in to download it.</p>",
				greeting, requestID,
			)

	default:
		return "Your data export is ready",
			fmt.Sprintf(
				"<p>%s,</p><p>Your data export (reference %s) is ready.</p>"+
					"<p><a href=%q>Download your data</a></p>"+
					"<p>This link expires at %s. The export itself is deleted at %s.</p>",
				greeting, requestID, downloadURL,
				notification.ExpiresAt.Format(time.RFC1123),
				req.ExpiresAt.Format(time.RFC1123),
			)
	}
}
