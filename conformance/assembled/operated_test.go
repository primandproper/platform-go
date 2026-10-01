package assembled_test

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/operations"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

const (
	// operatedKind is the application's own kind of long-running work in this
	// harness: work that waits to be cancelled, registered so the Operated
	// action has a kind operations.Service.Start will accept. Start refuses a
	// kind its registry does not hold, so a kind nobody registered is not an
	// option.
	operatedKind = "conformance.operated"

	// operatedBackstop is how long an operation of operatedKind waits for the
	// cancellation the suite ends on before it finishes on its own, so an
	// assertion that failed before reaching it holds a worker for this long
	// rather than for the rest of the run.
	operatedBackstop = 30 * time.Second
)

// registerOperatedKind adds operatedKind to the application's registry.
//
// It runs until it is cancelled rather than finishing at once, because a
// finished operation is one a cancellation leaves untouched: a refused
// cancellation that leaked through would change nothing on it, and the
// assertion that it changed nothing would pass whatever the surface did. The
// suite cancels it as its owner once it has asserted, which is what hands the
// worker back to the exports the dataprivacy suite is waiting on.
func registerOperatedKind(registry *operations.Registry) {
	operations.MustRegister(registry, operations.Definition[struct{}]{
		Kind: operatedKind,
		Run: func(ctx context.Context, _ struct{}, rep operations.Reporter) (*operations.Result, error) {
			backstop := time.NewTimer(operatedBackstop)
			defer backstop.Stop()

			select {
			case <-rep.Cancelled():
			case <-backstop.C:
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			return nil, nil
		},
	})
}

// operate is the Operated action: an operation of operatedKind, started owned
// by the tenant through the service the composition root built — the end of
// the path a consumer's own start endpoint takes.
func operate(svc operations.Service) func(context.Context, tenancy.Scope) (string, error) {
	return func(ctx context.Context, scope tenancy.Scope) (string, error) {
		op, err := svc.Start(ctx, operatedKind, struct{}{}, operations.WithOwner(scope))
		if err != nil {
			return "", err
		}

		return op.ID, nil
	}
}
