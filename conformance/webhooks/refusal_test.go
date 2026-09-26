package webhooks_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	conformancewebhooks "github.com/primandproper/platform-go/v14/conformance/webhooks"
	domain "github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The child run is the suite against allowlistSubject, in a process of its own
// so that its failure is something the parent reads rather than suffers.
const (
	childEnv      = "CONFORMANCE_WEBHOOKS_REFUSAL_CHILD"
	webhookURLEnv = "CONFORMANCE_WEBHOOKS_REFUSAL_URL"
)

// allowedURL is the one address allowlistSubject's deployment accepts.
const allowedURL = "https://hooks.conformance.test/accept-and-discard"

// childRun narrows the child to the assertion that registers an endpoint and
// reads it back — one that reaches register, and nothing the fake does not
// implement.
const childRun = "^TestSuite_RefusalChild$/^webhooks$/^endpoints$/" +
	"^an_endpoint_is_answered_as_it_was_stored_and_reads_back_the_same$"

// TestSuite_RefusedRegistrationFails is the reason register no longer skips: a
// deployment refusing the address the suite registers at is indistinguishable,
// by code, from one refusing every endpoint, and a skip let the second pass.
func TestSuite_RefusedRegistrationFails(T *testing.T) {
	T.Parallel()

	out, err := child(T, "")

	must.Error(T, err, must.Sprintf("the suite passed against a deployment refusing every endpoint:\n%s", out))
	test.StrContains(T, out, "--- FAIL")
	test.StrContains(T, out, "Seams.WebhookURL", test.Sprint("the failure does not name the seam that fixes it"))
	test.StrNotContains(T, out, "--- SKIP", test.Sprint("the refusal was skipped"))
}

// TestSuite_NamedWebhookURLRuns is the consumer the skip was protecting: an
// allowlist that refuses the documentation address still runs the suite, by
// naming an address it accepts.
func TestSuite_NamedWebhookURLRuns(T *testing.T) {
	T.Parallel()

	out, err := child(T, allowedURL)

	must.NoError(T, err, must.Sprintf("the suite failed against an address the deployment accepts:\n%s", out))
	test.StrContains(T, out, "--- PASS: TestSuite_RefusalChild/webhooks/endpoints/an_endpoint")
	test.StrNotContains(T, out, "--- SKIP")
}

// child runs TestSuite_RefusalChild in a process of its own, with webhookURL as
// the subject's Seams.WebhookURL.
func child(t *testing.T, webhookURL string) (string, error) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run="+childRun, "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), childEnv+"=1", webhookURLEnv+"="+webhookURL)

	out, err := cmd.CombinedOutput()

	return string(out), err
}

// TestSuite_RefusalChild is the suite against a deployment whose URL check is
// an allowlist of one host. It runs only when a parent above starts it.
func TestSuite_RefusalChild(T *testing.T) {
	T.Parallel()

	if os.Getenv(childEnv) == "" {
		T.Skip("run by TestSuite_RefusedRegistrationFails and TestSuite_NamedWebhookURLRuns")
	}

	client := &allowlistClient{allowed: allowedURL, endpoints: map[string]*webhookspb.WebhookEndpoint{}}

	conformance.Run(T, conformance.Seams{
		NewSubject: func(context.Context, ...conformance.SubjectOption) (*conformance.Subject, error) {
			return &conformance.Subject{
				Scope:    tenancy.Of(identifiers.New()),
				Surfaces: conformance.Surfaces{Webhooks: client},
			}, nil
		},
		WebhookURL: os.Getenv(webhookURLEnv),
	}, conformancewebhooks.Suite())
}

// allowlistClient is a webhooks surface whose URL check accepts one address,
// implementing only what registering an endpoint and reading it back reach.
// Anything else panics on the nil embedded client, which the narrowed child run
// never calls.
type allowlistClient struct {
	webhookspb.WebhooksServiceClient

	endpoints map[string]*webhookspb.WebhookEndpoint
	allowed   string
	mu        sync.Mutex
}

func (c *allowlistClient) ListEventTypes(
	context.Context,
	*webhookspb.ListEventTypesRequest,
	...grpc.CallOption,
) (*webhookspb.ListEventTypesResponse, error) {
	return &webhookspb.ListEventTypesResponse{Results: []*webhookspb.EventTypeDefinition{
		{EventType: "conformance.one"},
		{EventType: "conformance.two"},
	}}, nil
}

func (c *allowlistClient) SaveEndpoint(
	_ context.Context,
	req *webhookspb.SaveEndpointRequest,
	_ ...grpc.CallOption,
) (*webhookspb.SaveEndpointResponse, error) {
	in := req.GetEndpoint()
	if !strings.EqualFold(in.GetUrl(), c.allowed) {
		return nil, status.Errorf(codes.InvalidArgument, "endpoint url %q is not on this deployment's allowlist", in.GetUrl())
	}

	saved := &webhookspb.WebhookEndpoint{
		Id:          identifiers.New(),
		Name:        in.GetName(),
		Url:         in.GetUrl(),
		ContentType: domain.DefaultContentType,
		CreatedAt:   timestamppb.Now(),
	}
	for _, eventType := range in.GetEventTypes() {
		saved.Subscriptions = append(saved.Subscriptions, &webhookspb.WebhookSubscription{
			Id:         identifiers.New(),
			EndpointId: saved.GetId(),
			EventType:  eventType,
			CreatedAt:  saved.GetCreatedAt(),
		})
	}

	c.mu.Lock()
	c.endpoints[saved.GetId()] = saved
	c.mu.Unlock()

	return &webhookspb.SaveEndpointResponse{Result: saved}, nil
}

func (c *allowlistClient) GetEndpoint(
	_ context.Context,
	req *webhookspb.GetEndpointRequest,
	_ ...grpc.CallOption,
) (*webhookspb.GetEndpointResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	found, ok := c.endpoints[req.GetEndpointId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such endpoint")
	}

	return &webhookspb.GetEndpointResponse{Result: found}, nil
}

func (c *allowlistClient) ArchiveEndpoint(
	_ context.Context,
	req *webhookspb.ArchiveEndpointRequest,
	_ ...grpc.CallOption,
) (*webhookspb.ArchiveEndpointResponse, error) {
	c.mu.Lock()
	delete(c.endpoints, req.GetEndpointId())
	c.mu.Unlock()

	return &webhookspb.ArchiveEndpointResponse{}, nil
}
