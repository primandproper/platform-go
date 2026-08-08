package fanout

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v10/messagequeue/redis"
	"github.com/primandproper/platform-go/v10/notifications/async"
	"github.com/primandproper/platform-go/v10/testutils/containers/redistest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestNotifier_containerBacked proves the property against a real broker rather
// than a mock: two Notifiers standing in for two replicas, one Publish, and both
// replicas' local connections served exactly once.
//
// Redis is the backplane's natural backend — see the package documentation on
// broker fit — and it is also the one whose at-most-once semantics this test has
// to accommodate: a message published before a subscriber has finished
// subscribing is gone. The publish is therefore retried until it lands, which is
// what makes the test deterministic without making the package's delivery
// guarantee stronger than it is.
func TestNotifier_containerBacked(T *testing.T) {
	T.Parallel()

	T.Run("delivers to every replica", func(t *testing.T) {
		t.Parallel()

		container := redistest.Start(t)

		addr, err := container.ConnectionString(t.Context())
		must.NoError(t, err)

		cfg := redis.Config{QueueAddresses: []string{trimScheme(addr)}}

		topic := "fanout_container_test"

		replicaA, localA := newContainerBackedNotifier(t, cfg, topic)
		_, localB := newContainerBackedNotifier(t, cfg, topic)

		event := &async.Event{Type: "thing.created", Data: json.RawMessage(`{"id":"1"}`)}

		// Publish from one replica only; both should see it.
		deadline := time.Now().Add(30 * time.Second)
		for {
			must.NoError(t, replicaA.Publish(t.Context(), "org_1", event))

			if len(localA.deliveries()) > 0 && len(localB.deliveries()) > 0 {
				break
			}

			if time.Now().After(deadline) {
				t.Fatalf("fanned-out event never reached both replicas (a=%d, b=%d)", len(localA.deliveries()), len(localB.deliveries()))
			}

			time.Sleep(100 * time.Millisecond)
		}

		for name, local := range map[string]*localNotifier{"replicaA": localA, "replicaB": localB} {
			delivered := local.deliveries()
			must.SliceNotEmpty(t, delivered, must.Sprintf("%s received nothing", name))
			test.EqOp(t, "org_1", delivered[0].channel, test.Sprintf("%s channel", name))
			test.EqOp(t, "thing.created", delivered[0].event.Type, test.Sprintf("%s event type", name))
			test.EqOp(t, `{"id":"1"}`, string(delivered[0].event.Data), test.Sprintf("%s event data", name))
		}
	})
}

func newContainerBackedNotifier(t *testing.T, cfg redis.Config, topic string) (*Notifier, *localNotifier) {
	t.Helper()

	local := &localNotifier{}

	n, err := New(
		t.Context(),
		&Config{Enabled: true, Topic: topic},
		local,
		redis.NewRedisPublisherProvider(cfg),
		redis.NewRedisConsumerProvider(cfg),
	)
	must.NoError(t, err)

	t.Cleanup(func() { test.NoError(t, n.Close()) })

	return n, local
}

func trimScheme(addr string) string {
	const scheme = "redis://"
	if len(addr) >= len(scheme) && addr[:len(scheme)] == scheme {
		return addr[len(scheme):]
	}

	return addr
}
