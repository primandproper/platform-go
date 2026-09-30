package http

import (
	"context"
	nethttp "net/http"

	billingsync "github.com/primandproper/platform-go/v14/billing/sync"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// o11yName scopes this package's spans and logger.
const o11yName = "billing_webhook"

// Observability keys for this package.
const (
	eventIDKey   = o11yName + ".event_id"
	eventTypeKey = o11yName + ".event_type"
	scopeKey     = o11yName + ".scope"
	outcomeKey   = o11yName + ".outcome"
	statusKey    = o11yName + ".status_code"
)

// failedOutcome is the outcome recorded for a delivery answered 500, which
// billing/sync has no Outcome for: it never finished deciding one.
const failedOutcome = "failed"

type (
	// ScopeResolver says which tenant a verified delivery is for.
	//
	// It is handed the event and nothing else — not the request — because
	// everything on the request that is not the signed body is something anybody
	// could have written. The hand-written endpoint this package replaces took
	// the account from a query parameter and let it override the one in the
	// signed payload, which is a way for whoever can reach the URL to file a
	// provider's report against an account of their choosing. There is nowhere
	// for a resolver here to read one from.
	//
	// It answers the tenant and not the account. The account an agreement
	// belongs to is billing/sync's Place's to answer, from the provider's
	// customer on the signed state, and only on the delivery that opens the
	// agreement; after that it is on the row. What is left to the endpoint is
	// the scope every statement binds, which a processor's customer does not
	// carry. A deployment with one tenant passes GlobalScope.
	//
	// Returning an error is answered 500, so the provider redelivers. A
	// resolver's failure is either a transient one or a customer this
	// deployment does not know yet, and both are better retried than dropped:
	// the second is often a checkout whose own transaction has not committed.
	// A deployment that wants a delivery dropped acknowledges it — see
	// [WebhookHandler].
	ScopeResolver func(ctx context.Context, event *capitalism.Event) (tenancy.Scope, error)

	// AfterApply writes what belongs beside a reconciled delivery, on the
	// transaction the delivery was reconciled on.
	//
	// A processor callback almost never writes alone: the audit entry saying a
	// delivery moved an agreement and the outbox event an application publishes
	// off the back of it are one fact with the row. Returning an error rolls
	// all of it back and is answered 500, so the provider redelivers.
	//
	// It is called for every delivery billing/sync applied, including the ones
	// it acknowledged without writing — the result's Outcome says which.
	AfterApply func(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		event *capitalism.Event,
		result *billingsync.Result,
	) error

	// WebhookHandler is a payment provider's webhook endpoint: it verifies a
	// delivery, reconciles it into billing through billing/sync on a
	// transaction, and answers with the status code the provider acts on.
	WebhookHandler struct {
		manager    capitalism.PaymentManager
		syncer     *billingsync.Syncer
		client     database.Client
		resolver   ScopeResolver
		afterApply AfterApply
		o11y       observability.Observer
	}
)

var _ nethttp.Handler = (*WebhookHandler)(nil)

// GlobalScope is the ScopeResolver for a deployment with no tenants: every
// delivery is for tenancy.Global().
//
// It exists as a name rather than as the default so that "this deployment has no
// tenants" is something somebody wrote down.
func GlobalScope(context.Context, *capitalism.Event) (tenancy.Scope, error) {
	return tenancy.Global(), nil
}

// NewWebhookHandler builds the endpoint.
//
// The three positional dependencies are required: the manager verifies and
// parses a delivery, the syncer reconciles it, and the client opens the
// transaction it is reconciled on. WithScopeResolver is required too, and has
// no default; see ErrNilScopeResolver.
func NewWebhookHandler(
	manager capitalism.PaymentManager,
	syncer *billingsync.Syncer,
	client database.Client,
	opts ...Option,
) (*WebhookHandler, error) {
	if manager == nil {
		return nil, ErrNilPaymentManager
	}

	if syncer == nil {
		return nil, ErrNilSyncer
	}

	if client == nil {
		return nil, ErrNilClient
	}

	o := newOptions(opts)

	if o.resolver == nil {
		return nil, ErrNilScopeResolver
	}

	return &WebhookHandler{
		manager:    manager,
		syncer:     syncer,
		client:     client,
		resolver:   o.resolver,
		afterApply: o.afterApply,
		o11y:       observability.NewObserver(o11yName, o.logger, o.tracerProvider),
	}, nil
}

// ServeHTTP answers one delivery.
//
// The status code is the whole of the answer, because it is the only thing a
// provider reads, and there are three:
//
// 400, for a delivery that failed verification or could not be parsed. It is
// the one answer that tells the provider to stop, and it is given only where
// sending the same bytes again cannot succeed.
//
// 200, for a delivery reconciled, a redelivery billing/sync acknowledged as
// unchanged, and a verified delivery with nothing to reconcile — an event that
// is not about a subscription, or a manager reporting there is nothing to act
// on. A provider retrying any of those would be told the same thing again.
//
// 500, for everything else: the scope resolver failing, the transaction failing
// to open or commit, billing/sync refusing, and an AfterApply refusing. The
// provider redelivers, and every one of those either succeeds on a later try or
// is worth being loud about until somebody fixes it. The endpoint this replaces
// answered 400 to all of it, which tells the provider that a database blip is a
// bad delivery and to drop it.
//
// Nothing is written in the body. What went wrong is on the span and in the log.
func (h *WebhookHandler) ServeHTTP(res nethttp.ResponseWriter, req *nethttp.Request) {
	ctx, op := h.o11y.Begin(req.Context())
	defer op.End()

	status, err := h.serve(ctx, op, req)
	if err != nil {
		op.Set(outcomeKey, failedOutcome)
	}

	op.Set(statusKey, status)
	res.WriteHeader(status)
}

// serve answers a delivery with its status code, and with the error behind a
// 500, already recorded on the span and the log.
func (h *WebhookHandler) serve(ctx context.Context, op observability.Operation, req *nethttp.Request) (int, error) {
	event, err := h.manager.HandleEventWebhook(req.WithContext(ctx))
	if err != nil {
		op.Acknowledge(err, "refusing a billing webhook delivery")

		return nethttp.StatusBadRequest, nil
	}

	if event == nil {
		return nethttp.StatusOK, nil
	}

	if event.ID != "" {
		op.Set(eventIDKey, event.ID)
	}

	if event.Type != "" {
		op.Set(eventTypeKey, event.Type)
	}

	// Resolved only for the deliveries there is something to reconcile. An event
	// that is not about a subscription is acknowledged by the sync regardless of
	// scope, and a resolver keyed on the subscription's customer asked about a
	// payment intent would fail it into a redelivery loop over nothing.
	if event.Subscription == nil {
		op.Set(outcomeKey, string(billingsync.OutcomeIgnored))

		return nethttp.StatusOK, nil
	}

	scope, err := h.resolver(ctx, event)
	if err != nil {
		return nethttp.StatusInternalServerError, op.Error(err, "resolving the scope of a billing webhook delivery")
	}

	op.Set(scopeKey, scope.String())

	err = h.client.WithTransaction(ctx, func(tx database.Tx) error {
		result, applyErr := h.syncer.Apply(ctx, tx, scope, event)
		if applyErr != nil {
			return applyErr
		}

		op.Set(outcomeKey, string(result.Outcome))

		if h.afterApply == nil {
			return nil
		}

		return h.afterApply(ctx, tx, scope, event, result)
	})
	if err != nil {
		return nethttp.StatusInternalServerError, op.Error(err, "reconciling a billing webhook delivery")
	}

	return nethttp.StatusOK, nil
}
