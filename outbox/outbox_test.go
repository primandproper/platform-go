package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/migrate"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	"github.com/primandproper/platform-go/v13/outbox/migrations"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestNewWriter(T *testing.T) {
	T.Parallel()

	T.Run("accepts every supported dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			w, err := NewWriter(d)
			must.NoError(t, err)
			test.EqOp(t, "outbox_messages", w.table)
		}
	})

	T.Run("rejects an unknown dialect", func(t *testing.T) {
		t.Parallel()

		_, err := NewWriter("cassandra")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects a table name that is not an identifier", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"outbox; DROP TABLE users", "outbox messages", "1outbox", "a.b.c", ""} {
			_, err := NewWriter(dialect.SQLite, WithWriterTablePrefix(name))
			if name == "" {
				// An empty override is ignored rather than rejected.
				test.NoError(t, err)

				continue
			}

			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
		}
	})

	// The writer and the DDL renderer live in different packages and reject the
	// same bad name. They previously raised two distinct sentinels with
	// identical messages, so a caller checking one against the other's error
	// silently got false; one shared sentinel is what makes that check work.
	T.Run("rejects a bad table name identically to the migrations package", func(t *testing.T) {
		t.Parallel()

		const bad = "outbox; DROP TABLE users"

		_, writerErr := NewWriter(dialect.SQLite, WithWriterTablePrefix(bad))
		_, migrationErr := migrations.Statements(dialect.SQLite, bad)

		must.Error(t, writerErr)
		must.Error(t, migrationErr)
		test.ErrorIs(t, writerErr, dialect.ErrInvalidIdentifier)
		test.ErrorIs(t, migrationErr, dialect.ErrInvalidIdentifier)
	})

	T.Run("accepts a schema-qualified table name", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(dialect.Postgres, WithWriterTablePrefix("events"))
		must.NoError(t, err)
		test.EqOp(t, "events_outbox_messages", w.table)
	})
}

func TestWriter_Enqueue(T *testing.T) {
	T.Parallel()

	T.Run("writes rows inside the caller's transaction", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)

		enqueue(t, client, newTestWriter(t, c),
			Message{Topic: "orders", Payload: map[string]any{"id": "a"}},
			Message{Topic: "shipments", Key: "cart-1", Payload: map[string]any{"id": "b"}},
		)

		test.EqOp(t, 2, countRows(t, client, "1=1"))
		test.EqOp(t, 1, countRows(t, client, "topic = 'shipments' AND partition_key = 'cart-1'"))

		// A new message is immediately eligible.
		test.EqOp(t, 2, countRows(t, client, "next_attempt = created_at AND attempts = 0"))
	})

	T.Run("rolls back with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		w := newTestWriter(t, c)

		boom := platformerrors.New("caller work failed")

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			if enqueueErr := w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}}); enqueueErr != nil {
				return enqueueErr
			}

			// The caller's own work fails after the outbox write.
			return boom
		})

		test.ErrorIs(t, err, boom)

		// This is the entire point of the package: no event survives a rolled
		// back transaction.
		test.EqOp(t, 0, countRows(t, client, "1=1"))
	})

	T.Run("is a no-op with no messages", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)

		enqueue(t, client, newTestWriter(t, c))

		test.EqOp(t, 0, countRows(t, client, "1=1"))
	})

	T.Run("rejects invalid messages", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		w := newTestWriter(t, c)

		cases := map[string]struct {
			expected error
			msg      Message
		}{
			"empty topic": {msg: Message{Payload: map[string]any{"id": "a"}}, expected: ErrEmptyTopic},
			"nil payload": {msg: Message{Topic: "orders"}, expected: ErrNilPayload},
		}

		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
					return w.Enqueue(t.Context(), q, tc.msg)
				})
				test.ErrorIs(t, err, tc.expected)
			})
		}
	})

	T.Run("rejects a nil executor", func(t *testing.T) {
		t.Parallel()

		w := newTestWriter(t, newStubClock())

		err := w.Enqueue(t.Context(), nil, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("rejects a payload that cannot be marshaled", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		w := newTestWriter(t, c)

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: make(chan int)})
		})
		test.Error(t, err)

		test.EqOp(t, 0, countRows(t, client, "1=1"))
	})
}

// migrateOutboxTable creates the outbox table through database/migrate rather
// than by executing the DDL directly, which is how a consumer that already runs
// migrations should be wiring it up.
func migrateOutboxTable(t *testing.T, client database.Client, d dialect.Dialect, table string) {
	t.Helper()

	ddl, err := migrations.SQL(d, table)
	must.NoError(t, err)

	m, err := migrate.New(d, fstest.MapFS{},
		migrate.WithGeneratedMigration(1, "create_"+table, ddl),
		migrate.WithLogger(loggingnoop.NewLogger()),
	)
	must.NoError(t, err)

	raw, ok := client.(database.RawAccess)
	must.True(t, ok, must.Sprint("client does not expose RawAccess"))

	must.NoError(t, m.Migrate(t.Context(), raw.WriteDB()))

	// Idempotent, so a second replica booting against the same database is a
	// no-op rather than a failure.
	must.NoError(t, m.Migrate(t.Context(), raw.WriteDB()))
}

// countingExecutor counts the statements an Enqueue runs, so the promise that a
// derived event rides the caller's insert rather than adding one of its own is
// asserted rather than assumed.
type countingExecutor struct {
	database.SQLQueryExecutor

	execs int
}

func (e *countingExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	e.execs++

	return e.SQLQueryExecutor.ExecContext(ctx, query, args...)
}

// newSideEffectWriter builds a Writer with side effects registered, failing the
// test if the registration is refused.
func newSideEffectWriter(t *testing.T, c *stubClock, opts ...WriterOption) *Writer {
	t.Helper()

	w, err := NewWriter(dialect.SQLite, append([]WriterOption{WithWriterClock(c)}, opts...)...)
	must.NoError(t, err)

	return w
}

// derive returns a side effect that emits one message per caller message, and
// appends its own name to ran so ordering is observable.
func derive(ran *[]string, name, topic string) SideEffect {
	return func(_ context.Context, _ database.SQLQueryExecutor, msgs []Message) ([]Message, error) {
		*ran = append(*ran, name)

		derived := make([]Message, 0, len(msgs))
		for i := range msgs {
			derived = append(derived, Message{Topic: topic, Key: msgs[i].Key, Payload: map[string]any{"id": msgs[i].Key}})
		}

		return derived, nil
	}
}

func TestNewWriter_sideEffectRegistration(T *testing.T) {
	T.Parallel()

	noop := func(context.Context, database.SQLQueryExecutor, []Message) ([]Message, error) { return nil, nil }

	T.Run("accepts distinctly named side effects", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(dialect.SQLite,
			WithWriterSideEffect("index", noop),
			WithWriterSideEffect("webhooks", noop))
		must.NoError(t, err)
		test.SliceLen(t, 2, w.sideEffects)

		// Registration order is the order they run in, so it is preserved
		// rather than collected into a map.
		test.EqOp(t, "index", w.sideEffects[0].name)
		test.EqOp(t, "webhooks", w.sideEffects[1].name)
	})

	// Every one of these is refused rather than dropped. A registration exists
	// because the call site cannot be trusted to remember the event, so a
	// registration that disappears at construction is the same forgotten event
	// with even less to see it by.
	T.Run("refuses an unnamed side effect", func(t *testing.T) {
		t.Parallel()

		_, err := NewWriter(dialect.SQLite, WithWriterSideEffect("", noop))
		test.ErrorIs(t, err, ErrUnnamedSideEffect)
	})

	T.Run("refuses a nil side effect", func(t *testing.T) {
		t.Parallel()

		_, err := NewWriter(dialect.SQLite, WithWriterSideEffect("index", nil))
		test.ErrorIs(t, err, ErrNilSideEffect)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a duplicated name", func(t *testing.T) {
		t.Parallel()

		// Two different effects under one name, which is the shape this catches:
		// the same registration wired twice would derive the same event twice.
		_, err := NewWriter(dialect.SQLite,
			WithWriterSideEffect("index", noop),
			WithWriterSideEffect("index", func(context.Context, database.SQLQueryExecutor, []Message) ([]Message, error) {
				return []Message{{Topic: "orders-index", Payload: map[string]any{"id": "order-1"}}}, nil
			}))
		test.ErrorIs(t, err, ErrDuplicateSideEffect)
	})
}

func TestWriter_Enqueue_sideEffects(T *testing.T) {
	T.Parallel()

	T.Run("writes derived messages in the caller's statement", func(t *testing.T) {
		t.Parallel()

		var ran []string

		c := newStubClock()
		client := newTestClient(t)
		w := newSideEffectWriter(t, c, WithWriterSideEffect("index", derive(&ran, "index", "orders-index")))

		var counter *countingExecutor

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			counter = &countingExecutor{SQLQueryExecutor: q}

			return w.Enqueue(t.Context(), counter,
				Message{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}},
				Message{Topic: "orders", Key: "order-2", Payload: map[string]any{"id": "order-2"}})
		}))

		// The whole point of deriving inside Enqueue rather than around it: two
		// caller messages and two derived ones still cost one round trip.
		test.EqOp(t, 1, counter.execs)
		test.EqOp(t, 4, countRows(t, client, "1=1"))
		test.EqOp(t, 2, countRows(t, client, "topic = 'orders-index'"))

		// Derived from the caller's message, so it inherits the key that buys
		// per-document ordering.
		test.EqOp(t, 1, countRows(t, client, "topic = 'orders-index' AND partition_key = 'order-1'"))
	})

	T.Run("runs side effects in registration order", func(t *testing.T) {
		t.Parallel()

		var ran []string

		c := newStubClock()
		client := newTestClient(t)
		w := newSideEffectWriter(t, c,
			WithWriterSideEffect("first", derive(&ran, "first", "first-derived")),
			WithWriterSideEffect("second", derive(&ran, "second", "second-derived")))

		enqueue(t, client, w, Message{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}})

		test.Eq(t, []string{"first", "second"}, ran)
		test.EqOp(t, 3, countRows(t, client, "1=1"))
	})

	// A side effect that saw another's output would turn the registration list
	// into an evaluation order to reason about, and would let a derived event
	// derive events of its own.
	T.Run("shows a side effect only the caller's messages", func(t *testing.T) {
		t.Parallel()

		var (
			ran  []string
			seen []string
		)

		c := newStubClock()
		client := newTestClient(t)
		w := newSideEffectWriter(t, c,
			WithWriterSideEffect("first", derive(&ran, "first", "first-derived")),
			WithWriterSideEffect("second", func(_ context.Context, _ database.SQLQueryExecutor, msgs []Message) ([]Message, error) {
				for _, msg := range msgs {
					seen = append(seen, msg.Topic)
				}

				return nil, nil
			}))

		enqueue(t, client, w, Message{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}})

		test.Eq(t, []string{"orders"}, seen)
	})

	// The slice is the caller's backing array where the variadic came from one,
	// and one effect's edit must not reach the next effect or the rows.
	T.Run("hands every side effect its own copy", func(t *testing.T) {
		t.Parallel()

		var seen []string

		c := newStubClock()
		client := newTestClient(t)
		w := newSideEffectWriter(t, c,
			WithWriterSideEffect("mutating", func(_ context.Context, _ database.SQLQueryExecutor, msgs []Message) ([]Message, error) {
				msgs[0].Topic = "clobbered"

				return nil, nil
			}),
			WithWriterSideEffect("observing", func(_ context.Context, _ database.SQLQueryExecutor, msgs []Message) ([]Message, error) {
				for _, msg := range msgs {
					seen = append(seen, msg.Topic)
				}

				return nil, nil
			}))

		callerMessages := []Message{{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}}}

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return w.Enqueue(t.Context(), q, callerMessages...)
		}))

		test.Eq(t, []string{"orders"}, seen)
		test.EqOp(t, "orders", callerMessages[0].Topic)
		test.EqOp(t, 1, countRows(t, client, "topic = 'orders'"))
		test.EqOp(t, 0, countRows(t, client, "topic = 'clobbered'"))
	})

	T.Run("commits rows a side effect writes with the row change", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		createDispatchTable(t, client)

		w := newSideEffectWriter(t, c, WithWriterSideEffect("webhooks", dispatchWebhooks))

		enqueue(t, client, w, Message{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}})

		test.EqOp(t, 1, countDispatches(t, client))
	})

	T.Run("rolls a side effect's rows back with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		createDispatchTable(t, client)

		w := newSideEffectWriter(t, c, WithWriterSideEffect("webhooks", dispatchWebhooks))

		boom := platformerrors.New("payment declined")

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			if enqueueErr := w.Enqueue(t.Context(), q, Message{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}}); enqueueErr != nil {
				return enqueueErr
			}

			return boom
		})

		test.ErrorIs(t, err, boom)
		test.EqOp(t, 0, countRows(t, client, "1=1"))
		test.EqOp(t, 0, countDispatches(t, client))
	})

	T.Run("aborts the enqueue when a side effect fails", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		createDispatchTable(t, client)

		boom := platformerrors.New("dispatch unavailable")

		var laterRan bool

		w := newSideEffectWriter(t, c,
			WithWriterSideEffect("webhooks", dispatchWebhooks),
			WithWriterSideEffect("failing", func(context.Context, database.SQLQueryExecutor, []Message) ([]Message, error) {
				return nil, boom
			}),
			WithWriterSideEffect("later", func(context.Context, database.SQLQueryExecutor, []Message) ([]Message, error) {
				laterRan = true

				return nil, nil
			}))

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}})
		})

		// Returned rather than swallowed, which is what leaves the caller's
		// transaction to roll back — taking the rows the earlier side effect
		// had already written with it.
		test.ErrorIs(t, err, boom)
		test.False(t, laterRan)
		test.EqOp(t, 0, countRows(t, client, "1=1"))
		test.EqOp(t, 0, countDispatches(t, client))
	})

	T.Run("runs no side effects for an enqueue with no messages", func(t *testing.T) {
		t.Parallel()

		var ran []string

		c := newStubClock()
		client := newTestClient(t)
		w := newSideEffectWriter(t, c, WithWriterSideEffect("index", derive(&ran, "index", "orders-index")))

		enqueue(t, client, w)

		test.SliceEmpty(t, ran)
		test.EqOp(t, 0, countRows(t, client, "1=1"))
	})

	T.Run("names the side effects that ran on the span", func(t *testing.T) {
		t.Parallel()

		var ran []string

		c := newStubClock()
		client := newTestClient(t)
		w := newSideEffectWriter(t, c,
			WithWriterSideEffect("first", derive(&ran, "first", "first-derived")),
			WithWriterSideEffect("second", derive(&ran, "second", "second-derived")))

		obs := observability.NewRecordingObserver()
		w.o11y = obs

		enqueue(t, client, w, Message{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}})

		obs.ObservedOperationWithData(t, map[string]any{
			sideEffectsKey:  []string{"first", "second"},
			messageCountKey: 3,
		})
	})

	// The effect that failed is one that ran, and a trace omitting it describes
	// an enqueue other than the one that happened.
	T.Run("names a failing side effect on the span", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		w := newSideEffectWriter(t, c,
			WithWriterSideEffect("failing", func(context.Context, database.SQLQueryExecutor, []Message) ([]Message, error) {
				return nil, platformerrors.New("dispatch unavailable")
			}))

		obs := observability.NewRecordingObserver()
		w.o11y = obs

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "order-1"}})
		})
		test.Error(t, err)

		op := obs.ObservedOperationWithData(t, map[string]any{sideEffectsKey: []string{"failing"}})
		test.SliceLen(t, 1, op.Errors)
	})
}

// createDispatchTable stands in for a consumer's own derived table — the
// webhook dispatch rows a side effect writes rather than publishes.
func createDispatchTable(t *testing.T, client database.Client) {
	t.Helper()

	_, err := client.Writer().ExecContext(t.Context(), "CREATE TABLE dispatches (id TEXT NOT NULL)")
	must.NoError(t, err)
}

// dispatchWebhooks writes a row per caller message and returns no messages, the
// shape of a side effect whose output is rows rather than events.
func dispatchWebhooks(ctx context.Context, q database.SQLQueryExecutor, msgs []Message) ([]Message, error) {
	for i := range msgs {
		if _, err := q.ExecContext(ctx, "INSERT INTO dispatches (id) VALUES (?)", msgs[i].Key); err != nil {
			return nil, err
		}
	}

	return nil, nil
}

func countDispatches(t *testing.T, client database.Client) int {
	t.Helper()

	var n int
	must.NoError(t, client.Reader().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dispatches").Scan(&n))

	return n
}

// fanoutRecord is one observation of the fan-out histogram.
type fanoutRecord struct {
	topic string
	value float64
}

// recordingHistogram captures what it was asked to record, with the topic
// attribute the measurement carried.
type recordingHistogram struct {
	records []fanoutRecord
	mu      sync.Mutex
}

var _ metrics.Float64Histogram = (*recordingHistogram)(nil)

func (h *recordingHistogram) Record(_ context.Context, value float64, opts ...metric.RecordOption) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var topic string

	set := metric.NewRecordConfig(opts).Attributes()

	attrs := set.ToSlice()
	for i := range attrs {
		if string(attrs[i].Key) == keys.TopicKey {
			topic = attrs[i].Value.AsString()
		}
	}

	h.records = append(h.records, fanoutRecord{topic: topic, value: value})
}

func (h *recordingHistogram) observed() []fanoutRecord {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]fanoutRecord(nil), h.records...)
}

func TestWriter_Enqueue_fanoutHistogram(T *testing.T) {
	T.Parallel()

	// newFanoutWriter builds a Writer whose fan-out histogram is the recorder,
	// leaving every other instrument the noop provider's.
	newFanoutWriter := func(t *testing.T, opts ...WriterOption) (*Writer, *recordingHistogram) {
		t.Helper()

		hist := &recordingHistogram{}
		base := metricsnoop.NewMetricsProvider()

		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: base.NewInt64Counter,
			NewFloat64HistogramFunc: func(name string, o ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				if name == fmt.Sprintf("%s_enqueue_fanout", serviceName) {
					return hist, nil
				}

				return base.NewFloat64Histogram(name, o...)
			},
		}

		w, err := NewWriter(dialect.SQLite, append([]WriterOption{
			WithWriterClock(newStubClock()),
			WithWriterMetricsProvider(provider),
		}, opts...)...)
		must.NoError(t, err)

		return w, hist
	}

	// Sampled once per distinct topic, carrying the whole enqueue's count: the
	// question it answers is whether a write of this kind still owes what it
	// used to, so the caller's own topic has to see the total the derived event
	// contributes to.
	T.Run("records the enqueue's total once per distinct topic", func(t *testing.T) {
		t.Parallel()

		var ran []string

		client := newTestClient(t)
		w, hist := newFanoutWriter(t, WithWriterSideEffect("index", derive(&ran, "index", "orders-index")))

		enqueue(t, client, w,
			Message{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}},
			Message{Topic: "orders", Key: "order-2", Payload: map[string]any{"id": "order-2"}})

		test.Eq(t, []fanoutRecord{
			{topic: "orders", value: 4},
			{topic: "orders-index", value: 4},
		}, hist.observed())
	})

	// This is the drop the histogram exists to make visible: the same call site
	// with its registration removed records three rather than four against the
	// topic it still writes.
	T.Run("falls when a write stops owing an event", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		w, hist := newFanoutWriter(t)

		enqueue(t, client, w,
			Message{Topic: "orders", Key: "order-1", Payload: map[string]any{"id": "order-1"}},
			Message{Topic: "orders", Key: "order-2", Payload: map[string]any{"id": "order-2"}})

		test.Eq(t, []fanoutRecord{{topic: "orders", value: 2}}, hist.observed())
	})

	T.Run("records nothing for an enqueue that fails", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		w, hist := newFanoutWriter(t)

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders"})
		})
		test.ErrorIs(t, err, ErrNilPayload)

		test.SliceEmpty(t, hist.observed())
	})
}

func TestNewWriter_instrumentFailures(T *testing.T) {
	T.Parallel()

	// Every instrument the writer creates, with the message NewWriter wraps its
	// failure in. A new instrument added without a matching error path shows up
	// here as a construction that unexpectedly succeeds.
	for _, tc := range []struct {
		instrument string
		wantErr    string
	}{
		{"messages_enqueued", "creating messages enqueued counter"},
		{"enqueue_fanout", "creating enqueue fanout histogram"},
	} {
		T.Run(tc.instrument, func(t *testing.T) {
			t.Parallel()

			w, err := NewWriter(dialect.SQLite,
				WithWriterMetricsProvider(failingMetricsProvider(fmt.Sprintf("%s_%s", serviceName, tc.instrument))))

			test.Nil(t, w)
			must.Error(t, err)
			test.StrContains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestOutbox_MigratorIntegration(T *testing.T) {
	T.Parallel()

	T.Run("sqlite table created via migrate is usable", func(t *testing.T) {
		t.Parallel()

		const table = "migrated_outbox"

		c := newStubClock()
		client := newTestClient(t)

		migrateOutboxTable(t, client, dialect.SQLite, table)

		w, err := NewWriter(dialect.SQLite, WithWriterClock(c), WithWriterTablePrefix(table))
		must.NoError(t, err)

		relay, rec := newTestRelay(t, client, c, func(cfg *RelayConfig) {
			cfg.TablePrefix = table
		})

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		}))

		relay.cycle(t.Context())

		test.Eq(t, []string{`{"id":"a"}`}, rec.payloads())
	})
}
