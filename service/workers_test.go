package service

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/searchsync"

	"github.com/primandproper/primitives-go/v2/batching"
	"github.com/primandproper/primitives-go/v2/distributedlock/memory"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"
	"github.com/primandproper/primitives-go/v2/messagequeue"
	messagequeuemock "github.com/primandproper/primitives-go/v2/messagequeue/mock"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// groupConsumers is the broker a pool group consumes from. Each consumer
// journals when it starts and when it stops, and hands each topic's handler
// whatever payloads the test queued for that topic before it blocks.
func groupConsumers(j *journal, payloads map[string][][]byte, newErr error) *messagequeuemock.ConsumerProviderMock {
	return &messagequeuemock.ConsumerProviderMock{
		NewConsumerFunc: func(_ context.Context, topic string, handler messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
			if newErr != nil {
				return nil, newErr
			}

			return &messagequeuemock.ConsumerMock{
				ConsumeFunc: func(ctx context.Context, _ chan<- error) {
					j.record("consume:" + topic)

					for _, payload := range payloads[topic] {
						_ = handler(ctx, payload)
					}

					<-ctx.Done()
					j.record("stop:" + topic)
				},
			}, nil
		},
		CloseFunc: func() {},
	}
}

func poolGroup(t *testing.T, specs []jobs.PoolSpec, provider messagequeue.ConsumerProvider) *jobs.PoolGroup {
	t.Helper()

	group, err := jobs.NewPoolGroup(t.Context(), specs, provider)
	must.NoError(t, err)

	return group
}

func TestService_PoolGroup(T *testing.T) {
	T.Parallel()

	T.Run("starts with the loops and drains after ingress, before the clients", func(t *testing.T) {
		t.Parallel()

		j := &journal{}
		srv := newFakeServer(j, "http")
		i := lifecycleInjector(t, j, srv)

		handler := func(context.Context, []byte) error { return nil }
		do.ProvideValue(i, poolGroup(t, []jobs.PoolSpec{
			{Topic: "orders", Handler: handler},
			{Topic: "invoices", Handler: handler},
		}, groupConsumers(j, nil, nil)))

		svc, err := New(i)
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		errs := make(chan error, 1)
		go func() { errs <- svc.Run(ctx) }()

		waitFor(t, j, "consume:orders")
		waitFor(t, j, "consume:invoices")
		waitFor(t, j, "serve:http")
		cancel()

		must.NoError(t, <-errs)

		events := j.all()
		happensBefore(t, events, "shutdown:http", "stop:orders")
		happensBefore(t, events, "shutdown:http", "stop:invoices")
		happensBefore(t, events, "stop:orders", "close:consumers")
		happensBefore(t, events, "stop:invoices", "close:database")
	})

	T.Run("a group that cannot start stops the service before it serves", func(t *testing.T) {
		t.Parallel()

		// A subscription that cannot be established is a startup failure. The
		// group reports it from Start, and Run has to hand it back rather than
		// serving a worker that drains nothing.
		j := &journal{}
		srv := newFakeServer(j, "http")
		i := lifecycleInjector(t, j, srv)

		errSubscribe := platformerrors.New("subscribing")
		do.ProvideValue(i, poolGroup(t, []jobs.PoolSpec{
			{Topic: "orders", Handler: func(context.Context, []byte) error { return nil }},
		}, groupConsumers(j, nil, errSubscribe)))

		loop := newFakeRunner(j, "app")

		svc, err := New(i, WithRunners(loop))
		must.NoError(t, err)

		err = svc.Run(t.Context())
		must.Error(t, err)
		test.ErrorIs(t, err, errSubscribe)

		events := j.all()
		test.SliceNotContains(t, events, "serve:http")
		test.SliceContains(t, events, "close:app")
		test.SliceContains(t, events, "close:database")
	})
}

// startingRunner is an application's runner that happens to have a Start of
// its own — which the application calls, and Run must not.
type startingRunner struct {
	*fakeRunner
}

func (r *startingRunner) Start(context.Context) error {
	r.journal.record("start:" + r.name)

	return platformerrors.New("started by somebody who was not told to")
}

func TestService_StartsOnlyThePoolGroup(T *testing.T) {
	T.Parallel()

	T.Run("an application runner with a Start of its own is not started", func(t *testing.T) {
		t.Parallel()

		j := &journal{}
		i := lifecycleInjector(t, j, nil)

		loop := &startingRunner{fakeRunner: newFakeRunner(j, "app")}

		svc, err := New(i, WithRunners(loop))
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		errs := make(chan error, 1)
		go func() { errs <- svc.Run(ctx) }()

		waitFor(t, j, "run:app")
		cancel()

		must.NoError(t, <-errs)
		test.SliceNotContains(t, j.all(), "start:app")
	})
}

// stampedIndex is a searchsync source and target over one document, which is
// as much of an index as the registry's shutdown obligation needs: the target
// accepts it, and the accepted ID is what the stamp buffer holds until Close.
type stampedIndex struct {
	journal *journal
}

type indexedDoc struct{}

func (x *stampedIndex) Fetch(_ context.Context, ids ...string) ([]searchsync.Document[indexedDoc], error) {
	docs := make([]searchsync.Document[indexedDoc], 0, len(ids))
	for _, id := range ids {
		docs = append(docs, searchsync.Document[indexedDoc]{ID: id, Body: &indexedDoc{}})
	}

	return docs, nil
}

func (x *stampedIndex) Scan(context.Context, string, int) ([]searchsync.Document[indexedDoc], error) {
	return nil, nil
}

func (x *stampedIndex) Upsert(_ context.Context, docs ...searchsync.Document[indexedDoc]) error {
	for _, doc := range docs {
		x.journal.record("upsert:" + doc.ID)
	}

	return nil
}

func (x *stampedIndex) Delete(context.Context, ...string) error { return nil }

func TestService_SearchIndexing(T *testing.T) {
	T.Parallel()

	T.Run("closes the registry after the pools drain and before the database", func(t *testing.T) {
		t.Parallel()

		// The stamp buffer flushes on an interval nobody will wait out, so the
		// only write it makes is the one Registry.Close forces. Closed before
		// the pool drained, it would miss the stamps still being produced;
		// closed after the database, it would have nothing to write through.
		j := &journal{}
		i := lifecycleInjector(t, j, nil)

		index := &stampedIndex{journal: j}

		registry := searchsync.NewRegistry()
		_, err := searchsync.RegisterIndex(registry, searchsync.IndexSpec[indexedDoc]{
			Name:   "orders",
			Topic:  "orders-index",
			Source: index,
			Target: index,
			Stamp: func(_ context.Context, ids []string) error {
				for _, id := range ids {
					j.record("stamp:" + id)
				}

				return nil
			},
			StampOptions: []batching.Option{batching.WithFlushInterval(time.Hour)},
		})
		must.NoError(t, err)

		payload, err := encoding.EncodeJSON(searchsync.NewEvent(searchsync.OpUpsert, "order-1"))
		must.NoError(t, err)

		do.ProvideValue(i, registry)
		do.ProvideValue(i, poolGroup(t, registry.PoolSpecs(), groupConsumers(j, map[string][][]byte{
			"orders-index": {payload},
		}, nil)))

		svc, err := New(i)
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		errs := make(chan error, 1)
		go func() { errs <- svc.Run(ctx) }()

		waitFor(t, j, "upsert:order-1")
		cancel()

		must.NoError(t, <-errs)

		events := j.all()
		happensBefore(t, events, "stop:orders-index", "stamp:order-1")
		happensBefore(t, events, "stamp:order-1", "close:database")
	})
}

func scheduler(t *testing.T) *jobs.Scheduler {
	t.Helper()

	locker, err := memory.NewLocker()
	must.NoError(t, err)

	sch, err := jobs.NewScheduler(t.Context(), &jobs.SchedulerConfig{}, locker)
	must.NoError(t, err)

	return sch
}

func TestService_ScheduledJobs(T *testing.T) {
	T.Parallel()

	T.Run("registers the application's jobs before the scheduler runs", func(t *testing.T) {
		t.Parallel()

		j := &journal{}
		i := lifecycleInjector(t, j, nil)

		do.ProvideValue(i, scheduler(t))
		do.ProvideValue(i, []jobs.Job{{
			Name:       "reap",
			Interval:   time.Hour,
			RunOnStart: true,
			Run: func(context.Context) error {
				j.record("job:reap")

				return nil
			},
		}})

		svc, err := New(i)
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		errs := make(chan error, 1)
		go func() { errs <- svc.Run(ctx) }()

		waitFor(t, j, "job:reap")
		cancel()

		must.NoError(t, <-errs)

		happensBefore(t, j.all(), "job:reap", "close:database")
	})

	T.Run("jobs with no scheduler to run them fail the startup", func(t *testing.T) {
		t.Parallel()

		i := lifecycleInjector(t, &journal{}, nil)
		do.ProvideValue(i, []jobs.Job{{
			Name:     "reap",
			Interval: time.Hour,
			Run:      func(context.Context) error { return nil },
		}})

		_, err := New(i)
		test.ErrorIs(t, err, ErrScheduledJobsWithoutScheduler)
	})

	T.Run("an empty list needs no scheduler", func(t *testing.T) {
		t.Parallel()

		i := lifecycleInjector(t, &journal{}, nil)
		do.ProvideValue(i, []jobs.Job{})

		_, err := New(i)
		test.NoError(t, err)
	})

	T.Run("a list the scheduler refuses fails the startup", func(t *testing.T) {
		t.Parallel()

		i := lifecycleInjector(t, &journal{}, nil)

		job := jobs.Job{Name: "reap", Interval: time.Hour, Run: func(context.Context) error { return nil }}

		do.ProvideValue(i, scheduler(t))
		do.ProvideValue(i, []jobs.Job{job, job})

		_, err := New(i)
		test.ErrorIs(t, err, jobs.ErrDuplicateJob)
	})
}
