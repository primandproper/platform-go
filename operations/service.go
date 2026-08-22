package operations

import (
	"context"
	stderrors "errors"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/workqueue"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var _ Service = (*StoreService)(nil)

// StoreService is the Service implementation over a Store and a work queue. It
// is exported, and returned by NewService, so a caller can depend on the service
// it built rather than on the Service seam.
type StoreService struct {
	store    Store
	queue    *workqueue.Queue[string]
	registry *Registry
	o11y     observability.Observer

	startedCounter   metrics.Int64Counter
	cancelledCounter metrics.Int64Counter
	recoveredCounter metrics.Int64Counter
	strandedCounter  metrics.Int64Counter

	cfg Config
}

// NewService builds the application-facing seam over a store, a work queue, and
// the kinds this process knows how to run.
//
// The queue is passed rather than built because a process that starts operations
// almost always runs them too — the service that accepts an export request is
// commonly the one that performs it — and two Queue values over one table would
// mean two of everything a queue carries, including its enqueue batcher, which
// is the part that only pays off when it is shared.
//
// The registry is required even on a process that never runs anything. Start
// encodes the request through the kind's registration and refuses a kind this
// build does not have, which is the check that keeps an unrunnable operation out
// of the table rather than discovering it in a worker an hour later.
func NewService(
	ctx context.Context,
	cfg *Config,
	store Store,
	queue *workqueue.Queue[string],
	registry *Registry,
	opts ...ServiceOption,
) (*StoreService, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if store == nil {
		return nil, ErrNilStore
	}
	if registry == nil {
		return nil, ErrNilRegistry
	}
	if queue == nil {
		return nil, ErrNilQueue
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating operations config")
	}

	// Checked rather than assumed. The service enqueues onto this queue and a
	// worker claims from the one Config.QueueName selects; if they are not the
	// same queue, every operation this service starts is recorded, enqueued, and
	// never run — and the symptom is a table of pending rows with nothing to say
	// why.
	if queue.Name() != cfg.QueueName {
		return nil, platformerrors.Wrapf(ErrInvalidDefinition,
			"work queue is named %q, operations config names %q", queue.Name(), cfg.QueueName)
	}

	o := newServiceOptions(opts)

	s := &StoreService{
		cfg:      *cfg,
		store:    store,
		queue:    queue,
		registry: registry,
		o11y:     observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error

	if s.startedCounter, err = mp.NewInt64Counter(serviceName + "_started"); err != nil {
		return nil, platformerrors.Wrap(err, "creating operations started counter")
	}

	if s.cancelledCounter, err = mp.NewInt64Counter(serviceName + "_cancellations_requested"); err != nil {
		return nil, platformerrors.Wrap(err, "creating operations cancellation counter")
	}

	if s.recoveredCounter, err = mp.NewInt64Counter(serviceName + "_recovered"); err != nil {
		return nil, platformerrors.Wrap(err, "creating operations recovered counter")
	}

	// Separate from recovered, and the more important of the two. Recovered
	// counts rows this sweep re-offered; stranded counts rows it *found*, which
	// is the number that says the gap between Start's two writes is being hit
	// regularly rather than once in a blue moon.
	if s.strandedCounter, err = mp.NewInt64Counter(serviceName + "_stranded"); err != nil {
		return nil, platformerrors.Wrap(err, "creating operations stranded counter")
	}

	return s, nil
}

// kindAttr labels a measurement with the kind it was about. One process commonly
// runs several kinds, and without this their counters collapse into a single
// number in which the kind that has started failing is invisible beside the ones
// that are fine.
func kindAttr(kind string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(kindKey, kind))
}

func (s *StoreService) Start(ctx context.Context, kind string, request any, opts ...StartOption) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(kindKey, kind))
	defer span.End()

	var op *Operation

	// The insert runs in a transaction of the store's own so that Start is
	// atomic even when the caller supplies nothing. It is the same code path
	// StartInTransaction takes, which is what keeps the two from drifting.
	err := s.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		var startErr error
		op, startErr = s.start(ctx, span, q, kind, request, opts)

		return startErr
	})
	if err != nil {
		return nil, err
	}

	s.enqueue(ctx, op, newStartOptions(opts))

	return op, nil
}

func (s *StoreService) StartInTransaction(
	ctx context.Context,
	q database.SQLQueryExecutor,
	kind string,
	request any,
	opts ...StartOption,
) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(kindKey, kind))
	defer span.End()

	if q == nil {
		return nil, span.Error(ErrNilExecutor, "starting operation")
	}

	op, err := s.start(ctx, span, q, kind, request, opts)
	if err != nil {
		return nil, err
	}

	// Deliberately not enqueued here. The caller's transaction has not committed,
	// and an enqueue that lands first offers a worker an operation row that does
	// not exist yet — which the worker's own guarded Begin would refuse, burning
	// an attempt on work that was about to become perfectly valid.
	//
	// The operation is picked up by the recovery sweep instead, within
	// Config.RecoverAfter. A caller that wants it sooner enqueues it themselves
	// after their commit, which is what Enqueue is for.
	return op, nil
}

// start does the shared work of both Starts: resolve the kind, encode the
// request, mint an ID, and insert the row.
func (s *StoreService) start(
	ctx context.Context,
	span observability.Operation,
	q database.SQLQueryExecutor,
	kind string,
	request any,
	opts []StartOption,
) (*Operation, error) {
	bound, err := s.registry.lookup(kind)
	if err != nil {
		return nil, span.Error(err, "starting operation")
	}

	encoded, err := bound.encode(ctx, request)
	if err != nil {
		return nil, span.Error(err, "encoding operation request")
	}

	o := newStartOptions(opts)

	id := o.id
	if id == "" {
		id = identifiers.New()
	}

	op := &Operation{
		ID:      id,
		Kind:    kind,
		State:   StatePending,
		Owner:   o.owner,
		Request: encoded,
		Progress: Progress{
			CountLabel: bound.countLabel,
		},
	}

	span.Set(operationIDKey, op.ID).Set(ownerKey, op.Owner)

	// The inserted row comes back from the write itself rather than from a
	// second read. The write may be inside a transaction nobody has committed —
	// the caller's, or this service's own — and a read on another connection
	// would find nothing at all.
	inserted, err := s.store.Insert(ctx, q, op)
	if err != nil {
		if stderrors.Is(err, ErrDuplicateOperation) {
			// The idempotency seam, arriving. The caller asked for this work
			// under an ID they derived and it is already recorded, so the answer
			// is the operation that already exists rather than a second one
			// doing the same thing.
			//
			// This read can use the ordinary path: a genuine duplicate is a
			// committed row. The one case it cannot see is two Starts racing on
			// the same ID, where the loser reads before the winner commits —
			// which is reported as the duplicate it is rather than invented.
			// The read's own failure is the one reported. Returning the duplicate
			// sentinel here would tell a caller whose database blinked that their
			// operation already exists — an answer they would act on, and a
			// transient failure they would never see.
			existing, getErr := s.store.Get(ctx, op.ID)
			if getErr != nil {
				return nil, span.Error(getErr, "reading existing operation")
			}

			span.Set(stateKey, string(existing.State))

			return existing, nil
		}

		return nil, span.Error(err, "recording operation")
	}

	s.startedCounter.Add(ctx, 1, kindAttr(kind))

	return inserted, nil
}

// enqueue offers the operation's ID to the work queue.
//
// A failure here is logged rather than returned, and the reason is the whole
// design of Recover. The row is durable: the operation exists, is readable, and
// will be picked up by the sweep. Failing Start after the row landed would tell
// the caller their operation does not exist while the table says it does — and
// they would retry, producing a second one.
func (s *StoreService) enqueue(ctx context.Context, op *Operation, o *startOptions) {
	if op == nil || op.State != StatePending {
		return
	}

	err := s.queue.Enqueue(ctx, workqueue.Entry[string]{
		Key:      op.ID,
		Priority: o.priority,
		Delay:    o.delay,
	})
	if err != nil {
		s.o11y.Logger().WithValue(operationIDKey, op.ID).
			Error("enqueuing operation; it will be recovered by the sweep", err)
	}
}

func (s *StoreService) Get(ctx context.Context, id string) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(operationIDKey, id))
	defer span.End()

	op, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, span.Error(err, "reading operation")
	}

	return op, nil
}

func (s *StoreService) List(
	ctx context.Context,
	scope *ListScope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Operation], error) {
	ctx, span := s.o11y.Begin(ctx)
	defer span.End()

	results, err := s.store.List(ctx, scope, filter)
	if err != nil {
		return nil, span.Error(err, "listing operations")
	}

	return results, nil
}

func (s *StoreService) Cancel(ctx context.Context, id string) (*Operation, error) {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(operationIDKey, id))
	defer span.End()

	op, err := s.store.RequestCancel(ctx, id)
	if err != nil {
		return nil, span.Error(err, "cancelling operation")
	}

	span.Set(stateKey, string(op.State))
	s.cancelledCounter.Add(ctx, 1, kindAttr(op.Kind))

	return op, nil
}

func (s *StoreService) Recover(ctx context.Context) (int, error) {
	ctx, span := s.o11y.Begin(ctx)
	defer span.End()

	stranded, err := s.store.Stranded(ctx, s.cfg.RecoverAfter, s.cfg.RecoverBatchSize)
	if err != nil {
		return 0, span.Error(err, "reading stranded operations")
	}

	if len(stranded) == 0 {
		return 0, nil
	}

	span.Set(recoveredKey, len(stranded))
	s.strandedCounter.Add(ctx, int64(len(stranded)))

	entries := make([]workqueue.Entry[string], 0, len(stranded))
	for _, op := range stranded {
		entries = append(entries, workqueue.Entry[string]{Key: op.ID})
	}

	// Re-enqueueing an item that is already queued is harmless: the upsert
	// merges on the key, raises nothing it should not, and a worker that claims
	// an operation somebody else is running is refused by the guarded Begin. So
	// the sweep does not have to establish that an operation is *really* lost —
	// which it could not do anyway, since the queue and the row are two tables
	// and no read spans both consistently.
	if err = s.queue.Enqueue(ctx, entries...); err != nil {
		return 0, span.Error(err, "re-enqueuing stranded operations")
	}

	s.recoveredCounter.Add(ctx, int64(len(entries)))

	// Logged at info, always, when it does anything. A sweep that recovers rows
	// every cycle means processes are dying between Start's two writes, and that
	// is worth somebody noticing rather than leaving to a dashboard nobody has
	// built yet.
	s.o11y.Logger().WithValue(recoveredKey, len(entries)).Info("re-enqueued stranded operations")

	return len(entries), nil
}

func (s *StoreService) Enqueue(ctx context.Context, id string, opts ...StartOption) error {
	ctx, span := s.o11y.Begin(ctx, observability.WithValue(operationIDKey, id))
	defer span.End()

	o := newStartOptions(opts)

	err := s.queue.Enqueue(ctx, workqueue.Entry[string]{
		Key:      id,
		Priority: o.priority,
		Delay:    o.delay,
	})
	if err != nil {
		return span.Error(err, "enqueuing operation")
	}

	return nil
}

func (s *StoreService) Reap(ctx context.Context) (int64, error) {
	ctx, span := s.o11y.Begin(ctx)
	defer span.End()

	reaped, err := s.store.Reap(ctx, s.cfg.Retention, s.cfg.ReapBatchSize)
	if err != nil {
		return 0, span.Error(err, "reaping operations")
	}

	span.Set(rowsAffectedKey, reaped)

	return reaped, nil
}
