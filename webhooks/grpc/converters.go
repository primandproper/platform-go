package grpc

import (
	"github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/tenancy"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EndpointToProto renders an endpoint for the wire.
//
// It carries no signing keys and no scope, and neither is an omission.
// webhooks.Store.GetEndpoint reads an endpoint "secrets included" and this
// function is where they stop: [webhookspb.WebhookEndpoint] has no field to put
// them in, so a key cannot leak through this surface by somebody adding an
// assignment. A subscriber authenticates a delivery by its HMAC, so a key
// readable back over an administrative API is a key anyone who can read that API
// can forge deliveries with. The scope is the caller's own, read off their
// principal, so a response naming it would be telling a client something it
// supplied.
//
// CreatedBy is rendered by its owner identifier rather than by String, which is
// prose: a scope naming nobody would render as "<unset>" and a client would show
// it to somebody. Empty is the honest answer for an endpoint registered by a
// caller the deployment tracks no person behind.
//
// It is exported because a consumer composing webhooks into a larger response —
// an admin console assembling a settings page — otherwise writes the same eleven
// assignments and gets one of them wrong.
func EndpointToProto(e *webhooks.Endpoint) *webhookspb.WebhookEndpoint {
	if e == nil {
		return nil
	}

	out := &webhookspb.WebhookEndpoint{
		CreatedAt:     timestamppb.New(e.CreatedAt),
		Id:            e.ID,
		Name:          e.Name,
		Url:           e.URL,
		ContentType:   e.ContentType,
		Headers:       e.Headers,
		Disabled:      e.Disabled,
		Subscriptions: endpointSubscriptionsToProto(e.Subscriptions),
		CreatedBy:     e.CreatedBy.Owner(),
	}

	// The two nullable times stay unset rather than becoming the zero
	// timestamp: a client rendering "last updated" wants to know there was no
	// update, and 1970 is not that answer.
	if e.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*e.LastUpdatedAt)
	}

	if e.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*e.ArchivedAt)
	}

	return out
}

// EndpointsToProto renders a page of endpoints.
//
// The three plural converters here all take the pointer slice
// filtering.QueryFilteredResult hands back, so a handler passes page.Data
// straight in. The one place the module holds a value slice instead is an
// endpoint's own Subscriptions, and endpointSubscriptionsToProto is the
// unexported half that reads it — one shape on the exported surface rather than
// two overloads a caller has to pick between.
func EndpointsToProto(endpoints []*webhooks.Endpoint) []*webhookspb.WebhookEndpoint {
	out := make([]*webhookspb.WebhookEndpoint, 0, len(endpoints))
	for _, e := range endpoints {
		out = append(out, EndpointToProto(e))
	}

	return out
}

// SubscriptionToProto renders one subscription.
//
// The event type goes out as the string it is. See webhooks.proto for why it
// will not become a generated enum: the catalog is the consumer's, and an enum
// would put their vocabulary on this module's release cadence.
func SubscriptionToProto(s *webhooks.Subscription) *webhookspb.WebhookSubscription {
	if s == nil {
		return nil
	}

	out := &webhookspb.WebhookSubscription{
		CreatedAt:  timestamppb.New(s.CreatedAt),
		Id:         s.ID,
		EndpointId: s.EndpointID,
		EventType:  s.EventType.String(),
	}

	if s.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*s.LastUpdatedAt)
	}

	if s.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*s.ArchivedAt)
	}

	return out
}

// SubscriptionsToProto renders a page of subscriptions.
func SubscriptionsToProto(subscriptions []*webhooks.Subscription) []*webhookspb.WebhookSubscription {
	out := make([]*webhookspb.WebhookSubscription, 0, len(subscriptions))
	for _, s := range subscriptions {
		out = append(out, SubscriptionToProto(s))
	}

	return out
}

// endpointSubscriptionsToProto renders the subscriptions an endpoint carries,
// which is the module's one value slice of them.
func endpointSubscriptionsToProto(subscriptions []webhooks.Subscription) []*webhookspb.WebhookSubscription {
	out := make([]*webhookspb.WebhookSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, SubscriptionToProto(&subscriptions[i]))
	}

	return out
}

// AttemptToProto renders one line of the delivery log.
func AttemptToProto(a *webhooks.Attempt) *webhookspb.WebhookAttempt {
	if a == nil {
		return nil
	}

	return &webhookspb.WebhookAttempt{
		CreatedAt:    timestamppb.New(a.CreatedAt),
		Duration:     durationpb.New(a.Duration),
		Id:           a.ID,
		DeliveryId:   a.DeliveryID,
		EndpointId:   a.EndpointID,
		Error:        a.Error,
		StatusCode:   int32(a.StatusCode),
		AttemptCount: int32(a.AttemptCount),
	}
}

// AttemptsToProto renders a page of the delivery log.
func AttemptsToProto(attempts []*webhooks.Attempt) []*webhookspb.WebhookAttempt {
	out := make([]*webhookspb.WebhookAttempt, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, AttemptToProto(a))
	}

	return out
}

// endpointFromProto reads a registration request into the endpoint the store
// saves.
//
// A nil message is nil rather than an empty endpoint, so a request that named no
// endpoint is refused as malformed instead of being registered as one with no
// URL — which validation would refuse anyway, with a message about the URL
// rather than about the request.
//
// Three fields on webhooks.Endpoint are deliberately not read from the request
// and are the caller's or the store's to fill: Scope, which comes off the
// principal; CreatedBy, which comes off the principal too, because provenance a
// client could name is provenance that says whatever the client wanted; and the
// timestamps, which are the database's.
//
// The event types become the subscription set the way a registration always
// states one, through webhooks.SubscribeTo. Duplicates are not filtered here for
// the reason that function does not filter them: the save reconciles by event
// type so a repeated one is a single row either way, and dropping them silently
// would hide a caller whose list was built by something with a bug in it.
func endpointFromProto(in *webhookspb.WebhookEndpointInput, keys *webhookspb.WebhookSigningKeys, createdBy string) *webhooks.Endpoint {
	if in == nil {
		return nil
	}

	events := make([]webhooks.EventType, 0, len(in.GetEventTypes()))
	for _, event := range in.GetEventTypes() {
		events = append(events, webhooks.EventType(event))
	}

	return &webhooks.Endpoint{
		Headers:       in.GetHeaders(),
		CreatedBy:     tenancy.Of(createdBy),
		ID:            in.GetId(),
		Name:          in.GetName(),
		URL:           in.GetUrl(),
		ContentType:   in.GetContentType(),
		Secret:        keyringFromProto(keys),
		Subscriptions: webhooks.SubscribeTo(events...),
		Disabled:      in.GetDisabled(),
	}
}

// keyringFromProto reads the signing keys off a save request.
//
// It is the only direction this conversion has, and there is deliberately no
// inverse: nothing on this surface renders a keyring, which is what keeps
// [EndpointToProto] from being one assignment away from disclosing one.
//
// The bytes are copied rather than aliased. The request message is the
// unmarshaled wire buffer and its lifetime is the RPC's, whereas the keyring
// travels into a store write and, in a consumer's own hook, wherever they take
// it — so borrowing the message's backing array would make what gets signed with
// depend on when the runtime got round to reusing it.
func keyringFromProto(keys *webhookspb.WebhookSigningKeys) webhooks.Secret {
	if keys == nil {
		return webhooks.Secret{}
	}

	return webhooks.Secret{
		Current:  bytesCopy(keys.GetCurrent()),
		Previous: bytesCopy(keys.GetPrevious()),
	}
}

// bytesCopy copies a key, mapping an empty one to nil so that "no previous key"
// is one value rather than two.
func bytesCopy(in []byte) []byte {
	if len(in) == 0 {
		return nil
	}

	out := make([]byte, len(in))
	copy(out, in)

	return out
}
