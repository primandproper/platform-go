package webhooks

// The resource types the audit entries about this package's own writes name.
// They are platform's vocabulary for platform's own tables, prefixed so a
// consumer's "endpoint" is never mistaken for one of these.
//
// They are declared here, beside the rows they name, and not beside the
// RecordingHooks that writes them, which lives in webhooks/recordinghooks: that
// type is built over a recording.Recorder, recording imports this package for
// its Emitter, and so the one type that needs recording cannot sit in the
// package recording needs. Everything a subscriber or an audit reader decodes —
// the names, the catalog fragment, the payloads — needs nothing from recording,
// and stays where a consumer of webhooks already looks.
const (
	// ResourceTypeEndpoint is what an audit entry about an Endpoint names.
	ResourceTypeEndpoint = "webhooks.endpoint"
	// ResourceTypeSubscription is what an audit entry about a Subscription names.
	ResourceTypeSubscription = "webhooks.subscription"
)

// The events this package's endpoint and subscription writes emit when a
// deployment installs recordinghooks.RecordingHooks, one per write. A
// subscriber may receive one only if the dispatcher's catalog knows it;
// [EventCatalog] is the fragment to merge into that catalog.
//
// An endpoint may subscribe to these like any other event type, including to
// the ones about itself, and the one a reader asks about is
// EventEndpointArchived: is an endpoint told about its own archival? It is
// not. The hook runs after the archive's write, on the same transaction, and
// the fan-out it causes reads its subscribers on that transaction too, so the
// endpoint is already archived when it is looked for and fan-out skips archived
// endpoints. Every other live endpoint in the scope subscribed to the event is
// told. The same reading makes an endpoint that registers itself subscribed to
// EventEndpointCreated hear about its own creation, because by the time the
// hook dispatches, the row and its subscriptions are both there.
//
// None of this loops. A delivery is a row on the dispatches table, written by
// Dispatch and drained by the Worker, and neither writes an endpoint or a
// subscription; only those writes have hooks, so an event about an endpoint
// fans out once and causes nothing further.
const (
	// EventEndpointCreated says an endpoint was registered.
	EventEndpointCreated EventType = "webhooks.endpoint.created"
	// EventEndpointUpdated says an endpoint was re-registered over an existing
	// one; the payload names which fields moved.
	EventEndpointUpdated EventType = "webhooks.endpoint.updated"
	// EventEndpointArchived says an endpoint was retired. The endpoint retired is
	// not among those told; see above.
	EventEndpointArchived EventType = "webhooks.endpoint.archived"
	// EventEndpointSecretRotated says an endpoint's signing key was replaced.
	// The payload names the endpoint and no key, old or new.
	EventEndpointSecretRotated EventType = "webhooks.endpoint.secret_rotated"
	// EventSubscriptionCreated says an endpoint subscribed to an event type. It
	// is emitted whether the subscription was new, an archived one revived, or
	// one already live, because to a subscriber the three mean the same thing.
	EventSubscriptionCreated EventType = "webhooks.subscription.created"
	// EventSubscriptionArchived says an endpoint stopped receiving an event type.
	EventSubscriptionArchived EventType = "webhooks.subscription.archived"
)

// EventCatalog is every event this package's writes emit, described, for a
// consumer to merge into the catalog its dispatchers are built with:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, webhooks.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() Catalog {
	return Catalog{
		EventEndpointCreated:       {Description: "A webhook endpoint was registered."},
		EventEndpointUpdated:       {Description: "A webhook endpoint was re-registered with new details."},
		EventEndpointArchived:      {Description: "A webhook endpoint was retired."},
		EventEndpointSecretRotated: {Description: "A webhook endpoint's signing key was replaced."},
		EventSubscriptionCreated:   {Description: "A webhook endpoint subscribed to an event type."},
		EventSubscriptionArchived:  {Description: "A webhook endpoint unsubscribed from an event type."},
	}
}

// EndpointEvent is the payload of every endpoint event.
//
// It names the endpoint by ID and URL and carries nothing else of the row. Not
// its Headers, which are as often a credential as a routing token and are
// tagged out of audit diffs for that reason, and never its Secret: an event is
// copied into every subscriber's logs, and an endpoint's keys are what a
// delivery is authenticated with.
type EndpointEvent struct {
	_ struct{} `json:"-"`

	// EndpointID is the endpoint the event is about.
	EndpointID string `json:"endpointID"`
	// URL is where the endpoint delivers to, as the write left it. It is empty
	// on EventEndpointSecretRotated, whose write reads no row back.
	URL string `json:"url,omitempty"`

	// Changed names the fields a re-registration moved, sorted, and is empty
	// for every other event. The timestamp every save stamps is left off, and so
	// are the fields an audit diff never sees.
	Changed []string `json:"changed,omitempty"`
}

// SubscriptionEvent is the payload of every subscription event.
type SubscriptionEvent struct {
	_ struct{} `json:"-"`

	// SubscriptionID is the subscription the event is about.
	SubscriptionID string `json:"subscriptionID"`
	// EndpointID is the endpoint that subscribed.
	EndpointID string `json:"endpointID"`
	// EventType is the event type subscribed to or unsubscribed from.
	EventType EventType `json:"eventType"`
}
