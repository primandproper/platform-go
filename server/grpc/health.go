package grpc

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/healthcheck"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// defaultWatchInterval is how often a Watch stream re-runs the checks looking
// for a change. The health protocol has no push side — the registry's checkers
// answer a question, they do not announce anything — so a watcher is a poll, and
// the interval is the resolution a watching load balancer gets.
const defaultWatchInterval = 5 * time.Second

// healthService answers grpc_health_v1 from a healthcheck.Registry, which is
// the same registry the HTTP sibling serves /readyz from.
//
// It is deliberately not grpc-go's own health.Server. That one holds a status
// map an application is expected to keep up to date by calling SetServingStatus,
// which makes the health of a dependency something every caller has to remember
// to report; this one asks the checkers, so the answer cannot drift from what is
// actually true.
type healthService struct {
	grpc_health_v1.UnimplementedHealthServer

	registry healthcheck.Registry
	interval time.Duration
}

// NewHealthService returns the grpc_health_v1 service backed by registry.
//
// It is exported so an application that builds its own *grpc.Server still gets
// the platform's health service, registered the ordinary way with
// grpc_health_v1.RegisterHealthServer. Servers built here get it from
// WithHealthRegistry instead.
//
// The empty service name is the whole process, per the health protocol: it
// reports the registry's aggregate. Any other name is looked up among the
// registered checkers, so a client that cares about one dependency can ask about
// that one by the name it was registered under.
// A nil registry has nothing to check, and therefore reports SERVING for the
// process and SERVICE_UNKNOWN for every name asked of it.
func NewHealthService(registry healthcheck.Registry) grpc_health_v1.HealthServer {
	return &healthService{registry: registry, interval: defaultWatchInterval}
}

// Check reports the current status of the named service, or NotFound if the
// registry has no checker by that name — which is what the protocol asks for,
// and is worth more than a reflexive NOT_SERVING: a client that typoed a
// component name should hear that it did, not that the component is down.
func (h *healthService) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	serving := h.statusFor(ctx, req.GetService())
	if serving == grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN {
		return nil, status.Errorf(codes.NotFound, "unknown service %q", req.GetService())
	}

	return &grpc_health_v1.HealthCheckResponse{Status: serving}, nil
}

// List reports every checker's status in one snapshot, alongside the aggregate
// under the empty name the protocol reserves for the process as a whole.
func (h *healthService) List(ctx context.Context, _ *grpc_health_v1.HealthListRequest) (*grpc_health_v1.HealthListResponse, error) {
	result := healthcheck.Check(ctx, h.registry)

	statuses := make(map[string]*grpc_health_v1.HealthCheckResponse, len(result.Components)+1)
	statuses[""] = &grpc_health_v1.HealthCheckResponse{Status: servingStatus(result.Status)}

	for name := range result.Components {
		statuses[name] = &grpc_health_v1.HealthCheckResponse{Status: servingStatus(result.Components[name].Status)}
	}

	return &grpc_health_v1.HealthListResponse{Statuses: statuses}, nil
}

// Watch streams the named service's status, sending immediately and then again
// on every change until the client goes away.
//
// This is what gRPC's own client-side health checking consumes to pull a backend
// out of a load balancer's pool, so it is worth more than the Unimplemented the
// embedded server would answer with — a client told Unimplemented stops asking
// for the life of the connection.
//
// An unknown service name streams SERVICE_UNKNOWN rather than failing the call,
// because a checker may be registered later; the stream then reports the real
// status without the client having to reconnect.
func (h *healthService) Watch(req *grpc_health_v1.HealthCheckRequest, stream grpc.ServerStreamingServer[grpc_health_v1.HealthCheckResponse]) error {
	ctx := stream.Context()

	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	var (
		last grpc_health_v1.HealthCheckResponse_ServingStatus
		sent bool
	)

	for {
		if serving := h.statusFor(ctx, req.GetService()); !sent || serving != last {
			if err := stream.Send(&grpc_health_v1.HealthCheckResponse{Status: serving}); err != nil {
				return err
			}

			last, sent = serving, true
		}

		select {
		case <-ctx.Done():
			// The client hanging up is how a watch ends, so it is reported as
			// the cancellation it is rather than as a failure of this server.
			return status.FromContextError(ctx.Err()).Err()
		case <-ticker.C:
		}
	}
}

// statusFor runs the checks and reads the answer for one service name, where the
// empty name is the aggregate and an unregistered one is SERVICE_UNKNOWN.
func (h *healthService) statusFor(ctx context.Context, name string) grpc_health_v1.HealthCheckResponse_ServingStatus {
	result := healthcheck.Check(ctx, h.registry)

	if name == "" {
		return servingStatus(result.Status)
	}

	component, ok := result.Components[name]
	if !ok {
		return grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN
	}

	return servingStatus(component.Status)
}

// servingStatus maps a health status onto the protocol's enum. Anything that is
// not affirmatively up is NOT_SERVING: the point of the endpoint is to keep
// traffic away from a process that cannot handle it, and an unrecognized status
// is not evidence that it can.
func servingStatus(s healthcheck.Status) grpc_health_v1.HealthCheckResponse_ServingStatus {
	if s == healthcheck.StatusUp {
		return grpc_health_v1.HealthCheckResponse_SERVING
	}

	return grpc_health_v1.HealthCheckResponse_NOT_SERVING
}
