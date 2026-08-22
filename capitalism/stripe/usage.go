package stripe

import (
	"context"
	"maps"
	"strconv"
	"time"

	"github.com/primandproper/platform-go/v13/capitalism"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/client"
)

const (
	usageImplementationName = "stripe_usage_reporter"

	// customerPayloadKey and valuePayloadKey are the meter event payload keys
	// Stripe reads to decide who is billed and how much.
	//
	// They are the defaults for a meter's customer_mapping.event_payload_key and
	// value_settings.event_payload_key. A meter created with either overridden
	// will not aggregate events posted by this adapter, which is a configuration
	// mismatch rather than something the adapter can detect: the override lives
	// on the meter, and reading it back would cost an API call per post.
	customerPayloadKey = "stripe_customer_id"
	valuePayloadKey    = "value"
)

var (
	_ capitalism.UsageReporter = (*UsageReporter)(nil)

	// ErrEmptyCustomerID indicates a report with no customer to bill. Stripe's
	// meter events key on a customer rather than on a subscription item, so an
	// empty one is usage that belongs to nobody.
	ErrEmptyCustomerID = platformerrors.New("empty stripe customer ID")

	// ErrEmptyMeterName indicates a report with no meter to count against.
	//
	// The name is what ties an event to a billing meter, and this adapter can
	// only check that one was supplied: whether it names a meter that exists is
	// Stripe's to decide, and the answer lives in a dashboard this code cannot
	// see.
	ErrEmptyMeterName = platformerrors.New("empty stripe meter name")

	// ErrReservedPayloadKey indicates metadata that would overwrite a reserved
	// meter event payload key.
	//
	// Stripe's meter event payload is one flat map, and two of its keys decide
	// who is billed and how much. Annotation that landed on either would move a
	// charge to a different customer or change its size, so a collision is
	// refused rather than resolved in the caller's favor or ours.
	ErrReservedPayloadKey = platformerrors.New("usage metadata uses a reserved stripe meter event payload key")

	// ErrEmptyUsageIdempotencyKey indicates a report with no idempotency key.
	//
	// It is refused rather than defaulted. A usage post with no key is a post
	// that double-bills on retry, and the retry is not optional — it is what a
	// flush loop does when the network blinks. Generating a key here would make
	// every attempt distinct, which is precisely the wrong answer.
	ErrEmptyUsageIdempotencyKey = platformerrors.New("empty stripe usage idempotency key")
)

var _ capitalism.UsageReporter = (*UsageReporter)(nil)

// UsageReporter posts meter events to Stripe's usage-based billing API. It is
// exported, and returned by NewUsageReporter, so a caller who has chosen Stripe
// can depend on that choice rather than on the capitalism.UsageReporter seam.
type UsageReporter struct {
	o11y        observability.Observer
	client      *client.API
	instruments *instruments
}

// NewUsageReporter builds a Stripe-backed UsageReporter.
//
// Unlike NewPaymentManager, the API key is required: there is no inbound path
// here, so a reporter without one could do nothing at all and would fail on its
// first flush rather than at startup.
func NewUsageReporter(cfg *Config, opts ...Option) (*UsageReporter, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if cfg.APIKey == "" {
		return nil, ErrAPIKeyNotConfigured
	}

	o := newOptions(opts)

	instruments, err := newInstruments(o.metricsProvider)
	if err != nil {
		return nil, err
	}

	sc := &client.API{}
	sc.Init(cfg.APIKey, nil)

	return &UsageReporter{
		client:      sc,
		o11y:        observability.NewObserver(usageImplementationName, o.logger, o.tracerProvider),
		instruments: instruments,
	}, nil
}

// ReportUsage posts one meter event, adding the reported quantity to the
// customer's total for the meter in the current billing period.
//
// Meter events are Stripe's replacement for the usage-records endpoint this
// adapter used to post to, and they are additive by construction: there is no
// "set" action to overwrite a period's total, only events that Stripe aggregates.
// That removes the trap the old API had — a "set" for one period arriving at two
// different timestamps left the smaller total standing beside the larger rather
// than replacing it — and leaves the same discipline in place: post the delta
// since the last flush, under a key derived from the flush's sequence number.
func (s *UsageReporter) ReportUsage(ctx context.Context, input *capitalism.UsageReportInput) (err error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	startedAt := time.Now()
	defer func() { s.instruments.record(ctx, opReportUsage, startedAt, err) }()

	if input == nil {
		return op.Error(platformerrors.ErrNilInputParameter, "reporting usage")
	}

	if input.CustomerID == "" {
		return op.Error(ErrEmptyCustomerID, "reporting usage")
	}

	if input.MeterName == "" {
		return op.Error(ErrEmptyMeterName, "reporting usage")
	}

	if input.IdempotencyKey == "" {
		return op.Error(ErrEmptyUsageIdempotencyKey, "reporting usage")
	}

	payload, err := meterEventPayload(input)
	if err != nil {
		return op.Error(err, "reporting usage")
	}

	op.Set("stripe.customer_id", input.CustomerID).
		Set("stripe.meter_event_name", input.MeterName).
		Set("stripe.usage_quantity", input.Quantity)

	params := &stripe.BillingMeterEventParams{
		EventName: new(input.MeterName),
		Payload:   payload,
		// Stripe deduplicates meter events by identifier over a rolling window of
		// at least 24 hours, which outlives the idempotency key header's own
		// replay window and covers the case the header cannot: a retry the flush
		// loop reassembles rather than repeats byte for byte.
		Identifier: new(input.IdempotencyKey),
	}

	if !input.OccurredAt.IsZero() {
		// Left unset for a zero time, so Stripe stamps it. A locally computed
		// "now" would be refused on a worker whose clock runs more than five
		// minutes fast, and Stripe's own clock is the one the window is measured
		// against.
		//
		// An explicit time still has to land inside that window: Stripe refuses an
		// event dated more than 35 days in the past or more than five minutes
		// ahead. A flush that has been failing for longer than that has lost the
		// usage regardless of what this adapter does with it.
		params.Timestamp = new(input.OccurredAt.Unix())
	}

	applyRequestParams(&params.Params, ctx, input.IdempotencyKey)

	event, err := s.client.BillingMeterEvents.New(params)
	if err != nil {
		return op.Error(err, "reporting usage")
	}

	op.Set("stripe.meter_event_identifier", event.Identifier)

	return nil
}

// meterEventPayload renders the input as a Stripe meter event payload.
//
// The payload is where a meter event carries everything Stripe reads, including
// the customer and the quantity, so it is also where caller metadata has to go if
// it is to go anywhere. Metadata that collides with one of the reserved keys is
// an error rather than an overwrite in either direction — silently dropping it
// loses an annotation the caller thought it stored, and silently honoring it
// bills somebody else.
func meterEventPayload(input *capitalism.UsageReportInput) (map[string]string, error) {
	var reserved []string
	for _, key := range []string{customerPayloadKey, valuePayloadKey} {
		if _, ok := input.Metadata[key]; ok {
			reserved = append(reserved, key)
		}
	}

	if len(reserved) > 0 {
		return nil, platformerrors.Wrapf(ErrReservedPayloadKey, "keys %v", reserved)
	}

	payload := make(map[string]string, len(input.Metadata)+2)
	maps.Copy(payload, input.Metadata)

	payload[customerPayloadKey] = input.CustomerID
	payload[valuePayloadKey] = strconv.FormatInt(input.Quantity, 10)

	return payload, nil
}
