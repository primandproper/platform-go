package service

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	capitalismmock "github.com/primandproper/platform-go/v13/capitalism/mock"
	"github.com/primandproper/platform-go/v13/database"
	databasemock "github.com/primandproper/platform-go/v13/database/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"
	"github.com/primandproper/platform-go/v13/metering"
	meteringcfg "github.com/primandproper/platform-go/v13/metering/config"
	meteringmock "github.com/primandproper/platform-go/v13/metering/mock"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/routing"
	httpserver "github.com/primandproper/platform-go/v13/server/http"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// journal records what happened, in the order it happened. Every fake below
// writes to one, because the ordering is the only thing this package's
// lifecycle actually promises.
type journal struct {
	events []string
	mu     sync.Mutex
}

func (j *journal) record(event string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.events = append(j.events, event)
}

func (j *journal) all() []string {
	j.mu.Lock()
	defer j.mu.Unlock()

	return append([]string(nil), j.events...)
}

// fakeRunner is a background loop with the shape of every real one: Run blocks
// until Close, and Close is safe to call twice.
type fakeRunner struct {
	journal  *journal
	stop     chan struct{}
	done     chan struct{}
	closeErr error
	name     string
	started  atomic.Bool
	once     sync.Once
	block    bool
}

func newFakeRunner(j *journal, name string) *fakeRunner {
	return &fakeRunner{journal: j, name: name, stop: make(chan struct{}), done: make(chan struct{})}
}

func (r *fakeRunner) Run() {
	defer close(r.done)

	r.started.Store(true)
	r.journal.record("run:" + r.name)

	<-r.stop
}

func (r *fakeRunner) Close(ctx context.Context) error {
	r.journal.record("close:" + r.name)

	// The runner that never stops, for the tests about the shutdown budget.
	if r.block {
		<-ctx.Done()

		return ctx.Err()
	}

	r.once.Do(func() { close(r.stop) })

	// The real loops are only ever closed after being run; the tests that build
	// a Service by hand to inspect the shutdown order are not.
	if r.started.Load() {
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return r.closeErr
}

// fakeServer is ingress with the shape of the two real ones: Serve blocks until
// Shutdown, and a graceful close reports nil.
type fakeServer struct {
	journal     *journal
	stop        chan struct{}
	serveErr    error
	shutdownErr error
	name        string
	once        sync.Once
}

func newFakeServer(j *journal, name string) *fakeServer {
	return &fakeServer{journal: j, name: name, stop: make(chan struct{})}
}

func (s *fakeServer) Serve(context.Context) error {
	s.journal.record("serve:" + s.name)

	if s.serveErr != nil {
		return s.serveErr
	}

	<-s.stop

	return nil
}

func (s *fakeServer) Shutdown(context.Context) error {
	s.journal.record("shutdown:" + s.name)
	s.once.Do(func() { close(s.stop) })

	return s.shutdownErr
}

func (s *fakeServer) Router() *routing.Router { return nil }

var _ httpserver.Server = (*fakeServer)(nil)

// fakeProfiler is the one pillar with a start of its own, which makes it the
// cheapest way to drive both ends of the sequence: the failure Run reports
// before anything is serving, and the failure Pillars.Shutdown reports after
// everything has stopped.
type fakeProfiler struct {
	startErr    error
	shutdownErr error
	journal     *journal
}

func (p *fakeProfiler) Start(context.Context) error {
	p.journal.record("start:profiler")

	return p.startErr
}

func (p *fakeProfiler) Shutdown(context.Context) error {
	p.journal.record("shutdown:profiler")

	return p.shutdownErr
}

// lifecycleInjector registers the pieces New resolves through interfaces, which
// is as much of a service as these tests need: clients to release and a server
// to drain.
func lifecycleInjector(t *testing.T, j *journal, srv httpserver.Server) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue(i, &Config{Name: "example", ShutdownTimeout: time.Minute})

	do.ProvideValue[database.Client](i, &databasemock.ClientMock{
		CloseFunc: func() error {
			j.record("close:database")

			return nil
		},
	})

	// The message queue providers are the components whose Close reports
	// nothing at all, so they are also the ones that exercise the adapter for
	// that shape.
	do.ProvideValue[messagequeue.PublisherProvider](i, &messagequeuemock.PublisherProviderMock{
		CloseFunc: func() { j.record("close:publishers") },
	})
	do.ProvideValue[messagequeue.ConsumerProvider](i, &messagequeuemock.ConsumerProviderMock{
		CloseFunc: func() { j.record("close:consumers") },
	})

	if srv != nil {
		do.ProvideValue(i, srv)
	}

	return i
}

func TestService_Shutdown(T *testing.T) {
	T.Parallel()

	T.Run("takes the service down in the documented order", func(t *testing.T) {
		t.Parallel()

		// The whole point of the type. Ingress stops before anything drains,
		// the loops close in reverse of the order they started so each one's
		// last cycle runs after everything feeding it has stopped, the
		// single-shot flushes run once the producers are quiet, and the clients
		// they all needed are released only after that.
		j := &journal{}
		svc := &Service{
			logger:          testLogger(),
			pillars:         &observability.Pillars{},
			shutdownTimeout: time.Minute,
			servers:         []named[Server]{{name: "HTTP server", v: newFakeServer(j, "http")}},
			runners: []named[Runner]{
				{name: "outbox relay", v: newFakeRunner(j, "relay")},
				{name: "webhooks worker", v: newFakeRunner(j, "webhooks")},
			},
			flushes: []named[func(context.Context) error]{
				{name: "metering flusher", v: func(context.Context) error {
					j.record("flush:metering")

					return nil
				}},
			},
			closers: []named[func(context.Context) error]{
				{name: "database client", v: func(context.Context) error {
					j.record("close:database")

					return nil
				}},
				{name: "analytics reporter", v: func(context.Context) error {
					j.record("close:analytics")

					return nil
				}},
			},
		}

		must.NoError(t, svc.Shutdown(t.Context()))

		test.Eq(t, []string{
			"shutdown:http",
			"close:webhooks",
			"close:relay",
			"flush:metering",
			"close:analytics",
			"close:database",
		}, j.all())
	})

	T.Run("reports every failure rather than the first", func(t *testing.T) {
		t.Parallel()

		// A shutdown has nobody to hand an error back to mid-sequence, so
		// stopping at the first one would leave the rest of the process
		// un-drained to report a single symptom.
		j := &journal{}

		srv := newFakeServer(j, "http")
		srv.shutdownErr = platformerrors.New("draining")

		runner := newFakeRunner(j, "relay")
		runner.closeErr = platformerrors.New("relaying")

		errFlush := platformerrors.New("flushing")
		errRelease := platformerrors.New("releasing")

		profiler := &fakeProfiler{journal: j, shutdownErr: platformerrors.New("profiling")}

		svc := &Service{
			logger:          testLogger(),
			pillars:         &observability.Pillars{Profiler: profiler},
			shutdownTimeout: time.Minute,
			servers:         []named[Server]{{name: "HTTP server", v: srv}},
			runners:         []named[Runner]{{name: "outbox relay", v: runner}},
			flushes: []named[func(context.Context) error]{
				{name: "metering flusher", v: func(context.Context) error { return errFlush }},
			},
			closers: []named[func(context.Context) error]{
				{name: "database client", v: func(context.Context) error { return errRelease }},
			},
		}

		err := svc.Shutdown(t.Context())
		must.Error(t, err)
		test.ErrorIs(t, err, srv.shutdownErr)
		test.ErrorIs(t, err, runner.closeErr)
		test.ErrorIs(t, err, errFlush)
		test.ErrorIs(t, err, errRelease)
		test.ErrorIs(t, err, profiler.shutdownErr)

		// Every phase still ran, and the pillars that carried the reports of
		// the other four failures were the last thing to go.
		test.SliceContains(t, j.all(), "shutdown:profiler")
	})

	T.Run("bounds the sequence and keeps going past a loop that will not stop", func(t *testing.T) {
		t.Parallel()

		// The budget is shared, so a loop that overruns it costs the phases
		// after it their time — but not their turn. A client that is never
		// released is a connection the next process cannot have.
		j := &journal{}

		runner := newFakeRunner(j, "wedged")
		runner.block = true

		svc := &Service{
			logger:          testLogger(),
			pillars:         &observability.Pillars{},
			shutdownTimeout: time.Millisecond,
			runners:         []named[Runner]{{name: "wedged runner", v: runner}},
			closers: []named[func(context.Context) error]{
				{name: "database client", v: func(context.Context) error {
					j.record("close:database")

					return nil
				}},
			},
		}

		err := svc.Shutdown(t.Context())
		must.Error(t, err)
		test.ErrorIs(t, err, context.DeadlineExceeded)
		test.SliceContains(t, j.all(), "close:database")
	})

	T.Run("runs once however many times it is called", func(t *testing.T) {
		t.Parallel()

		var closes int

		svc := &Service{
			logger:          testLogger(),
			pillars:         &observability.Pillars{},
			shutdownTimeout: time.Minute,
			closers: []named[func(context.Context) error]{
				{name: "database client", v: func(context.Context) error {
					closes++

					return nil
				}},
			},
		}

		must.NoError(t, svc.Shutdown(t.Context()))
		must.NoError(t, svc.Shutdown(t.Context()))

		test.EqOp(t, 1, closes)
	})
}

func TestService_Run(T *testing.T) {
	T.Parallel()

	T.Run("serves until the context is cancelled, then shuts down", func(t *testing.T) {
		t.Parallel()

		j := &journal{}
		srv := newFakeServer(j, "http")

		i := lifecycleInjector(t, j, srv)

		loop := newFakeRunner(j, "app")

		svc, err := New(i, WithRunners(loop))
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		errs := make(chan error, 1)
		go func() { errs <- svc.Run(ctx) }()

		// Cancelling the parent cancels the context signal.NotifyContext
		// derived from it, which is the same path SIGTERM takes.
		//
		// Both waits are needed. Run launches each loop with a bare `go` and
		// has nothing to wait on, so on a loaded machine the runner goroutine
		// can still be unscheduled when Run returns — and then the run:app
		// assertion below fails on a service that behaved correctly.
		waitFor(t, j, "run:app")
		waitFor(t, j, "serve:http")
		cancel()

		must.NoError(t, <-errs)

		// Ingress is down before anything drains, and the client they both used
		// outlives them.
		//
		// Startup ordering is not asserted the same way, because Run's guarantee
		// there is that it launches the loops before it binds the listeners, not
		// that a loop's first tick beats the first request — a goroutine's start
		// is the scheduler's business, and Runner has no readiness to wait on.
		// That the loop ran at all is asserted, which is what the wait above
		// buys.
		events := j.all()
		test.SliceContains(t, events, "run:app")
		happensBefore(t, events, "shutdown:http", "close:app")
		happensBefore(t, events, "close:app", "close:consumers")
		happensBefore(t, events, "close:consumers", "close:publishers")
		happensBefore(t, events, "close:publishers", "close:database")
	})

	T.Run("a server that stops serving takes the service down with it", func(t *testing.T) {
		t.Parallel()

		// A bound port that is no longer answering is not a state to keep the
		// rest of a process alive for, and the failure has to reach the caller
		// — a process that exits 0 on a bind failure is a deploy that looks
		// like it worked.
		j := &journal{}

		srv := newFakeServer(j, "http")
		srv.serveErr = platformerrors.New("binding")

		svc, err := New(lifecycleInjector(t, j, srv))
		must.NoError(t, err)

		err = svc.Run(t.Context())
		must.Error(t, err)
		test.ErrorIs(t, err, srv.serveErr)

		// And it still took everything else down on its way out.
		test.SliceContains(t, j.all(), "close:database")
	})

	T.Run("a profiler that will not start stops the service before it serves", func(t *testing.T) {
		t.Parallel()

		// The profiler starts first, so its failure is the one startup failure
		// Run can hit after New succeeded — and it still has to take down
		// everything New built rather than leaking it.
		j := &journal{}

		profiler := &fakeProfiler{journal: j, startErr: platformerrors.New("profiling")}

		srv := newFakeServer(j, "http")
		i := lifecycleInjector(t, j, srv)
		do.ProvideValue(i, &observability.Pillars{Profiler: profiler})

		svc, err := New(i)
		must.NoError(t, err)

		err = svc.Run(t.Context())
		must.Error(t, err)
		test.ErrorIs(t, err, profiler.startErr)

		events := j.all()
		test.SliceNotContains(t, events, "serve:http")
		test.SliceContains(t, events, "close:database")
	})

	T.Run("runs the final flush after the loops and before the clients", func(t *testing.T) {
		t.Parallel()

		// The metering flush is the one drain with no loop of its own: it needs
		// every producer above it to have stopped and the database below it to
		// still be open.
		j := &journal{}

		i := lifecycleInjector(t, j, nil)
		do.ProvideValue(i, meteringFlusher(t, j))

		svc, err := New(i)
		must.NoError(t, err)

		must.NoError(t, svc.Shutdown(t.Context()))

		happensBefore(t, j.all(), "flush:metering", "close:database")
	})

	T.Run("a service made of nothing still runs and stops", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Name: "example"})

		svc, err := New(i)
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		errs := make(chan error, 1)
		go func() { errs <- svc.Run(ctx) }()

		cancel()

		must.NoError(t, <-errs)
	})
}

// meteringFlusher builds a real *metering.Flusher over mocked storage and a
// mocked billing provider. Real, because the shutdown's final pass calls Flush
// and a hand-made stand-in would only prove that a function this package wrote
// calls itself.
func meteringFlusher(t *testing.T, j *journal) *metering.Flusher {
	t.Helper()

	store := &meteringmock.StoreMock{
		ClaimFlushableFunc: func(context.Context, time.Time, int, int, time.Time) ([]*metering.Total, error) {
			j.record("flush:metering")

			return nil, nil
		},
		ReapEventsFunc: func(context.Context, time.Time, int) (int64, error) { return 0, nil },
	}

	mapper := metering.ProviderMapperFunc(func(context.Context, string, string) (metering.ProviderRef, error) {
		return metering.ProviderRef{}, nil
	})

	flusher, err := meteringcfg.NewFlusher(t.Context(), &meteringcfg.Config{}, store, mapper, &capitalismmock.UsageReporterMock{})
	must.NoError(t, err)

	return flusher
}

// waitFor blocks until the journal has recorded event, so a test can act on
// something a goroutine had to reach first.
func waitFor(t *testing.T, j *journal, event string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(j.all(), event) {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("timed out waiting for %q, saw %v", event, j.all())
}

// happensBefore asserts that both events happened and that the first one
// happened first.
func happensBefore(t *testing.T, events []string, earlier, later string) {
	t.Helper()

	test.Less(t, indexOf(t, events, later), indexOf(t, events, earlier))
}

// indexOf reports where event landed, failing the test when it never did.
func indexOf(t *testing.T, events []string, event string) int {
	t.Helper()

	for idx, recorded := range events {
		if recorded == event {
			return idx
		}
	}

	t.Fatalf("%q never happened, saw %v", event, events)

	return -1
}

// testLogger is what every hand-built Service in these tests carries. The
// lifecycle's promises are about order, not about what it logged on the way.
func testLogger() logging.Logger {
	return logging.EnsureLogger(nil)
}
