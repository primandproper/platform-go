package dataprivacy

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/email"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingEmailer captures what was sent instead of sending it.
type recordingEmailer struct {
	err  error
	sent []*email.OutboundEmailMessage
	mu   sync.Mutex
}

var _ email.Emailer = (*recordingEmailer)(nil)

func (e *recordingEmailer) SendEmail(_ context.Context, message *email.OutboundEmailMessage) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.err != nil {
		return e.err
	}

	e.sent = append(e.sent, message)

	return nil
}

func (e *recordingEmailer) last() *email.OutboundEmailMessage {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.sent) == 0 {
		return nil
	}

	return e.sent[len(e.sent)-1]
}

// resolveTo builds a resolver returning a fixed recipient.
func resolveTo(address, name string) RecipientResolver {
	return func(context.Context, Subject) (*Recipient, error) {
		return &Recipient{Address: address, Name: name}, nil
	}
}

// testSender is the from address this suite mails as.
var testSender = Recipient{Address: "privacy@example.com", Name: "Example Privacy"}

func TestNewEmailNotifier(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil emailer", func(t *testing.T) {
		t.Parallel()

		_, err := NewEmailNotifier(nil, testSender, resolveTo("a@example.com", ""))
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a nil resolver", func(t *testing.T) {
		t.Parallel()

		// The mapping from an opaque subject ID to an address is the one piece
		// only the application has, so there is no default to fall back to.
		_, err := NewEmailNotifier(&recordingEmailer{}, testSender, nil)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses an empty sender address", func(t *testing.T) {
		t.Parallel()

		_, err := NewEmailNotifier(&recordingEmailer{}, Recipient{}, resolveTo("a@example.com", ""))
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})
}

func TestEmailNotifier_Notify(T *testing.T) {
	T.Parallel()

	T.Run("mails a completed export with its link", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}

		notifier, err := NewEmailNotifier(emailer, testSender, resolveTo("subject@example.com", "Ada"))
		must.NoError(t, err)

		expiresAt := baseTime.Add(15 * time.Minute)

		must.NoError(t, notifier.Notify(t.Context(), &Notification{
			Request: &Request{
				ID:        "req-1",
				Type:      RequestExport,
				Status:    StatusCompleted,
				ExpiresAt: baseTime.Add(DefaultArtifactTTL),
			},
			DownloadURL: "https://storage.example/exports/req-1.json?sig=abc",
			ExpiresAt:   expiresAt,
		}))

		sent := emailer.last()
		must.NotNil(t, sent)

		test.EqOp(t, "subject@example.com", sent.ToAddress)
		test.EqOp(t, "Ada", sent.ToName)
		test.EqOp(t, testSender.Address, sent.FromAddress)
		test.StrContains(t, sent.Subject, "export is ready")
		test.StrContains(t, sent.HTMLContent, "Hello Ada")
		test.StrContains(t, sent.HTMLContent, "https://storage.example/exports/req-1.json?sig=abc")

		// Both expiries are stated. A link that has silently stopped working is
		// the most common complaint about this whole workflow.
		test.StrContains(t, sent.HTMLContent, expiresAt.Format(time.RFC1123))
	})

	T.Run("a completed export without a link says to sign in", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}

		notifier, err := NewEmailNotifier(emailer, testSender, resolveTo("subject@example.com", ""))
		must.NoError(t, err)

		must.NoError(t, notifier.Notify(t.Context(), &Notification{
			Request: &Request{ID: "req-1", Type: RequestExport, Status: StatusCompleted},
		}))

		sent := emailer.last()
		must.NotNil(t, sent)

		test.StrContains(t, sent.HTMLContent, "sign in")
		test.StrNotContains(t, sent.HTMLContent, "<a href")

		// No name resolved, so the greeting stays generic rather than reading
		// "Hello ,".
		test.StrContains(t, sent.HTMLContent, "<p>Hello,</p>")
	})

	T.Run("mails a completed erasure", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}

		notifier, err := NewEmailNotifier(emailer, testSender, resolveTo("subject@example.com", ""))
		must.NoError(t, err)

		must.NoError(t, notifier.Notify(t.Context(), &Notification{
			Request: &Request{ID: "req-1", Type: RequestErasure, Status: StatusCompleted},
		}))

		sent := emailer.last()
		must.NotNil(t, sent)

		test.StrContains(t, sent.Subject, "erased")

		// Retention is mentioned, because some records always are.
		test.StrContains(t, sent.HTMLContent, "retained")
	})

	T.Run("mails a failure", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}

		notifier, err := NewEmailNotifier(emailer, testSender, resolveTo("subject@example.com", ""))
		must.NoError(t, err)

		must.NoError(t, notifier.Notify(t.Context(), &Notification{
			Request: &Request{ID: "req-1", Type: RequestExport, Status: StatusFailed},
		}))

		sent := emailer.last()
		must.NotNil(t, sent)

		test.StrContains(t, sent.Subject, "could not be completed")
	})

	T.Run("a nil recipient sends nothing and is not an error", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}

		notifier, err := NewEmailNotifier(emailer, testSender,
			func(context.Context, Subject) (*Recipient, error) { return nil, nil })
		must.NoError(t, err)

		// An erased subject may no longer have an address to write to, which is
		// the system working rather than failing.
		must.NoError(t, notifier.Notify(t.Context(), &Notification{
			Request: &Request{ID: "req-1", Type: RequestErasure, Status: StatusCompleted},
		}))

		test.Nil(t, emailer.last())
	})

	T.Run("an empty address sends nothing", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}

		notifier, err := NewEmailNotifier(emailer, testSender, resolveTo("", "Ada"))
		must.NoError(t, err)

		must.NoError(t, notifier.Notify(t.Context(), &Notification{
			Request: &Request{ID: "req-1", Type: RequestExport, Status: StatusCompleted},
		}))

		test.Nil(t, emailer.last())
	})

	T.Run("propagates a resolver error", func(t *testing.T) {
		t.Parallel()

		notifier, err := NewEmailNotifier(&recordingEmailer{}, testSender,
			func(context.Context, Subject) (*Recipient, error) {
				return nil, platformerrors.New("directory is down")
			})
		must.NoError(t, err)

		err = notifier.Notify(t.Context(), &Notification{
			Request: &Request{ID: "req-1", Type: RequestExport, Status: StatusCompleted},
		})

		must.Error(t, err)
		test.StrContains(t, err.Error(), "directory is down")
	})

	T.Run("propagates a send error", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{err: platformerrors.New("smtp refused")}

		notifier, err := NewEmailNotifier(emailer, testSender, resolveTo("subject@example.com", ""))
		must.NoError(t, err)

		err = notifier.Notify(t.Context(), &Notification{
			Request: &Request{ID: "req-1", Type: RequestExport, Status: StatusCompleted},
		})

		must.Error(t, err)
		test.StrContains(t, err.Error(), "smtp refused")
	})

	T.Run("refuses a notification with no request", func(t *testing.T) {
		t.Parallel()

		notifier, err := NewEmailNotifier(&recordingEmailer{}, testSender, resolveTo("a@example.com", ""))
		must.NoError(t, err)

		test.ErrorIs(t, notifier.Notify(t.Context(), nil), ErrNilRequest)
		test.ErrorIs(t, notifier.Notify(t.Context(), &Notification{}), ErrNilRequest)
	})

	T.Run("WithMessageRenderer replaces the message", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}

		notifier, err := NewEmailNotifier(emailer, testSender, resolveTo("subject@example.com", ""),
			WithMessageRenderer(func(n *Notification, to Recipient) (string, string) {
				return "Custom subject for " + to.Address, "<p>" + n.Request.ID + "</p>"
			}))
		must.NoError(t, err)

		must.NoError(t, notifier.Notify(t.Context(), &Notification{
			Request: &Request{ID: "req-42", Type: RequestExport, Status: StatusCompleted},
		}))

		sent := emailer.last()
		must.NotNil(t, sent)

		test.EqOp(t, "Custom subject for subject@example.com", sent.Subject)
		test.EqOp(t, "<p>req-42</p>", sent.HTMLContent)

		// A nil renderer leaves the existing one in place.
		WithMessageRenderer(nil)(notifier)
		must.NotNil(t, notifier.renderer)
	})

	T.Run("carries no collected data into the mail", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}

		notifier, err := NewEmailNotifier(emailer, testSender, resolveTo("subject@example.com", ""))
		must.NoError(t, err)

		must.NoError(t, notifier.Notify(t.Context(), &Notification{
			Request: &Request{
				ID:      "req-1",
				Type:    RequestExport,
				Status:  StatusCompleted,
				Subject: Subject{ID: "user-secret-identifier"},
			},
			DownloadURL: "https://storage.example/x.json",
		}))

		sent := emailer.last()
		must.NotNil(t, sent)

		// The artifact is behind the link; mail is not a confidential channel,
		// so nothing about the subject travels in the body.
		test.StrNotContains(t, sent.HTMLContent, "user-secret-identifier")
	})
}

func Test_renderDefaultMessage_escapesInterpolatedValues(T *testing.T) {
	T.Parallel()

	// The recipient's display name is whatever they typed into a profile form in
	// most deployments, and it lands in an HTML mail body.
	T.Run("escapes the recipient name", func(t *testing.T) {
		t.Parallel()

		_, body := renderDefaultMessage(
			&Notification{Request: &Request{ID: "req_1", Type: RequestErasure, Status: StatusCompleted}},
			Recipient{Name: `<script>alert(1)</script>`},
		)

		test.StrNotContains(t, body, "<script>")
		test.StrContains(t, body, "&lt;script&gt;")
	})

	T.Run("escapes the request ID", func(t *testing.T) {
		t.Parallel()

		_, body := renderDefaultMessage(
			&Notification{Request: &Request{ID: `"><img src=x onerror=alert(1)>`, Type: RequestErasure, Status: StatusCompleted}},
			Recipient{Name: "Spot"},
		)

		test.StrNotContains(t, body, "<img")
	})
}
