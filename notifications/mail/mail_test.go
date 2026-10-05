package mail

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/passwordreset"
	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/identity"
	"github.com/primandproper/platform-go/v15/links"
	"github.com/primandproper/platform-go/v15/outbox"
	"github.com/primandproper/platform-go/v15/outbox/migrations"
	"github.com/primandproper/platform-go/v15/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v15/waitlists/grpc"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/email"
	"github.com/primandproper/primitives-go/v2/retry"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const testTopic = "outbound-mail"

type testClientConfig struct{ connectionString string }

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 1 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// newTestClient builds a SQLite client with the outbox table created, so a
// QueuedMailer writes through the real outbox.Writer and the bytes a Drainer
// is handed are the bytes the relay would publish.
func newTestClient(t *testing.T) database.Client {
	t.Helper()

	ctx := t.Context()

	client, err := sqlite.NewDatabaseClient(ctx, &testClientConfig{connectionString: filepath.Join(t.TempDir(), "mail.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, outbox.DefaultTablePrefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(ctx, stmt)
		must.NoError(t, execErr)
	}

	return client
}

func newQueuedMailer(t *testing.T, client database.Client) *QueuedMailer {
	t.Helper()

	writer, err := outbox.NewWriter(dialect.SQLite)
	must.NoError(t, err)

	mailer, err := NewQueuedMailer(client, writer, testTopic)
	must.NoError(t, err)

	return mailer
}

// payloads reads back what was enqueued, in order.
func payloads(t *testing.T, client database.Client) [][]byte {
	t.Helper()

	rows, err := client.Reader().QueryContext(t.Context(), "SELECT topic, payload FROM outbox_messages ORDER BY created_at, id")
	must.NoError(t, err)
	t.Cleanup(func() { _ = rows.Close() })

	var out [][]byte
	for rows.Next() {
		var (
			topic   string
			payload []byte
		)
		must.NoError(t, rows.Scan(&topic, &payload))
		test.EqOp(t, testTopic, topic)
		out = append(out, payload)
	}
	must.NoError(t, rows.Err())

	return out
}

// recordingRenderer captures every mail it is handed and renders a fixed
// message addressed from it.
type recordingRenderer struct {
	err   error
	mails []*Mail

	mu   sync.Mutex
	nilM bool
}

func (r *recordingRenderer) Render(_ context.Context, m *Mail) (*email.OutboundEmailMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.mails = append(r.mails, m)

	if r.err != nil {
		return nil, r.err
	}

	if r.nilM {
		return nil, nil
	}

	return &email.OutboundEmailMessage{ToAddress: "someone@example.com", Subject: string(m.Kind)}, nil
}

type recordingEmailer struct {
	err  error
	sent []*email.OutboundEmailMessage
}

func (e *recordingEmailer) SendEmail(_ context.Context, details *email.OutboundEmailMessage) error {
	if e.err != nil {
		return e.err
	}

	e.sent = append(e.sent, details)

	return nil
}

// jsonOf renders a value's JSON, which is the comparison a value carrying a
// blank `_` field can be given.
func jsonOf(t *testing.T, v any) string {
	t.Helper()

	b, err := json.Marshal(v)
	must.NoError(t, err)

	return string(b)
}

func testUser() *identity.User {
	return &identity.User{ID: "user-1", Username: "handle", EmailAddress: "someone@example.com", Scope: tenancy.Of("acct-1")}
}

func testTime() time.Time {
	return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
}

func TestNewQueuedMailer(t *testing.T) {
	t.Parallel()

	client := newTestClient(t)
	writer, writerErr := outbox.NewWriter(dialect.SQLite)
	must.NoError(t, writerErr)

	t.Run("nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewQueuedMailer(nil, writer, testTopic)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	t.Run("nil enqueuer", func(t *testing.T) {
		t.Parallel()

		_, err := NewQueuedMailer(client, nil, testTopic)
		test.ErrorIs(t, err, ErrNilEnqueuer)
	})

	t.Run("empty topic", func(t *testing.T) {
		t.Parallel()

		_, err := NewQueuedMailer(client, writer, "")
		test.ErrorIs(t, err, outbox.ErrEmptyTopic)
	})
}

func TestNewDrainer(t *testing.T) {
	t.Parallel()

	t.Run("nil emailer", func(t *testing.T) {
		t.Parallel()

		_, err := NewDrainer(nil, &recordingRenderer{})
		test.ErrorIs(t, err, ErrNilEmailer)
	})

	t.Run("nil renderer", func(t *testing.T) {
		t.Parallel()

		_, err := NewDrainer(&recordingEmailer{}, nil)
		test.ErrorIs(t, err, ErrNilRenderer)
	})
}

// TestRoundTrip sends each of the six mails through the queued mailer, reads
// the stored payload back off the outbox table, and drains it: the renderer
// must be handed the value the synchronous seam was, secret included.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	user := testUser()
	expiresAt := testTime()

	invitation := &identity.InvitationMail{
		Invitation: &identity.Invitation{ID: "inv-1", ToEmail: "someone@example.com", ExpiresAt: expiresAt},
		Token:      "invitation-secret",
	}
	verification := &signin.VerificationMail{User: user, Token: "verification-secret", ExpiresAt: expiresAt}
	reminder := &signin.HandleReminderMail{User: user}
	magicLink := &signin.MagicLinkMail{
		User: user,
		Issuance: &signin.MagicLinkIssuance{
			Link:   &signin.MagicLink{SubjectID: "user-1", EmailAddress: "someone@example.com", ExpiresAt: expiresAt},
			Secret: "magic-secret",
		},
	}
	reset := &passwordreset.Mail{
		User: user,
		Issuance: &passwordreset.Issuance{
			Token:  &passwordreset.Token{ID: "reset-1", UserID: user.ID, Scope: user.Scope, ExpiresAt: expiresAt},
			Secret: "reset-secret",
		},
	}
	confirmation := &waitlistsgrpc.ConfirmationMail{
		Signup:      &waitlists.Signup{ID: "signup-1", ListID: "list-1", Contact: "someone@example.com"},
		Confirm:     &links.Link{URL: "https://example.com/c?t=confirm-secret", Token: "confirm-secret", ID: "c-1", Action: "confirm", Subject: "signup-1", ExpiresAt: expiresAt},
		Unsubscribe: &links.Link{URL: "https://example.com/u?t=unsub-secret", Token: "unsub-secret", ID: "u-1", Action: "unsubscribe", Subject: "signup-1", ExpiresAt: expiresAt},
	}
	waitlistScope := tenancy.Of("tenant-9")

	tests := []struct {
		send  func(ctx context.Context, q *QueuedMailer) error
		check func(t *testing.T, m *Mail)
		kind  Kind
	}{
		{
			kind: KindInvitation,
			send: func(ctx context.Context, q *QueuedMailer) error { return q.SendInvitation(ctx, invitation) },
			check: func(t *testing.T, m *Mail) {
				t.Helper()

				must.NotNil(t, m.Invitation)
				test.EqOp(t, invitation.Token, m.Invitation.Token)
				test.EqOp(t, jsonOf(t, invitation.Invitation), jsonOf(t, m.Invitation.Invitation))
			},
		},
		{
			kind: KindVerification,
			send: func(ctx context.Context, q *QueuedMailer) error { return q.SendVerification(ctx, verification) },
			check: func(t *testing.T, m *Mail) {
				t.Helper()

				must.NotNil(t, m.Verification)
				test.EqOp(t, verification.Token, m.Verification.Token)
				test.True(t, verification.ExpiresAt.Equal(m.Verification.ExpiresAt))
				test.EqOp(t, jsonOf(t, verification), jsonOf(t, m.Verification))
			},
		},
		{
			kind: KindHandleReminder,
			send: func(ctx context.Context, q *QueuedMailer) error { return q.SendHandleReminder(ctx, reminder) },
			check: func(t *testing.T, m *Mail) {
				t.Helper()

				must.NotNil(t, m.HandleReminder)
				test.EqOp(t, jsonOf(t, reminder), jsonOf(t, m.HandleReminder))
			},
		},
		{
			kind: KindMagicLink,
			send: func(ctx context.Context, q *QueuedMailer) error { return q.SendMagicLink(ctx, magicLink) },
			check: func(t *testing.T, m *Mail) {
				t.Helper()

				must.NotNil(t, m.MagicLink)
				test.EqOp(t, magicLink.Issuance.Secret, m.MagicLink.Issuance.Secret)
				test.EqOp(t, jsonOf(t, magicLink), jsonOf(t, m.MagicLink))
			},
		},
		{
			kind: KindPasswordReset,
			send: func(ctx context.Context, q *QueuedMailer) error { return q.SendPasswordReset(ctx, reset) },
			check: func(t *testing.T, m *Mail) {
				t.Helper()

				must.NotNil(t, m.PasswordReset)
				test.EqOp(t, reset.Issuance.Secret, m.PasswordReset.Issuance.Secret)
				test.EqOp(t, jsonOf(t, reset), jsonOf(t, m.PasswordReset))
			},
		},
		{
			kind: KindWaitlistConfirmation,
			send: func(ctx context.Context, q *QueuedMailer) error {
				return q.SendConfirmation(ctx, waitlistScope, confirmation)
			},
			check: func(t *testing.T, m *Mail) {
				t.Helper()

				must.NotNil(t, m.WaitlistConfirmation)
				test.EqOp(t, waitlistScope, m.WaitlistConfirmation.Scope)
				test.EqOp(t, confirmation.Confirm.Token, m.WaitlistConfirmation.Mail.Confirm.Token)
				test.EqOp(t, confirmation.Unsubscribe.URL, m.WaitlistConfirmation.Mail.Unsubscribe.URL)
				test.EqOp(t, jsonOf(t, confirmation), jsonOf(t, m.WaitlistConfirmation.Mail))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.kind.String(), func(t *testing.T) {
			t.Parallel()

			client := newTestClient(t)
			mailer := newQueuedMailer(t, client)

			must.NoError(t, tc.send(t.Context(), mailer))

			stored := payloads(t, client)
			must.SliceLen(t, 1, stored)

			renderer := &recordingRenderer{}
			emailer := &recordingEmailer{}

			drainer, err := NewDrainer(emailer, renderer)
			must.NoError(t, err)

			must.NoError(t, drainer.Handle(t.Context(), stored[0]))

			must.SliceLen(t, 1, renderer.mails)
			m := renderer.mails[0]
			test.EqOp(t, tc.kind, m.Kind)
			test.EqOp(t, "", m.TestID)
			tc.check(t, m)

			set := 0
			for _, part := range []bool{
				m.Invitation != nil, m.Verification != nil, m.HandleReminder != nil,
				m.MagicLink != nil, m.PasswordReset != nil, m.WaitlistConfirmation != nil,
			} {
				if part {
					set++
				}
			}
			test.EqOp(t, 1, set)

			must.SliceLen(t, 1, emailer.sent)
			test.EqOp(t, tc.kind.String(), emailer.sent[0].Subject)
		})
	}
}

func TestQueuedMailer_TestID(t *testing.T) {
	t.Parallel()

	client := newTestClient(t)
	mailer := newQueuedMailer(t, client)

	ctx := ContextWithTestID(t.Context(), "canary-42")
	must.NoError(t, mailer.SendHandleReminder(ctx, &signin.HandleReminderMail{User: testUser()}))

	stored := payloads(t, client)
	must.SliceLen(t, 1, stored)

	renderer := &recordingRenderer{}
	drainer, err := NewDrainer(&recordingEmailer{}, renderer)
	must.NoError(t, err)
	must.NoError(t, drainer.Handle(t.Context(), stored[0]))

	must.SliceLen(t, 1, renderer.mails)
	test.EqOp(t, "canary-42", renderer.mails[0].TestID)
}

func TestQueuedMailer_IncompleteMail(t *testing.T) {
	t.Parallel()

	client := newTestClient(t)
	mailer := newQueuedMailer(t, client)
	ctx := t.Context()

	test.ErrorIs(t, mailer.SendInvitation(ctx, nil), ErrNilMail)
	test.ErrorIs(t, mailer.SendInvitation(ctx, &identity.InvitationMail{Token: "x"}), ErrNilMail)
	test.ErrorIs(t, mailer.SendVerification(ctx, &signin.VerificationMail{Token: "x"}), ErrNilMail)
	test.ErrorIs(t, mailer.SendHandleReminder(ctx, &signin.HandleReminderMail{}), ErrNilMail)
	test.ErrorIs(t, mailer.SendMagicLink(ctx, &signin.MagicLinkMail{User: testUser()}), ErrNilMail)
	test.ErrorIs(t, mailer.SendPasswordReset(ctx, &passwordreset.Mail{User: testUser(), Issuance: &passwordreset.Issuance{}}), ErrNilMail)
	test.ErrorIs(t, mailer.SendConfirmation(ctx, tenancy.Global(), &waitlistsgrpc.ConfirmationMail{Signup: &waitlists.Signup{}}), ErrNilMail)

	test.SliceEmpty(t, payloads(t, client))
}

type failingEnqueuer struct{ err error }

func (f failingEnqueuer) Enqueue(context.Context, database.Tx, ...outbox.Message) error {
	return f.err
}

func TestQueuedMailer_EnqueueFailure(t *testing.T) {
	t.Parallel()

	client := newTestClient(t)
	cause := errors.New("outbox refused")

	mailer, err := NewQueuedMailer(client, failingEnqueuer{err: cause}, testTopic)
	must.NoError(t, err)

	err = mailer.SendHandleReminder(t.Context(), &signin.HandleReminderMail{User: testUser()})
	test.ErrorIs(t, err, cause)
	test.SliceEmpty(t, payloads(t, client))
}

// TestQueuedMailer_SecretOnlyUnderSecret pins that the credential travels in
// the one field the wire names for it, and that the drain keeps it out of
// what it returns.
func TestQueuedMailer_SecretOnlyUnderSecret(t *testing.T) {
	t.Parallel()

	client := newTestClient(t)
	mailer := newQueuedMailer(t, client)

	must.NoError(t, mailer.SendVerification(t.Context(), &signin.VerificationMail{
		User: testUser(), Token: "the-secret", ExpiresAt: testTime(),
	}))

	stored := payloads(t, client)
	must.SliceLen(t, 1, stored)

	var raw map[string]json.RawMessage
	must.NoError(t, json.Unmarshal(stored[0], &raw))
	test.EqOp(t, `"the-secret"`, string(raw["secret"]))
	test.EqOp(t, 1, strings.Count(string(stored[0]), "the-secret"))
}

func TestDrainer_Handle(t *testing.T) {
	t.Parallel()

	reminder := func(t *testing.T) []byte {
		t.Helper()

		b, err := json.Marshal(&message{Kind: KindHandleReminder, User: testUser()})
		must.NoError(t, err)

		return b
	}

	t.Run("unparseable payload is unretryable", func(t *testing.T) {
		t.Parallel()

		renderer := &recordingRenderer{}
		drainer, err := NewDrainer(&recordingEmailer{}, renderer)
		must.NoError(t, err)

		err = drainer.Handle(t.Context(), []byte(`{"kind": "signin.verification", "secret": `))
		test.ErrorIs(t, err, ErrUndecodableMail)
		test.ErrorIs(t, err, retry.ErrUnretryable)
		test.StrNotContains(t, err.Error(), "secret")
		test.SliceEmpty(t, renderer.mails)
	})

	t.Run("unknown kind is unretryable", func(t *testing.T) {
		t.Parallel()

		drainer, err := NewDrainer(&recordingEmailer{}, &recordingRenderer{})
		must.NoError(t, err)

		err = drainer.Handle(t.Context(), []byte(`{"kind": "billing.invoice"}`))
		test.ErrorIs(t, err, ErrUndecodableMail)
		test.ErrorIs(t, err, retry.ErrUnretryable)
	})

	t.Run("missing part is unretryable", func(t *testing.T) {
		t.Parallel()

		drainer, err := NewDrainer(&recordingEmailer{}, &recordingRenderer{})
		must.NoError(t, err)

		for _, kind := range []Kind{
			KindInvitation, KindVerification, KindHandleReminder,
			KindMagicLink, KindPasswordReset, KindWaitlistConfirmation,
		} {
			err = drainer.Handle(t.Context(), []byte(`{"kind": "`+kind.String()+`"}`))
			test.ErrorIs(t, err, ErrUndecodableMail, test.Sprint(kind))
			test.ErrorIs(t, err, retry.ErrUnretryable, test.Sprint(kind))
		}
	})

	t.Run("renderer error is retried", func(t *testing.T) {
		t.Parallel()

		cause := errors.New("template missing")
		emailer := &recordingEmailer{}
		drainer, err := NewDrainer(emailer, &recordingRenderer{err: cause})
		must.NoError(t, err)

		err = drainer.Handle(t.Context(), reminder(t))
		test.ErrorIs(t, err, cause)
		test.False(t, errors.Is(err, retry.ErrUnretryable))
		test.SliceEmpty(t, emailer.sent)
	})

	t.Run("nothing rendered is unretryable", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}
		drainer, err := NewDrainer(emailer, &recordingRenderer{nilM: true})
		must.NoError(t, err)

		err = drainer.Handle(t.Context(), reminder(t))
		test.ErrorIs(t, err, ErrNothingRendered)
		test.ErrorIs(t, err, retry.ErrUnretryable)
		test.SliceEmpty(t, emailer.sent)
	})

	t.Run("emailer error is retried", func(t *testing.T) {
		t.Parallel()

		cause := errors.New("provider down")
		drainer, err := NewDrainer(&recordingEmailer{err: cause}, &recordingRenderer{})
		must.NoError(t, err)

		err = drainer.Handle(t.Context(), reminder(t))
		test.ErrorIs(t, err, cause)
		test.False(t, errors.Is(err, retry.ErrUnretryable))
	})

	t.Run("renderer func adapts", func(t *testing.T) {
		t.Parallel()

		emailer := &recordingEmailer{}
		drainer, err := NewDrainer(emailer, RendererFunc(func(_ context.Context, m *Mail) (*email.OutboundEmailMessage, error) {
			return &email.OutboundEmailMessage{ToAddress: m.HandleReminder.User.EmailAddress, Subject: m.HandleReminder.User.Username}, nil
		}))
		must.NoError(t, err)

		must.NoError(t, drainer.Handle(t.Context(), reminder(t)))
		must.SliceLen(t, 1, emailer.sent)
		test.EqOp(t, "handle", emailer.sent[0].Subject)
		test.EqOp(t, "someone@example.com", emailer.sent[0].ToAddress)
	})
}

func TestTestIDFromContext(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "", TestIDFromContext(t.Context()))
	test.EqOp(t, "abc", TestIDFromContext(ContextWithTestID(t.Context(), "abc")))
}
