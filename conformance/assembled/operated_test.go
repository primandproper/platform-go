package assembled_test

import (
	"context"

	"github.com/primandproper/platform-go/v14/operations"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

// operatedKind is the application's own kind of long-running work in this
// harness: work that does nothing, registered so the Operated action has a
// kind operations.Service.Start will accept. Start refuses a kind its registry
// does not hold, so a kind nobody registered is not an option.
const operatedKind = "conformance.operated"

// registerOperatedKind adds operatedKind to the application's registry.
//
// It finishes at once rather than waiting to be cancelled: the suite asserts
// nothing that needs the operation to still be running, and a run that held a
// worker until somebody cancelled it would hold it from the exports the
// dataprivacy suite is waiting on.
func registerOperatedKind(registry *operations.Registry) {
	operations.MustRegister(registry, operations.Definition[struct{}]{
		Kind: operatedKind,
		Run: func(context.Context, struct{}, operations.Reporter) (*operations.Result, error) {
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
