package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/postgres"
	"github.com/primandproper/platform-go/v13/database/postgres/pgnotify"
	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"
	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The wake path is unit-tested against a bare channel, which is the whole
// argument for taking one. What only a real server can show is the other half:
// that a pg_notify emitted inside the caller's transaction reaches a listener,
// and that the relay it wakes publishes in milliseconds rather than in poll
// intervals.

const (
	// pollFloor is the poll interval both relays below run on. It is long
	// enough that a publication inside the deadline cannot have come from a
	// tick, which is what makes this a measurement rather than a race.
	pollFloor = 30 * time.Second

	// wakeDeadline is what "fast" means here. Two orders of magnitude below
	// pollFloor, and still generous for a loopback socket.
	wakeDeadline = 3 * time.Second
)

// liveRelay builds a relay on the real clock — the stub every other test uses
// would freeze the very intervals this one measures — against one recording
// publisher.
func liveRelay(t *testing.T, client database.Client, table string, opts ...RelayOption) (*Relay, *recordingPublisher) {
	t.Helper()

	rec := &recordingPublisher{}

	provider := &messagequeuemock.PublisherProviderMock{
		NewPublisherFunc: func(context.Context, string) (messagequeue.Publisher, error) {
			return &messagequeuemock.PublisherMock{
				PublishFunc: rec.Publish,
				StopFunc:    func() {},
			}, nil
		},
		CloseFunc: func() {},
	}

	cfg := &RelayConfig{
		TablePrefix:  table,
		ClaimMode:    ClaimSkipLocked,
		PollInterval: pollFloor,
		ReapInterval: pollFloor,
	}

	relay, err := NewRelay(t.Context(), cfg, client, provider, opts...)
	must.NoError(t, err)

	return relay, rec
}

// publishedWithin reports how long the relay took to publish, or false if it
// did not within the deadline.
func publishedWithin(rec *recordingPublisher, d time.Duration) (time.Duration, bool) {
	startTime := time.Now()

	for time.Since(startTime) < d {
		if len(rec.payloads()) > 0 {
			return time.Since(startTime), true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return 0, false
}

func TestOutbox_NotifyWakeup_Containers(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(_ context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(T.Context(), &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		env := &dialectEnv{client: client, dialect: dialect.Postgres, claimMode: ClaimSkipLocked}

		// The measurement, and the control it is measured against, in one
		// subtest so both run on the same server under the same load.
		T.Run("a wakeup publishes in milliseconds where a poll would take the interval", func(t *testing.T) {
			t.Parallel()

			const channel = "outbox_wakeup"

			table := env.newTable(t)

			listener, listenErr := pgnotify.NewListener(t.Context(), &pgnotify.Config{
				ConnectionString: pg.ConnectionString,
				Channel:          channel,
			})
			must.NoError(t, listenErr)

			go listener.Run()

			t.Cleanup(func() {
				must.NoError(t, listener.Close(context.WithoutCancel(t.Context())))
			})

			// The catch-up wake every session opens with. Draining it here is
			// what makes the wake measured below the one the enqueue caused.
			select {
			case <-listener.Signal():
			case <-time.After(wakeDeadline):
				t.Fatal("the listener never established a session")
			}

			woken, wokenRec := liveRelay(t, client, table, WithRelayWakeup(listener.Signal()))

			go woken.Run()

			t.Cleanup(func() {
				must.NoError(t, woken.Close(context.WithoutCancel(t.Context())))
			})

			w, writerErr := NewWriter(dialect.Postgres,
				WithWriterTablePrefix(table),
				WithWriterNotifyChannel(channel),
			)
			must.NoError(t, writerErr)

			must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
				return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "woken"}})
			}))

			latency, published := publishedWithin(wokenRec, wakeDeadline)
			must.True(t, published, must.Sprint("the woken relay did not publish inside the deadline"))

			t.Logf("wake-driven publish latency: %s (poll interval is %s)", latency, pollFloor)

			test.Less(t, pollFloor, latency)
		})

		// The control. Same table shape, same poll interval, no wakeup and no
		// notify channel — this is the latency the wakeup removes, and it has
		// to still be there or the measurement above proves nothing.
		T.Run("without a wakeup the same enqueue waits for the poll", func(t *testing.T) {
			t.Parallel()

			table := env.newTable(t)

			polled, polledRec := liveRelay(t, client, table)

			go polled.Run()

			t.Cleanup(func() {
				must.NoError(t, polled.Close(context.WithoutCancel(t.Context())))
			})

			w, writerErr := NewWriter(dialect.Postgres, WithWriterTablePrefix(table))
			must.NoError(t, writerErr)

			must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
				return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "polled"}})
			}))

			_, published := publishedWithin(polledRec, wakeDeadline)
			test.False(t, published, test.Sprint("a relay with no wakeup published before its poll interval"))

			// And it is a relay that works, not one that is broken: the same
			// cycle its ticker would eventually run publishes the message.
			polled.cycle(t.Context())

			test.SliceLen(t, 1, polledRec.payloads())
		})
	})
}
