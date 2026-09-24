package webhooks

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// deliveryURL is where every endpoint here points: an address in the block RFC
// 5737 reserves for documentation. The package documentation says why.
const deliveryURL = "https://192.0.2.1/conformance/hook"

// cleanupTimeout bounds the archive each registered endpoint gets when its test
// ends. The test's own context is already canceled by then.
const cleanupTimeout = 10 * time.Second

// catalog reads the event types the deployment offers a subscription form,
// skipping where it offers fewer than atLeast.
func catalog(t *testing.T, caller *conformance.Subject, atLeast int) []string {
	t.Helper()

	offered, err := caller.Surfaces.Webhooks.ListEventTypes(caller.Context(t.Context()),
		&webhookspb.ListEventTypesRequest{})
	must.NoError(t, err, must.Sprint("reading the event catalog"))

	names := make([]string, 0, len(offered.GetResults()))
	for _, def := range offered.GetResults() {
		names = append(names, def.GetEventType())
	}

	if len(names) < atLeast {
		t.Skipf("conformance: this deployment's event catalog offers %d event types and the assertion needs %d",
			len(names), atLeast)
	}

	return names
}

// keyring is a signing keyring nobody else holds, so a response carrying any of
// it could only have got it from this test.
func keyring() *webhookspb.WebhookSigningKeys {
	return &webhookspb.WebhookSigningKeys{
		Current:  []byte("conformance-current-" + identifiers.New()),
		Previous: []byte("conformance-previous-" + identifiers.New()),
	}
}

// endpointFor is an endpoint subscribing to eventTypes, named for this test.
func endpointFor(eventTypes ...string) *webhookspb.WebhookEndpointInput {
	return &webhookspb.WebhookEndpointInput{
		Name:       "conf_" + identifiers.New(),
		Url:        deliveryURL,
		EventTypes: eventTypes,
	}
}

// save sends a registration as caller and answers with whatever came back.
func save(
	t *testing.T,
	caller *conformance.Subject,
	input *webhookspb.WebhookEndpointInput,
	keys *webhookspb.WebhookSigningKeys,
) (*webhookspb.WebhookEndpoint, error) {
	t.Helper()

	saved, err := caller.Surfaces.Webhooks.SaveEndpoint(caller.Context(t.Context()),
		&webhookspb.SaveEndpointRequest{Endpoint: input, SigningKeys: keys})
	if err != nil {
		return nil, err
	}

	return saved.GetResult(), nil
}

// register saves an endpoint as caller, failing the test if it is refused, and
// archives it when the test ends so a deployment is not left delivering to it.
//
// A refusal as InvalidArgument skips instead. That is what a deployment whose
// URL check is an allowlist of its own hosts answers for the documentation
// address, and it is the deployment being right rather than the surface being
// wrong — every other input here is one the surface accepts.
func register(
	t *testing.T,
	caller *conformance.Subject,
	input *webhookspb.WebhookEndpointInput,
	keys *webhookspb.WebhookSigningKeys,
) *webhookspb.WebhookEndpoint {
	t.Helper()

	saved, err := save(t, caller, input, keys)
	if status.Code(err) == codes.InvalidArgument {
		t.Skipf("conformance: this deployment refused an endpoint at %s (%v); "+
			"the assertion needs an endpoint it accepts", deliveryURL, err)
	}

	must.NoError(t, err, must.Sprint("registering an endpoint"))
	must.NotNil(t, saved, must.Sprint("an endpoint was stored and none came back"))

	id := saved.GetId()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), cleanupTimeout)
		defer cancel()

		if _, archiveErr := caller.Surfaces.Webhooks.ArchiveEndpoint(caller.Context(ctx),
			&webhookspb.ArchiveEndpointRequest{EndpointId: id}); archiveErr != nil {
			t.Logf("conformance: retiring endpoint %q after the test: %v", id, archiveErr)
		}
	})

	return saved
}

// registered is register with a fresh keyring, for the assertions that are not
// about the keys.
func registered(t *testing.T, caller *conformance.Subject, eventTypes ...string) *webhookspb.WebhookEndpoint {
	t.Helper()

	return register(t, caller, endpointFor(eventTypes...), keyring())
}

// endpoint reads one endpoint as caller, failing the test if it is not there.
func endpoint(t *testing.T, caller *conformance.Subject, id string) *webhookspb.WebhookEndpoint {
	t.Helper()

	found, err := caller.Surfaces.Webhooks.GetEndpoint(caller.Context(t.Context()),
		&webhookspb.GetEndpointRequest{EndpointId: id})
	must.NoError(t, err, must.Sprintf("reading endpoint %q", id))
	must.NotNil(t, found.GetResult())

	return found.GetResult()
}

// listedEndpoints is the caller's endpoints listing, as identifiers.
func listedEndpoints(t *testing.T, caller *conformance.Subject) []string {
	t.Helper()

	page, err := caller.Surfaces.Webhooks.ListEndpoints(caller.Context(t.Context()), &webhookspb.ListEndpointsRequest{})
	must.NoError(t, err, must.Sprint("listing endpoints"))
	test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

	ids := make([]string, 0, len(page.GetResults()))
	for _, e := range page.GetResults() {
		ids = append(ids, e.GetId())
	}

	return ids
}

// subscription reads one subscription as caller, failing the test if it is not
// there.
func subscription(t *testing.T, caller *conformance.Subject, id string) *webhookspb.WebhookSubscription {
	t.Helper()

	found, err := caller.Surfaces.Webhooks.GetSubscription(caller.Context(t.Context()),
		&webhookspb.GetSubscriptionRequest{SubscriptionId: id})
	must.NoError(t, err, must.Sprintf("reading subscription %q", id))
	must.NotNil(t, found.GetResult())

	return found.GetResult()
}

// listedSubscriptions is one endpoint's live subscriptions as caller reads them,
// as identifiers.
func listedSubscriptions(t *testing.T, caller *conformance.Subject, endpointID string) []string {
	t.Helper()

	page, err := caller.Surfaces.Webhooks.ListSubscriptions(caller.Context(t.Context()),
		&webhookspb.ListSubscriptionsRequest{EndpointId: endpointID})
	must.NoError(t, err, must.Sprintf("listing the subscriptions of %q", endpointID))

	return subscriptionIDs(page.GetResults())
}

// subscribedTo finds the subscription an endpoint holds for eventType.
func subscribedTo(t *testing.T, e *webhookspb.WebhookEndpoint, eventType string) *webhookspb.WebhookSubscription {
	t.Helper()

	for _, sub := range e.GetSubscriptions() {
		if sub.GetEventType() == eventType {
			return sub
		}
	}

	t.Fatalf("conformance: endpoint %q holds no subscription to %q", e.GetId(), eventType)

	return nil
}

func subscriptionIDs(subs []*webhookspb.WebhookSubscription) []string {
	out := make([]string, 0, len(subs))
	for _, sub := range subs {
		out = append(out, sub.GetId())
	}

	return out
}

func subscribedEventTypes(e *webhookspb.WebhookEndpoint) []string {
	out := make([]string, 0, len(e.GetSubscriptions()))
	for _, sub := range e.GetSubscriptions() {
		out = append(out, sub.GetEventType())
	}

	return out
}

// carries reports whether a message's encoded form contains needle anywhere.
//
// The encoding rather than the fields, because it is the broadest reading of
// "not readable through the surface" there is: asserting on the fields a
// converter happens to set would pass a message that grew a new one, and this
// looks at what actually crossed the wire.
func carries(t *testing.T, message proto.Message, needle []byte) bool {
	t.Helper()

	must.SliceNotEmpty(t, needle)

	encoded, err := proto.Marshal(message)
	must.NoError(t, err)

	return bytes.Contains(encoded, needle)
}

// refused asserts err is a refusal a client reads as code.
func refused(t *testing.T, err error, code codes.Code, what string) {
	t.Helper()

	must.Error(t, err, must.Sprintf("%s was not refused", what))
	test.EqOp(t, code, status.Code(err), test.Sprintf("%s was refused with the wrong code", what))
}

// needsUser skips unless the subject surfaced the caller's user identifier.
func needsUser(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.UserID == "" {
		t.Skip("conformance: this subject does not surface the caller's user identifier")
	}
}

// twoTenants mints two callers and refuses to proceed if the subject put them in
// one tenant, since every confinement assertion here would then compare a
// tenant with itself and pass.
func twoTenants(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.Subject(t), s.Subject(t)

	must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(),
		must.Sprint("the subject minted two callers in one tenant; the confinement this asserts cannot be observed"))

	return mine, theirs
}

// rolledKey is a signing key nobody else holds, for a rotation.
func rolledKey() []byte { return []byte("conformance-rolled-" + identifiers.New()) }

// absentID is an identifier that names nothing, anywhere.
func absentID() string { return "conf_absent_" + identifiers.New() }
