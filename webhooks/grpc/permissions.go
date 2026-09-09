package grpc

import (
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/authorization"
	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"
)

// The permissions this service's methods require, in authorization's
// vocabulary.
//
// They are declared here rather than in the authorization package because
// authorization is a primitive: it owns what a Permission *is* — a string, a
// set, a role that inherits — and owns no domain's names.
//
// The strings are dotted and namespaced so that a consumer composing several
// domains' fragments cannot have two of them collide on "read". They are values
// rather than an enum because a consumer's policy is data — a YAML file, a table
// of roles — and it has to be able to name one without importing Go.
//
// There are seven of them over nine RPCs, and the two collapses are deliberate.
// Each get and its list share one grant, because they answer the same question
// at two cardinalities and a grant that separated them would let a consumer
// allow enumeration while forbidding the read it enumerates into. A consumer who
// wants the page behind a stronger grant than the get overrides the map, which
// is what [Permissions] returning a fresh one is for.
const (
	// PermissionSaveEndpoints covers registering an endpoint and re-registering
	// one.
	//
	// It is the sharpest grant in this file, and it is sharper than "write a
	// row" looks. A save names the URL an authenticated request from inside the
	// deployment is about to be made to — which is why the URL is checked for
	// SSRF at registration and again at delivery — and it names the HMAC keys
	// those requests are signed under, so a holder can point an event stream
	// somewhere else and sign it in the deployment's name.
	PermissionSaveEndpoints authorization.Permission = "webhooks.endpoints.save"

	// PermissionReadEndpoints covers reading or paging the tenant's endpoints.
	//
	// It discloses no signing key, because no message on this surface carries
	// one. What it does disclose is where a tenant's events are being sent, which
	// is why it is a grant and not a method every caller reaches.
	PermissionReadEndpoints authorization.Permission = "webhooks.endpoints.read"

	// PermissionArchiveEndpoints covers retiring an endpoint. Its delivery
	// history outlives it, which is what makes this a lighter grant than a save:
	// nothing is destroyed and nothing is redirected.
	PermissionArchiveEndpoints authorization.Permission = "webhooks.endpoints.archive"

	// PermissionAddSubscriptions covers subscribing an existing endpoint to one
	// more event type.
	//
	// It is separate from PermissionSaveEndpoints, and the split is the reason
	// subscriptions are rows rather than a field of the endpoint: "let this team
	// pick which events their endpoint receives" and "let them change where the
	// events go" are different amounts of trust, and against a flat list they
	// would have to be the same one.
	PermissionAddSubscriptions authorization.Permission = "webhooks.subscriptions.add"

	// PermissionReadSubscriptions covers reading or paging an endpoint's
	// subscriptions.
	PermissionReadSubscriptions authorization.Permission = "webhooks.subscriptions.read"

	// PermissionArchiveSubscriptions covers retiring one subscription, so an
	// endpoint stops receiving one event type without its other subscriptions,
	// its delivery history, or its identity being touched.
	PermissionArchiveSubscriptions authorization.Permission = "webhooks.subscriptions.archive"

	// PermissionReadAttempts covers paging the delivery log of one of the
	// tenant's deliveries: what was tried, when, and what came back.
	//
	// It is its own grant rather than PermissionReadEndpoints, because the two
	// answer different questions to different people. Where a tenant's events go
	// is configuration; whether a particular delivery got through, and what the
	// subscriber said about it, is operational history that includes a
	// subscriber's own error text.
	PermissionReadAttempts authorization.Permission = "webhooks.attempts.read"
)

// Permissions is the default map from method name to what it requires: every
// RPC this service declares, and nothing else.
//
// Every method is in it. There is no second set of methods that require nothing
// — this service serves no RPC a caller reaches without a grant — so a method
// missing from this map is a bug rather than a decision, and the suite reads the
// service descriptor rather than a list in order to say so.
//
// The keys are the generated full method name constants rather than strings, so
// an RPC renamed in the .proto is a compile error here instead of a method that
// silently requires nothing.
//
// It returns a fresh map each call, so a consumer composing it into their own
// policy and then overriding an entry is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		webhookspb.WebhooksService_SaveEndpoint_FullMethodName:        {PermissionSaveEndpoints},
		webhookspb.WebhooksService_GetEndpoint_FullMethodName:         {PermissionReadEndpoints},
		webhookspb.WebhooksService_ListEndpoints_FullMethodName:       {PermissionReadEndpoints},
		webhookspb.WebhooksService_ArchiveEndpoint_FullMethodName:     {PermissionArchiveEndpoints},
		webhookspb.WebhooksService_AddSubscription_FullMethodName:     {PermissionAddSubscriptions},
		webhookspb.WebhooksService_GetSubscription_FullMethodName:     {PermissionReadSubscriptions},
		webhookspb.WebhooksService_ListSubscriptions_FullMethodName:   {PermissionReadSubscriptions},
		webhookspb.WebhooksService_ArchiveSubscription_FullMethodName: {PermissionArchiveSubscriptions},
		webhookspb.WebhooksService_ListAttempts_FullMethodName:        {PermissionReadAttempts},
	}
}

// Require declares every method of this service on a requirements builder.
//
// It is one line over Permissions today and is the exported name anyway, because
// authorization/grpc is fail-closed: a method declared nowhere is denied, and
// what a consumer needs is a call that stays correct when this service's method
// set changes.
//
// A nil builder is tolerated and returns nil, so composing several domains'
// fragments in a chain does not need a nil check per link.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	return b.RequireAll(Permissions())
}
