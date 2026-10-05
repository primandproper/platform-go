package mail

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/outbox"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Enqueuer is the outbox seam a QueuedMailer writes through: the one method
// outbox.Writer exports for a caller inside a transaction. It is declared here
// for the reason webhooks.Enqueuer is — a seam belongs to the side that
// depends on it.
type Enqueuer interface {
	Enqueue(ctx context.Context, tx database.Tx, msgs ...outbox.Message) error
}

var _ Enqueuer = (*outbox.Writer)(nil)

// QueuedMailer enqueues each mail as an outbox message, and satisfies every
// Mailer seam in this module. See the package documentation for what a nil
// error from it promises and what it cannot.
type QueuedMailer struct {
	client   database.Client
	enqueuer Enqueuer
	o11y     observability.Observer

	enqueuedCounter metrics.Int64Counter

	topic string
}

var (
	_ identity.InvitationMailer        = (*QueuedMailer)(nil)
	_ signin.VerificationMailer        = (*QueuedMailer)(nil)
	_ signin.HandleReminderMailer      = (*QueuedMailer)(nil)
	_ signin.MagicLinkMailer           = (*QueuedMailer)(nil)
	_ passwordreset.Mailer             = (*QueuedMailer)(nil)
	_ waitlistsgrpc.ConfirmationMailer = (*QueuedMailer)(nil)
)

// NewQueuedMailer builds a QueuedMailer that enqueues on topic through
// enqueuer, in transactions it opens on client.
//
// It takes a client, where every other writer here takes its caller's Tx,
// because none of the six seams it implements is handed one: each is called
// after its service's transaction has committed, so that a mail is never sent
// for a row that rolled back. The transaction it opens holds the one enqueue
// and nothing else.
//
// topic is fixed here rather than per mail for the reason webhooks.NewEmitter's
// is, and it must be a topic the Drainer alone consumes: every message on it
// carries a live credential.
func NewQueuedMailer(client database.Client, enqueuer Enqueuer, topic string, opts ...Option) (*QueuedMailer, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if enqueuer == nil {
		return nil, ErrNilEnqueuer
	}

	if topic == "" {
		return nil, platformerrors.Wrap(outbox.ErrEmptyTopic, "building a queued mailer")
	}

	o := applyOptions(opts)

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	enqueued, err := mp.NewInt64Counter(serviceName + "_queued")
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating queued mail counter")
	}

	return &QueuedMailer{
		client:          client,
		enqueuer:        enqueuer,
		topic:           topic,
		o11y:            observability.NewObserver(serviceName, o.logger, o.tracerProvider),
		enqueuedCounter: enqueued,
	}, nil
}

// SendInvitation implements identity.InvitationMailer.
func (q *QueuedMailer) SendInvitation(ctx context.Context, mail *identity.InvitationMail) error {
	if mail == nil || mail.Invitation == nil {
		return ErrNilMail
	}

	return q.enqueue(ctx, &message{Kind: KindInvitation, Invitation: mail.Invitation, Secret: mail.Token})
}

// SendVerification implements signin.VerificationMailer.
func (q *QueuedMailer) SendVerification(ctx context.Context, mail *signin.VerificationMail) error {
	if mail == nil || mail.User == nil {
		return ErrNilMail
	}

	expiresAt := mail.ExpiresAt

	return q.enqueue(ctx, &message{Kind: KindVerification, User: mail.User, ExpiresAt: &expiresAt, Secret: mail.Token})
}

// SendHandleReminder implements signin.HandleReminderMailer.
func (q *QueuedMailer) SendHandleReminder(ctx context.Context, mail *signin.HandleReminderMail) error {
	if mail == nil || mail.User == nil {
		return ErrNilMail
	}

	return q.enqueue(ctx, &message{Kind: KindHandleReminder, User: mail.User})
}

// SendMagicLink implements signin.MagicLinkMailer.
func (q *QueuedMailer) SendMagicLink(ctx context.Context, mail *signin.MagicLinkMail) error {
	if mail == nil || mail.User == nil || mail.Issuance == nil || mail.Issuance.Link == nil {
		return ErrNilMail
	}

	return q.enqueue(ctx, &message{
		Kind:      KindMagicLink,
		User:      mail.User,
		MagicLink: mail.Issuance.Link,
		Secret:    mail.Issuance.Secret,
	})
}

// SendPasswordReset implements passwordreset.Mailer.
func (q *QueuedMailer) SendPasswordReset(ctx context.Context, mail *passwordreset.Mail) error {
	if mail == nil || mail.User == nil || mail.Issuance == nil || mail.Issuance.Token == nil {
		return ErrNilMail
	}

	return q.enqueue(ctx, &message{
		Kind:       KindPasswordReset,
		User:       mail.User,
		ResetToken: mail.Issuance.Token,
		Secret:     mail.Issuance.Secret,
	})
}

// SendConfirmation implements waitlistsgrpc.ConfirmationMailer.
func (q *QueuedMailer) SendConfirmation(ctx context.Context, scope tenancy.Scope, mail *waitlistsgrpc.ConfirmationMail) error {
	if mail == nil || mail.Signup == nil || mail.Confirm == nil || mail.Unsubscribe == nil {
		return ErrNilMail
	}

	return q.enqueue(ctx, &message{
		Kind:        KindWaitlistConfirmation,
		Scope:       &scope,
		Signup:      mail.Signup,
		Confirm:     mail.Confirm,
		Unsubscribe: mail.Unsubscribe,
	})
}

// enqueue writes one message in a transaction of its own. The payload is never
// logged or put on the span: it carries the secret.
func (q *QueuedMailer) enqueue(ctx context.Context, msg *message) error {
	msg.TestID = TestIDFromContext(ctx)

	ctx, op := q.o11y.Begin(ctx, observability.WithValue(kindKey, msg.Kind.String()))
	defer op.End()

	if msg.TestID != "" {
		op.Set(testIDKey, msg.TestID)
	}

	if err := q.client.WithTransaction(ctx, func(tx database.Tx) error {
		return q.enqueuer.Enqueue(ctx, tx, outbox.Message{Topic: q.topic, Payload: msg})
	}); err != nil {
		return op.Error(err, "enqueuing %s mail", msg.Kind)
	}

	q.enqueuedCounter.Add(ctx, 1, kindAttr(msg.Kind))

	return nil
}

func kindAttr(kind Kind) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(kindKey, kind.String()))
}
