package noop_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/messagequeue/noop"
)

func ExampleNewPublisher() {
	pub := noop.NewPublisher()
	defer pub.Stop()

	err := pub.Publish(context.Background(), map[string]string{"event": "user.created"})
	fmt.Println(err)
	// Output: <nil>
}

func ExampleNewPublisherProvider() {
	provider := noop.NewPublisherProvider()
	defer provider.Close()

	pub, err := provider.ProvidePublisher(context.Background(), "user-events")
	if err != nil {
		panic(err)
	}

	fmt.Println(pub != nil)
	// Output: true
}
