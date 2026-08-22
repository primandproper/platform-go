package eventcapture

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

// NewRecorder builds a Recorder over sink. Start it with `go r.Run()` and
// stop it with Close. It returns an error only if the metrics provider cannot
// build the Recorder's instruments.
func NewRecorder[E any](sink Sink, opts ...Option) (*Recorder[E], error) {
	if sink == nil {
		return nil, platformerrors.New("nil sink provided")
	}

	o := &options{
		bufferSize:    DefaultBufferSize,
		flushInterval: DefaultFlushInterval,
		clock:         clock.NewClock(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	r := &Recorder[E]{
		events:          make(chan E, o.bufferSize),
		sink:            sink,
		raw:             !o.noRawRecords,
		flushInterval:   o.flushInterval,
		clock:           o.clock,
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
		overflow:        o.overflow,
		onFlush:         o.onFlush,
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
	}

	// Asserted rather than assumed: Option cannot name E, so this is where a
	// function built for another event type is caught. Dropping it silently
	// would leave a composition that looks wired up and records nothing.
	if o.transform != nil {
		transform, ok := o.transform.(func(*E) any)
		if !ok {
			return nil, platformerrors.Wrapf(
				ErrEventTypeMismatch, "transform is %T, want func(*%T) any", o.transform, *new(E),
			)
		}

		r.transform = transform
	}

	if o.observe != nil {
		observe, ok := o.observe.(func(*E))
		if !ok {
			return nil, platformerrors.Wrapf(
				ErrEventTypeMismatch, "observer is %T, want func(*%T)", o.observe, *new(E),
			)
		}

		r.observe = observe
	}

	r.o11y = observability.NewObserver(serviceName, o.logger, r.tracerProvider)

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	var err error
	if r.writtenCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_records_written", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating records written counter")
	}
	if r.droppedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_records_dropped", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating records dropped counter")
	}
	if r.overflowCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_aggregation_overflow", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating aggregation overflow counter")
	}
	if r.errCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_sink_errors", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating sink error counter")
	}
	if r.flushHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_flush_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating flush latency histogram")
	}

	return r, nil
}

// Record hands one event to the flusher. It never blocks: when the buffer is
// full the event is dropped and counted instead. The event is copied into the
// buffer; the pointer is not retained.
func (r *Recorder[E]) Record(ev *E) {
	select {
	case r.events <- *ev:
	default:
		r.dropped.Add(1)
	}
}

// Dropped reports how many events have been dropped because the buffer was
// full.
func (r *Recorder[E]) Dropped() uint64 {
	return r.dropped.Load()
}

// Run is the flusher loop: it consumes events, ticks the periodic flush, and
// on Close drains the buffer, flushes everything, and closes the sink. Run
// returns only after Close is called.
func (r *Recorder[E]) Run() {
	defer close(r.done)

	// Run deliberately takes no context (see the package documentation), but
	// the instruments need one. Background is the honest choice: the flusher
	// outlives every request whose events it is writing, so there is no
	// caller's context these measurements belong to.
	ctx := context.Background()

	ticker := r.clock.NewTicker(r.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case ev := <-r.events:
			r.consume(ctx, &ev)
		case <-ticker.Chan():
			r.flush(ctx, r.clock.Now(), false)
		case <-r.stop:
			r.drain(ctx)

			return
		}
	}
}

// Close stops the flusher and waits for it to drain buffered events and close
// the sink, up to ctx's deadline. Safe to call more than once.
//
// This is the one traced operation in the package: the drain is a real,
// once-per-process step that a shutdown trace wants accounted for, and a
// deadline hit here means captured events were abandoned.
func (r *Recorder[E]) Close(ctx context.Context) error {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	r.stopOnce.Do(func() { close(r.stop) })

	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return op.Error(ctx.Err(), "draining capture buffer before close")
	}
}

// consume applies one event to the configured paths. Sink errors are logged
// and counted, never surfaced: the request that produced the event has long
// been answered.
func (r *Recorder[E]) consume(ctx context.Context, ev *E) {
	if r.raw {
		record := any(ev)
		if r.transform != nil {
			record = r.transform(ev)
		}
		if record != nil {
			r.write(ctx, record, "writing captured event")
		}
	}

	if r.observe != nil {
		r.observe(ev)
	}
}

// write pushes one record through the sink, counting the outcome either way.
func (r *Recorder[E]) write(ctx context.Context, record any, description string) {
	if err := r.sink.Write(record); err != nil {
		r.errCounter.Add(ctx, 1)
		r.o11y.Logger().Error(description, err)

		return
	}

	r.writtenCounter.Add(ctx, 1)
}

// flush runs the tick hook, reports drop and overflow counters, and flushes
// the sink.
func (r *Recorder[E]) flush(ctx context.Context, now time.Time, final bool) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	defer op.Time(ctx, r.clock, r.flushHist)()

	if r.onFlush != nil {
		r.onFlush(now, final, func(record any) {
			r.write(ctx, record, "writing flush-emitted record")
		})
	}

	// Drops are counted on the hot path with an atomic and reported here, so
	// Record never pays for an instrument call.
	if d := r.dropped.Load(); d > r.loggedDropped {
		delta := d - r.loggedDropped
		r.droppedCounter.Add(ctx, int64(delta))
		r.o11y.Logger().WithValues(map[string]any{"dropped": delta, "total": d}).Info("captured events dropped: buffer full")
		r.loggedDropped = d
	}

	if r.overflow != nil {
		if ov := r.overflow(); ov > 0 {
			r.overflowCounter.Add(ctx, int64(ov))
			r.o11y.Logger().WithValue("overflow", ov).Info("aggregation observations dropped: key bound reached")
		}
	}

	if err := r.sink.Flush(); err != nil {
		r.errCounter.Add(ctx, 1)
		r.o11y.Logger().Error("flushing capture sink", err)
	}
}

// drain empties the channel after stop, then does a final full flush and
// closes the sink. New Record calls racing the drain may still land in the
// buffer and are consumed too; anything sent after the final sweep is dropped
// by the closed sink's error path, not lost silently mid-file.
func (r *Recorder[E]) drain(ctx context.Context) {
	for {
		select {
		case ev := <-r.events:
			r.consume(ctx, &ev)
		default:
			r.flush(ctx, r.clock.Now(), true)
			if err := r.sink.Close(); err != nil {
				r.errCounter.Add(ctx, 1)
				r.o11y.Logger().Error("closing capture sink", err)
			}

			return
		}
	}
}
