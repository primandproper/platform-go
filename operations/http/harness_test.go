package http

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v14/operations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// stubClient is the database.Client the Watcher constructor takes.
//
// It hands back a nil executor, which is all these tests need: every store here
// is a stub that ignores the executor entirely, and what is under test is the
// handler above them.
type stubClient struct{}

var _ database.Client = stubClient{}

func (stubClient) Dialect() dialect.Dialect { return dialect.Postgres }

func (stubClient) Reader() database.SQLQueryExecutor { return nil }

func (stubClient) Writer() database.SQLQueryExecutor { return nil }

func (stubClient) WithTransaction(_ context.Context, fn func(database.Tx) error) error {
	return fn(nil)
}

func (stubClient) Close() error { return nil }

func (stubClient) CurrentTime() time.Time { return time.Now().UTC() }

// stubStore satisfies operations.Store and does nothing. It is what the tests
// that only need a Watcher to exist hand it.
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

// streamingStore holds one operation a test can drive to completion, which is
// all the watch path needs to be exercised end to end through the handler.
type streamingStore struct {
	stubStore

	op *operations.Operation
	mu sync.Mutex
}

func newStreamingStore(id string, owner tenancy.Scope) *streamingStore {
	return &streamingStore{
		op: &operations.Operation{
			ID:       id,
			Owner:    owner,
			Kind:     "export",
			State:    operations.StateRunning,
			Revision: 1,
		},
	}
}

// serviceGet adapts this store's scoped read to the Service shape, which drops
// the executor and keeps the scope.
func (s *streamingStore) serviceGet(
	ctx context.Context,
	scope tenancy.Scope,
	id string,
) (*operations.Operation, error) {
	return s.Get(ctx, nil, scope, id)
}

// finish moves the operation to its terminal state, which is what closes the
// subscription and therefore the stream.
func (s *streamingStore) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.op.State = operations.StateSucceeded
	s.op.Done = true
	s.op.Revision++
}

// Both reads narrow by the scope the way the statements do: an operation in
// another tenant is not a refusal, it is a row the read does not have.
func (s *streamingStore) Get(
	_ context.Context,
	_ database.SQLQueryExecutor,
	scope tenancy.Scope,
	id string,
) (*operations.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id != s.op.ID || scope != s.op.Owner {
		return nil, platformerrors.Wrapf(operations.ErrOperationNotFound, "operation %q", id)
	}

	clone := *s.op

	return &clone, nil
}

func (s *streamingStore) GetMany(
	_ context.Context,
	_ database.SQLQueryExecutor,
	scope tenancy.Scope,
	ids []string,
) ([]*operations.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if scope == s.op.Owner && slices.Contains(ids, s.op.ID) {
		clone := *s.op

		return []*operations.Operation{&clone}, nil
	}

	return nil, nil
}
