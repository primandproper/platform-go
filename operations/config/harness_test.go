package operationscfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/operations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// stubStore satisfies operations.Store and does nothing.
//
// The constructors here are wiring: what is worth testing is that they translate
// options and reject what they cannot build, none of which needs a store that
// stores anything. The behavior of a real store is tested where it lives.
type stubStore struct{}

var _ operations.Store = stubStore{}

func (stubStore) Insert(
	context.Context,
	database.Tx,
	tenancy.Scope,
	*operations.Operation,
) (*operations.Operation, error) {
	return nil, nil
}

func (stubStore) Get(
	context.Context,
	database.SQLQueryExecutor,
	tenancy.Scope,
	string,
) (*operations.Operation, error) {
	return nil, nil
}

func (stubStore) GetMany(
	context.Context,
	database.SQLQueryExecutor,
	tenancy.Scope,
	[]string,
) ([]*operations.Operation, error) {
	return nil, nil
}

func (stubStore) List(
	context.Context,
	database.SQLQueryExecutor,
	tenancy.Scope,
	*operations.ListScope,
	*filtering.QueryFilter,
) (*filtering.QueryFilteredResult[operations.Operation], error) {
	return nil, nil
}

func (stubStore) Begin(context.Context, string, int, time.Duration) (*operations.Operation, error) {
	return nil, nil
}

func (stubStore) Progress(
	context.Context,
	string,
	operations.Progress,
	time.Duration,
) (operations.Ack, error) {
	return operations.Ack{}, nil
}

func (stubStore) Finish(
	context.Context,
	string,
	operations.State,
	*operations.Result,
	*operations.Error,
	bool,
) error {
	return nil
}

func (stubStore) Release(context.Context, string, *operations.Error) error { return nil }

func (stubStore) RequestCancel(context.Context, string) (*operations.Operation, error) {
	return nil, nil
}

func (stubStore) Stranded(context.Context, time.Duration, int) ([]*operations.Operation, error) {
	return nil, nil
}

func (stubStore) Reap(context.Context, time.Duration, int) (int64, error) { return 0, nil }
