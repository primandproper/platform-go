package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"
)

// ListEventTypes answers what a subscription may name: every event type in this
// application's catalog, sorted, with the prose saying when each one fires.
//
// # Why this is on the surface at all
//
// The catalog is a webhooks.Catalog the application supplies at construction,
// and every other RPC here already judges against it — webhooks.Endpoint's
// validation rejects a subscription naming an event type the catalog does not
// know, and that refusal reaches a client through SaveEndpoint. A surface that
// refuses a write against a list and will not disclose the list leaves a client
// guessing at the legal values of the field it just rejected.
//
// It is also what webhooks.Catalog.EventTypes says it is for, in those words:
// "for rendering a subscription UI or an API response". Until this method there
// was no API response it could reach, and every consumer serving a subscription
// form wrote this RPC themselves over a value they had already handed this
// package.
//
// # It takes no scope, and it is the only method here that does not
//
// A catalog is not a table. It is the same value for every tenant of this
// binary, because it is the application's own declaration of what it publishes,
// so there is no scope to bind and nothing a caller in one scope could learn
// about another. Every other read here names req.scope; this one deliberately
// does not, and a future change that "fixes" the inconsistency by binding one
// would be narrowing a constant by a dimension it has no rows along.
//
// For the same reason there is no filter and no pagination. The catalog is
// sized by how many kinds of thing the application publishes, it cannot change
// between two requests to the same process, and a cursor into it would be a
// cursor into a compile-time constant.
func (s *Server) ListEventTypes(
	ctx context.Context,
	_ *webhookspb.ListEventTypesRequest,
) (*webhookspb.ListEventTypesResponse, error) {
	_, req, done, err := s.caller(ctx, webhookspb.WebhooksService_ListEventTypes_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	catalog := s.dispatcher.Catalog()

	results := make([]*webhookspb.EventTypeDefinition, 0, len(catalog))
	for _, eventType := range catalog.EventTypes() {
		results = append(results, &webhookspb.EventTypeDefinition{
			EventType:   string(eventType),
			Description: catalog[eventType].Description,
		})
	}

	req.op.Set(eventTypeCountKey, len(results))

	return &webhookspb.ListEventTypesResponse{Results: results}, nil
}
