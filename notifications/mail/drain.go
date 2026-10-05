package mail

import (
	"context"
	"encoding/json"

	"github.com/primandproper/primitives-go/v2/email"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/retry"
)

// Drainer takes queued mail off the topic, renders it through the consumer's
// Renderer and sends it through an email.Emailer. Its Handle method is the
// jobs.Handler a jobs.Pool on that topic runs.
type Drainer struct {
	emailer  email.Emailer
	renderer Renderer
	o11y     observability.Observer

	sentCounter metrics.Int64Counter
}

var _ jobs.Handler = (*Drainer)(nil).Handle

// NewDrainer builds a Drainer over an emailer and the consumer's renderer.
// Both are required.
func NewDrainer(emailer email.Emailer, renderer Renderer, opts ...Option) (*Drainer, error) {
	if emailer == nil {
		return nil, ErrNilEmailer
	}

	if renderer == nil {
		return nil, ErrNilRenderer
	}

	o := applyOptions(opts)

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	sent, err := mp.NewInt64Counter(serviceName + "_sent")
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating sent mail counter")
	}

	return &Drainer{
		emailer:     emailer,
		renderer:    renderer,
		o11y:        observability.NewObserver(serviceName, o.logger, o.tracerProvider),
		sentCounter: sent,
	}, nil
}

// Handle decodes one message, renders it and sends it.
//
// A message that does not decode is refused with retry.Unretryable, so the
// pool dead-letters it at once rather than spending its attempts on bytes
// that will never parse. A renderer's or an emailer's error is returned as it
// stands and retried, since a provider's outage is the case queuing exists
// for; a renderer that knows its failure is permanent says so by wrapping it.
//
// An error never carries the payload, which holds the secret, and neither does
// the span.
func (d *Drainer) Handle(ctx context.Context, payload []byte) error {
	ctx, op := d.o11y.Begin(ctx)
	defer op.End()

	var msg message
	if err := json.Unmarshal(payload, &msg); err != nil {
		// The parse error is dropped rather than wrapped: encoding/json quotes
		// the offending input in some of its errors, and that input may be the
		// secret.
		return op.Error(retry.Unretryable(ErrUndecodableMail), "decoding queued mail")
	}

	op.Set(kindKey, msg.Kind.String())

	if msg.TestID != "" {
		op.Set(testIDKey, msg.TestID)
	}

	m, err := msg.decode()
	if err != nil {
		return op.Error(retry.Unretryable(err), "decoding queued mail")
	}

	rendered, err := d.renderer.Render(ctx, m)
	if err != nil {
		return op.Error(err, "rendering %s mail", m.Kind)
	}

	if rendered == nil {
		return op.Error(retry.Unretryable(ErrNothingRendered), "rendering %s mail", m.Kind)
	}

	if err = d.emailer.SendEmail(ctx, rendered); err != nil {
		return op.Error(err, "sending %s mail", m.Kind)
	}

	d.sentCounter.Add(ctx, 1, kindAttr(m.Kind))

	return nil
}
