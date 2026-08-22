package grpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/healthcheck"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// flakyChecker reports whatever its current error is, which is what makes a
// status change something a test can arrange.
type flakyChecker struct {
	err  error
	name string
	mu   sync.Mutex
}

func (c *flakyChecker) Name() string { return c.name }

func (c *flakyChecker) Check(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.err
}

func (c *flakyChecker) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.err = err
}

// recordingStream is the server side of a Watch, collecting what was sent until
// its context is cancelled.
type recordingStream struct {
	grpc.ServerStream

	ctx  context.Context
	sent chan *grpc_health_v1.HealthCheckResponse
}

func newRecordingStream(ctx context.Context) *recordingStream {
	return &recordingStream{ctx: ctx, sent: make(chan *grpc_health_v1.HealthCheckResponse, 8)}
}

func (s *recordingStream) Context() context.Context { return s.ctx }

func (s *recordingStream) Send(msg *grpc_health_v1.HealthCheckResponse) error {
	select {
	case s.sent <- msg:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// next waits for the stream's next message, failing the test rather than
// hanging if none arrives.
func (s *recordingStream) next(t *testing.T) *grpc_health_v1.HealthCheckResponse {
	t.Helper()

	select {
	case msg := <-s.sent:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a health status")

		return nil
	}
}

func TestHealthService_Check(T *testing.T) {
	T.Parallel()

	T.Run("the empty service name is the whole process", func(t *testing.T) {
		t.Parallel()

		registry := newHealthRegistry(t)
		registry.Register(&flakyChecker{name: "database"})

		res, err := NewHealthService(registry).Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		must.NoError(t, err)
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_SERVING, res.GetStatus())
	})

	T.Run("one component down takes the process down with it", func(t *testing.T) {
		t.Parallel()

		registry := newHealthRegistry(t)
		registry.Register(&flakyChecker{name: "database"})
		registry.Register(&flakyChecker{name: "message_queue", err: errors.New("no broker")})

		res, err := NewHealthService(registry).Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		must.NoError(t, err)
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING, res.GetStatus())
	})

	T.Run("a named component answers for itself", func(t *testing.T) {
		t.Parallel()

		// The aggregate is down, but the component asked about is not — a client
		// watching one dependency should not be told about another's outage.
		registry := newHealthRegistry(t)
		registry.Register(&flakyChecker{name: "database"})
		registry.Register(&flakyChecker{name: "message_queue", err: errors.New("no broker")})

		svc := NewHealthService(registry)

		res, err := svc.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{Service: "database"})
		must.NoError(t, err)
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_SERVING, res.GetStatus())

		res, err = svc.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{Service: "message_queue"})
		must.NoError(t, err)
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING, res.GetStatus())
	})

	T.Run("an unregistered name is NotFound rather than down", func(t *testing.T) {
		t.Parallel()

		// A client that typoed a component name has to hear that it did.
		// NOT_SERVING would read as a real outage of a real dependency.
		_, err := NewHealthService(newHealthRegistry(t)).
			Check(t.Context(), &grpc_health_v1.HealthCheckRequest{Service: "nonexistent"})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("a nil registry checks nothing and serves", func(t *testing.T) {
		t.Parallel()

		res, err := NewHealthService(nil).Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		must.NoError(t, err)
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_SERVING, res.GetStatus())
	})
}

func TestHealthService_List(T *testing.T) {
	T.Parallel()

	T.Run("reports every component alongside the aggregate", func(t *testing.T) {
		t.Parallel()

		registry := newHealthRegistry(t)
		registry.Register(&flakyChecker{name: "database"})
		registry.Register(&flakyChecker{name: "message_queue", err: errors.New("no broker")})

		res, err := NewHealthService(registry).List(t.Context(), &grpc_health_v1.HealthListRequest{})
		must.NoError(t, err)

		statuses := res.GetStatuses()
		must.MapLen(t, 3, statuses)
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING, statuses[""].GetStatus())
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_SERVING, statuses["database"].GetStatus())
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING, statuses["message_queue"].GetStatus())
	})
}

func TestHealthService_Watch(T *testing.T) {
	T.Parallel()

	T.Run("sends immediately, then again on every change", func(t *testing.T) {
		t.Parallel()

		checker := &flakyChecker{name: "database"}

		registry := newHealthRegistry(t)
		registry.Register(checker)

		svc := &healthService{registry: registry, interval: time.Millisecond}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stream := newRecordingStream(ctx)

		done := make(chan error, 1)
		go func() { done <- svc.Watch(&grpc_health_v1.HealthCheckRequest{}, stream) }()

		// The first message is the current status, sent without waiting for the
		// poll interval — a client learns where things stand on connect.
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_SERVING, stream.next(t).GetStatus())

		checker.fail(errors.New("connection refused"))
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING, stream.next(t).GetStatus())

		checker.fail(nil)
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_SERVING, stream.next(t).GetStatus())

		cancel()

		select {
		case err := <-done:
			// The client hanging up ends the watch, and is reported as the
			// cancellation it is.
			test.EqOp(t, codes.Canceled, status.Code(err))
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the watch to end")
		}
	})

	T.Run("an unknown service streams SERVICE_UNKNOWN instead of failing", func(t *testing.T) {
		t.Parallel()

		// The protocol is explicit that a name may become known later, so the
		// call stays open rather than making the client reconnect to find out.
		svc := &healthService{registry: newHealthRegistry(t), interval: time.Millisecond}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stream := newRecordingStream(ctx)

		done := make(chan error, 1)
		go func() { done <- svc.Watch(&grpc_health_v1.HealthCheckRequest{Service: "later"}, stream) }()

		test.EqOp(t, grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN, stream.next(t).GetStatus())

		cancel()
		<-done
	})
}

func TestNewGRPCServer_health(T *testing.T) {
	T.Parallel()

	T.Run("registers the health service when given a registry", func(t *testing.T) {
		t.Parallel()

		srv, err := NewGRPCServer(t.Context(), &Config{}, nil, nil, nil, WithHealthRegistry(newHealthRegistry(t)))
		must.NoError(t, err)

		_, registered := srv.grpcServer.GetServiceInfo()[grpc_health_v1.Health_ServiceDesc.ServiceName]
		test.True(t, registered)
	})

	T.Run("registers nothing without one", func(t *testing.T) {
		t.Parallel()

		// Opt-in, because gRPC panics on a service registered twice and an
		// application may register its own health implementation.
		srv, err := NewGRPCServer(t.Context(), &Config{}, nil, nil, nil)
		must.NoError(t, err)

		_, registered := srv.grpcServer.GetServiceInfo()[grpc_health_v1.Health_ServiceDesc.ServiceName]
		test.False(t, registered)
	})
}

// newHealthRegistry builds an uninstrumented health registry for a test.
func newHealthRegistry(t *testing.T) *healthcheck.CheckerRegistry {
	t.Helper()

	registry, err := healthcheck.NewRegistry()
	must.NoError(t, err)

	return registry
}
