package saga

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/outbox"
	outboxmigrations "github.com/primandproper/platform-go/v13/outbox/migrations"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// collectingPublisher records every event it is handed.
type collectingPublisher struct {
	events []Event
}

func (p *collectingPublisher) Publish(_ context.Context, _ database.SQLQueryExecutor, events ...Event) error {
	p.events = append(p.events, events...)

	return nil
}

func (p *collectingPublisher) types() []EventType {
	rendered := make([]EventType, 0, len(p.events))
	for i := range p.events {
		rendered = append(rendered, p.events[i].Type)
	}

	return rendered
}

func TestEvents_Lifecycle(T *testing.T) {
	T.Parallel()

	T.Run("a completed saga emits a step event per step and a completion", func(t *testing.T) {
		t.Parallel()

		publisher := &collectingPublisher{}

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"), noopStep("two"))
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk, WithWorkerEventPublisher(publisher))

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 5)
		must.EqOp(t, StatusCompleted, inst.Status)

		test.Eq(t, []EventType{
			EventStepCompleted,
			EventStepCompleted,
			EventCompleted,
		}, publisher.types())

		test.EqOp(t, "one", publisher.events[0].Step)
		test.EqOp(t, 0, publisher.events[0].StepIndex)
		test.EqOp(t, "two", publisher.events[1].Step)
		test.EqOp(t, StatusCompleted, publisher.events[2].Status)
		test.EqOp(t, "orders", publisher.events[2].Definition)
		test.EqOp(t, baseTime, publisher.events[2].OccurredAt)
	})

	T.Run("a compensated saga narrates the unwinding", func(t *testing.T) {
		t.Parallel()

		publisher := &collectingPublisher{}
		rec := &recorder{}

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders",
			trailStep(rec, "charge", nil, nil),
			trailStep(rec, "fail", retry.Unretryable(platformerrors.New("declined")), nil),
		)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk, WithWorkerEventPublisher(publisher))

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)
		must.EqOp(t, StatusCompensated, inst.Status)

		test.Eq(t, []EventType{
			EventStepCompleted,
			EventCompensating,
			EventStepCompensated,
			EventStepCompensated,
			EventCompensated,
		}, publisher.types())

		compensating := publisher.events[1]
		test.EqOp(t, "fail", compensating.Step)
		test.EqOp(t, StatusCompensating, compensating.Status)
		test.StrContains(t, compensating.Error, "declined")
	})

	T.Run("a stuck saga emits the event worth paging on", func(t *testing.T) {
		t.Parallel()

		publisher := &collectingPublisher{}

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders",
			Step[testState]{
				Name: "charge",
				Do:   func(context.Context, *testState) error { return nil },
				Undo: func(context.Context, *testState) error { return platformerrors.New("the refund API is down") },
			},
			Step[testState]{
				Name: "fail",
				Do:   func(context.Context, *testState) error { return retry.Unretryable(platformerrors.New("no")) },
			},
		)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk, WithWorkerEventPublisher(publisher))

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 15)
		must.EqOp(t, StatusStuck, inst.Status)

		last := publisher.events[len(publisher.events)-1]
		test.EqOp(t, EventStuck, last.Type)
		test.EqOp(t, StatusStuck, last.Status)
		test.StrContains(t, last.Error, "the refund API is down")
	})

	T.Run("a failing publisher rolls the advance back", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"), noopStep("two"))
		worker := newWorker(t, store, registry, newStubClock(), WithWorkerEventPublisher(
			EventPublisherFunc(func(context.Context, database.SQLQueryExecutor, ...Event) error {
				return platformerrors.New("the outbox table is missing")
			}),
		))

		startedRecord(t, store, registry, "orders", "i1")

		drainOnce(t, worker)

		// The cursor did not move: the step will be retried, and its
		// idempotency key is what stops the retry doing the work twice.
		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, 0, inst.CurrentStep)
		test.EqOp(t, StatusRunning, inst.Status)
	})

	T.Run("carries no saga state", func(t *testing.T) {
		t.Parallel()

		publisher := &collectingPublisher{}

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", Step[testState]{
			Name: "one",
			Do: func(_ context.Context, s *testState) error {
				s.Trail = append(s.Trail, "a-secret-the-subscriber-must-not-see")

				return nil
			},
		})
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk, WithWorkerEventPublisher(publisher))

		startedRecord(t, store, registry, "orders", "i1")

		drain(t, worker, store, clk, "i1", 5)

		for i := range publisher.events {
			encoded, err := json.Marshal(publisher.events[i])
			must.NoError(t, err)
			test.StrNotContains(t, string(encoded), "a-secret-the-subscriber-must-not-see")
		}
	})
}

func TestPublish(T *testing.T) {
	T.Parallel()

	T.Run("does nothing without a publisher or without events", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, publish(t.Context(), nil, nil))
		test.NoError(t, publish(t.Context(), &collectingPublisher{}, nil))
	})
}

func TestNewOutboxPublisher(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil writer", func(t *testing.T) {
		t.Parallel()

		_, err := NewOutboxPublisher(nil)
		test.Error(t, err)
	})

	T.Run("enqueues one message per event, keyed by instance", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		for _, stmt := range outboxMigrationStatements(t) {
			_, err := env.client.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, err)
		}

		writer, err := outbox.NewWriter(dialect.SQLite)
		must.NoError(t, err)

		publisher, err := NewOutboxPublisher(writer, nil, WithEventTopic(""))
		must.NoError(t, err)

		inst := newRecord("i1", "orders", []string{"one"}, testState{}, baseTime)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return publisher.Publish(t.Context(), q,
				newEvent(EventStarted, inst, "one", 0, baseTime, ""),
				newEvent(EventCompleted, inst, "", 1, baseTime, ""),
			)
		}))

		var count int
		must.NoError(t, env.client.Reader().QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM outbox_messages WHERE topic = ? AND partition_key = ?",
			DefaultEventTopic, "i1").Scan(&count))
		test.EqOp(t, 2, count)
	})

	T.Run("honors a custom topic and does nothing for no events", func(t *testing.T) {
		t.Parallel()

		writer, err := outbox.NewWriter(dialect.SQLite)
		must.NoError(t, err)

		publisher, err := NewOutboxPublisher(writer, WithEventTopic("sagas"))
		must.NoError(t, err)

		test.EqOp(t, "sagas", publisher.topic)

		test.NoError(t, publisher.Publish(t.Context(), nil))
	})
}

// outboxMigrationStatements renders the outbox DDL for SQLite.
func outboxMigrationStatements(t *testing.T) []string {
	t.Helper()

	stmts, err := outboxmigrations.Statements(dialect.SQLite, outbox.DefaultTablePrefix)
	must.NoError(t, err)

	return stmts
}
