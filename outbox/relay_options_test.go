package outbox

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	databasemock "github.com/primandproper/platform-go/v13/database/mock"
	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestRelayOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithRelayLogger sets the logger", func(t *testing.T) {
		t.Parallel()

		r := &Relay{}
		WithRelayLogger(loggingnoop.NewLogger())(r)

		test.NotNil(t, r.logger)
	})

	T.Run("WithRelayTracerProvider sets the tracer provider", func(t *testing.T) {
		t.Parallel()

		r := &Relay{}
		WithRelayTracerProvider(tracingnoop.NewTracerProvider())(r)

		test.NotNil(t, r.tracerProvider)
	})

	T.Run("WithRelayMetricsProvider sets the metrics provider", func(t *testing.T) {
		t.Parallel()

		r := &Relay{}
		WithRelayMetricsProvider(metricsnoop.NewMetricsProvider())(r)

		test.NotNil(t, r.metricsProvider)
	})

	T.Run("NewRelay applies every option and skips nil ones", func(t *testing.T) {
		t.Parallel()

		opts := []RelayOption{
			nil,
			WithRelayLogger(loggingnoop.NewLogger()),
			nil,
			WithRelayTracerProvider(tracingnoop.NewTracerProvider()),
			WithRelayMetricsProvider(metricsnoop.NewMetricsProvider()),
			nil,
		}

		r, err := NewRelay(
			t.Context(),
			&RelayConfig{ClaimMode: ClaimLease},
			newTestClient(t),
			&messagequeuemock.PublisherProviderMock{},
			opts...,
		)
		must.NoError(t, err)
		must.NotNil(t, r)

		test.NotNil(t, r.logger)
		test.NotNil(t, r.tracerProvider)
		test.NotNil(t, r.metricsProvider)
	})
}

func TestRelayConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	// validConfig is what EnsureDefaults produces for a supported dialect; each
	// case below breaks exactly one field so the failure is unambiguous.
	validConfig := func() *RelayConfig {
		cfg := &RelayConfig{ClaimMode: ClaimLease}
		cfg.EnsureDefaults()

		return cfg
	}

	T.Run("accepts a defaulted config", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, validConfig().ValidateWithContext(t.Context()))
	})

	// The dialect is no longer the config's to state — it comes off the
	// database.Client — so an unsupported one is caught by NewRelay.
	T.Run("NewRelay rejects a client on an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := NewRelay(
			t.Context(),
			validConfig(),
			&databasemock.ClientMock{
				DialectFunc: func() dialect.Dialect { return "cassandra" },
			},
			&messagequeuemock.PublisherProviderMock{},
		)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "cassandra")
	})

	T.Run("rejects an unknown claim mode", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.ClaimMode = "telepathy"

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "telepathy")
	})

	// SKIP LOCKED against a dialect that lacks it is no longer representable:
	// NewRelay narrows the claim mode once it knows the client's dialect.
	T.Run("NewRelay downgrades SKIP LOCKED on a dialect that cannot do it", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.ClaimMode = ClaimSkipLocked

		must.False(t, dialect.SQLite.SupportsSkipLocked())

		r, err := NewRelay(
			t.Context(),
			cfg,
			newTestClient(t),
			&messagequeuemock.PublisherProviderMock{},
		)
		must.NoError(t, err)
		test.EqOp(t, ClaimLease, r.cfg.ClaimMode)
	})

	T.Run("NewRelay surfaces a config that fails validation", func(t *testing.T) {
		t.Parallel()

		cfg := &RelayConfig{}
		cfg.EnsureDefaults()
		cfg.ClaimMode = "telepathy"

		_, err := NewRelay(
			t.Context(),
			cfg,
			newTestClient(t),
			&messagequeuemock.PublisherProviderMock{},
		)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "validating outbox relay config")
	})
}

// failingMetricsProvider serves the noop provider's instruments for every name
// except failOn, which reports an error. It walks NewRelay's instrument setup
// one failure at a time.
func failingMetricsProvider(failOn string) metrics.Provider {
	base := metricsnoop.NewMetricsProvider()
	boom := errors.New("instrument unavailable")

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failOn {
				return nil, boom
			}

			return base.NewInt64Counter(name, opts...)
		},
		NewInt64GaugeFunc: func(name string, opts ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
			if name == failOn {
				return nil, boom
			}

			return base.NewInt64Gauge(name, opts...)
		},
		NewFloat64HistogramFunc: func(name string, opts ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			if name == failOn {
				return nil, boom
			}

			return base.NewFloat64Histogram(name, opts...)
		},
	}
}

func TestNewRelay_instrumentFailures(T *testing.T) {
	T.Parallel()

	// Every instrument the relay creates, with the message NewRelay wraps its
	// failure in. A new instrument added without a matching error path shows up
	// here as a construction that unexpectedly succeeds.
	for _, tc := range []struct {
		instrument string
		wantErr    string
	}{
		{"messages_published", "creating messages published counter"},
		{"messages_failed", "creating messages failed counter"},
		{"messages_quarantined", "creating messages quarantined counter"},
		{"messages_reaped", "creating messages reaped counter"},
		{"claim_errors", "creating claim error counter"},
		{"backlog_depth", "creating backlog depth gauge"},
		{"backlog_age_seconds", "creating backlog age gauge"},
		{"publish_latency_ms", "creating publish latency histogram"},
		{"claimed_batch_size", "creating claimed batch size histogram"},
		{"cycle_latency_ms", "creating cycle latency histogram"},
	} {
		T.Run(tc.instrument, func(t *testing.T) {
			t.Parallel()

			r, err := NewRelay(
				t.Context(),
				&RelayConfig{ClaimMode: ClaimLease},
				newTestClient(t),
				&messagequeuemock.PublisherProviderMock{},
				WithRelayMetricsProvider(failingMetricsProvider(fmt.Sprintf("%s_%s", serviceName, tc.instrument))),
			)

			test.Nil(t, r)
			must.Error(t, err)
			test.StrContains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestRelay_Close(T *testing.T) {
	T.Parallel()

	T.Run("a canceled context ends the wait for the loop to drain", func(t *testing.T) {
		t.Parallel()

		// The relay is never started, so done never closes: Close has nothing to
		// wait for but the context, which is exactly the deadline path.
		relay, _ := newTestRelay(t, newTestClient(t), newStubClock())

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := relay.Close(ctx)
		must.Error(t, err)
		test.ErrorIs(t, err, context.Canceled)
	})
}

// newRelayWithProvider builds a relay over a real SQLite client but with a
// caller-supplied publisher provider, so the publisher-resolution failures can
// be driven independently of the database.
func newRelayWithProvider(t *testing.T, client database.Client, provider messagequeue.PublisherProvider) *Relay {
	t.Helper()

	r, err := NewRelay(
		t.Context(),
		&RelayConfig{ClaimMode: ClaimLease},
		client,
		provider,
	)
	must.NoError(t, err)
	must.NotNil(t, r)

	return r
}

func TestRelay_publisherFor(T *testing.T) {
	T.Parallel()

	T.Run("caches one publisher per topic", func(t *testing.T) {
		t.Parallel()

		var calls int
		provider := &messagequeuemock.PublisherProviderMock{
			NewPublisherFunc: func(context.Context, string) (messagequeue.Publisher, error) {
				calls++

				return &messagequeuemock.PublisherMock{StopFunc: func() {}}, nil
			},
			CloseFunc: func() {},
		}

		r := newRelayWithProvider(t, newTestClient(t), provider)

		first, err := r.publisherFor(t.Context(), "orders")
		must.NoError(t, err)

		second, err := r.publisherFor(t.Context(), "orders")
		must.NoError(t, err)

		test.EqOp(t, 1, calls)
		test.True(t, first == second)
	})

	T.Run("surfaces a provider that cannot build a publisher", func(t *testing.T) {
		t.Parallel()

		provider := &messagequeuemock.PublisherProviderMock{
			NewPublisherFunc: func(context.Context, string) (messagequeue.Publisher, error) {
				return nil, errors.New("broker unreachable")
			},
			CloseFunc: func() {},
		}

		r := newRelayWithProvider(t, newTestClient(t), provider)

		p, err := r.publisherFor(t.Context(), "orders")
		test.Nil(t, p)
		must.Error(t, err)
		test.StrContains(t, err.Error(), `building publisher for topic "orders"`)
	})

	T.Run("a publish that cannot resolve a publisher is reported", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		provider := &messagequeuemock.PublisherProviderMock{
			NewPublisherFunc: func(context.Context, string) (messagequeue.Publisher, error) {
				return nil, errors.New("broker unreachable")
			},
			CloseFunc: func() {},
		}

		r := newRelayWithProvider(t, client, provider)

		err := r.publish(t.Context(), &claimedMessage{topic: "orders", payload: []byte(`{"id":"a"}`), key: "k"})
		must.Error(t, err)
		test.StrContains(t, err.Error(), "resolving publisher")
	})
}

func TestRelay_databaseFailures(T *testing.T) {
	T.Parallel()

	// dropTable removes the outbox table out from under a fully constructed
	// relay, so every statement it issues fails the way a revoked grant or a
	// botched migration would. One fault, every query path.
	dropTable := func(t *testing.T, client database.Client) {
		t.Helper()

		_, err := client.Writer().ExecContext(t.Context(), "DROP TABLE outbox_messages")
		must.NoError(t, err)
	}

	T.Run("a failing claim is counted and logged, not panicked on", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, newStubClock())

		dropTable(t, client)

		// cycle swallows the error by design: there is no caller to hand it to.
		relay.cycle(t.Context())
	})

	T.Run("a failing claim is reported by claim itself", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, newStubClock())

		dropTable(t, client)

		msgs, err := relay.claim(t.Context())
		test.Nil(t, msgs)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "claiming outbox batch")
	})

	T.Run("a failing backlog sample is acknowledged, not fatal", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, newStubClock())

		dropTable(t, client)

		relay.sampleBacklog(t.Context())

		_, _, err := relay.backlog(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "reading outbox backlog")
	})

	T.Run("a failing reap is acknowledged, not fatal", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, newStubClock())

		dropTable(t, client)

		relay.reap(t.Context())
	})

	T.Run("a failing markPublished is reported", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, newStubClock())

		dropTable(t, client)

		err := relay.markPublished(t.Context(), []string{"abc"})
		must.Error(t, err)
		test.StrContains(t, err.Error(), "marking outbox messages published")
	})

	T.Run("a failing failure record is logged rather than returned", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, newStubClock())

		dropTable(t, client)

		// recordFailure has no error to return: the lease expires on its own, so
		// the message is retried regardless.
		relay.recordFailure(t.Context(), &claimedMessage{id: "abc", topic: "orders", attempts: 1}, errors.New("publish failed"))
	})
}

func TestRelay_Run_ticks(T *testing.T) {
	T.Parallel()

	T.Run("the poll and reap tickers both drive work", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)

		relay, rec := newTestRelay(t, client, c, func(cfg *RelayConfig) {
			cfg.PollInterval = time.Millisecond
		})

		// Set below the validated minimum after construction: the config is
		// already valid, and this test is about the loop, not the bounds. A
		// one-second reap interval would otherwise dominate the test's runtime.
		relay.cfg.ReapInterval = 2 * time.Millisecond

		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		go relay.Run()

		// Wait for a tick — not the drain on Close — to publish the message.
		deadline := time.Now().Add(10 * time.Second)
		for len(rec.payloads()) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}

		// The reap tick is slower than the poll tick, so give it a few of its own
		// periods to fire before the loop is shut down.
		time.Sleep(20 * time.Millisecond)

		must.NoError(t, relay.Close(t.Context()))
		test.Eq(t, []string{`{"id":"a"}`}, rec.payloads())
	})
}

func TestRelay_backlog_age(T *testing.T) {
	T.Parallel()

	T.Run("a row created after the clock reports zero age, not a negative one", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)

		// Enqueue an hour ahead, then rewind: the oldest row now sits in the
		// clock's future, which is what clock skew between writer and relay looks
		// like. A negative age would read as a wildly stale backlog on the gauge.
		c.advance(time.Hour)
		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		c.advance(-time.Hour)

		depth, age, err := relay.backlog(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(1), depth)
		test.EqOp(t, time.Duration(0), age)
	})

	T.Run("an ordinary backlog reports the real age", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)

		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		c.advance(90 * time.Second)

		depth, age, err := relay.backlog(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(1), depth)
		test.EqOp(t, 90*time.Second, age)
	})
}
