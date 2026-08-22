package service

import (
	"context"

	"github.com/primandproper/platform-go/v13/eventcapture"
	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/outbox"
	"github.com/primandproper/platform-go/v13/saga"
	grpcserver "github.com/primandproper/platform-go/v13/server/grpc"
	httpserver "github.com/primandproper/platform-go/v13/server/http"
	"github.com/primandproper/platform-go/v13/webhooks"
)

type (
	// Runner is the lifecycle every background loop in this module already has.
	//
	// It is named here rather than in each package because it was never any one
	// package's decision: the relay, the workers, the pool, the scheduler, the
	// sweeper, and the recorder arrived at the same two methods separately, and
	// the assertions below are the proof that writing the interface down
	// invented nothing.
	//
	// Run taking no context is the load-bearing half. A loop tied to the
	// server's context stops the moment the server does, which is exactly when
	// it still has work to do — the outbox relay would stop draining while the
	// last requests were still committing rows into it. So the owner serves
	// until ingress is down and only then calls Close, which is the order
	// Service.Shutdown encodes.
	Runner interface {
		// Run is the loop. It blocks until Close and is meant to be called on a
		// goroutine of its own.
		Run()

		// Close stops the loop and waits for the cycle in flight to finish, up
		// to ctx's deadline. It is safe to call more than once.
		Close(ctx context.Context) error
	}

	// Server is ingress: something that binds a listener and answers traffic.
	//
	// It is a second interface rather than a Runner because a server genuinely
	// differs. Serve takes a context because binding can block and can fail,
	// and a bind failure is a startup error a background loop has no equivalent
	// of; Shutdown drains what is in flight rather than stopping a cycle.
	Server interface {
		// Serve blocks serving traffic until Shutdown is called or ctx is done.
		// A graceful close reports nil; a bind failure is returned.
		Serve(ctx context.Context) error

		// Shutdown stops accepting and drains what is in flight, up to ctx's
		// deadline.
		Shutdown(ctx context.Context) error
	}
)

// Every background loop and both servers this module registers already satisfy
// the interfaces above, with no changes to any of them. These assertions are
// what keeps that true: a package that changes its loop's shape breaks the
// build here rather than silently dropping out of the shutdown ordering.
//
// eventcapture.Recorder is generic, so Service cannot resolve one from an
// injector — it joins through WithRunners like any application-owned loop. The
// assertion is here anyway, because the convention it satisfies is the same.
// operations.Worker is the exception, and the one this list would have hidden.
// Its Run takes a context and returns an error, so it joins through
// operationsRunner rather than directly — see that type for why neither side is
// wrong about its shape.
var (
	_ Runner = (*eventcapture.Recorder[struct{}])(nil)
	_ Runner = (*jobs.Pool)(nil)
	_ Runner = (*jobs.Scheduler)(nil)
	_ Runner = (*outbox.Relay)(nil)
	_ Runner = (*saga.Worker)(nil)
	_ Runner = (*webhooks.Worker)(nil)

	_ Server = (*grpcserver.Server)(nil)
	_ Server = httpserver.Server(nil)
)

// named pairs a lifecycle participant with the name its failures are reported
// under. Shutdown has no request to attribute a failure to and no caller to
// hand it back to, so the name is the only thing that says which of a dozen
// components gave up.
type named[T any] struct {
	v    T
	name string
}
