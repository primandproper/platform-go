package inbound_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/webhooks/inbound"
)

// printingPublisher stands in for a real broker so the example has something to publish to.
type printingPublisher struct{}

func (printingPublisher) Stop() {}

func (printingPublisher) PublishAsync(ctx context.Context, data any, _ ...messagequeue.PublishOption) {
	_ = ctx
	_ = data
}

func (printingPublisher) Publish(_ context.Context, data any, _ ...messagequeue.PublishOption) error {
	delivery, ok := data.(*inbound.Delivery)
	if !ok {
		return fmt.Errorf("unexpected message %T", data)
	}

	fmt.Printf("published a %s delivery of %d bytes\n", delivery.Provider, len(delivery.Body))

	return nil
}

var _ messagequeue.Publisher = printingPublisher{}

// Receiving GitHub webhooks: verify the signature, publish the delivery, ack. The work happens
// on the other end of the topic, so the ack is not waiting on it.
func Example() {
	verifier, err := inbound.NewGitHubVerifier("It's a Secret to Everybody")
	if err != nil {
		panic(err)
	}

	receiver, err := inbound.NewReceiver(verifier, printingPublisher{})
	if err != nil {
		panic(err)
	}

	// In a real service: receiver.Mount(router, "/webhooks/github")
	_ = receiver

	fmt.Println("mounted a receiver for", verifier.Provider())

	// Output:
	// mounted a receiver for github
}

// The consumer's half. It decodes the payload, keys deduplication on the provider's own event
// ID, and does the work — none of which the receiver knows anything about.
func ExampleDelivery() {
	delivery := &inbound.Delivery{
		Provider: "github",
		Body:     []byte(`{"action":"opened"}`),
	}

	var payload struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(delivery.Body, &payload); err != nil {
		panic(err)
	}

	// The delivery ID is in the headers, which the signature does not cover — fine as a
	// deduplication key, not as an authorization decision.
	fmt.Printf("%s: %s\n", delivery.Provider, payload.Action)

	// Output:
	// github: opened
}
