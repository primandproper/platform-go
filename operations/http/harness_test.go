package http

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/operations"
)

// stubStore satisfies operations.Store and does nothing. It is what the tests
// that only need a Watcher to exist hand it.
type stubStore struct{}

var _ operations.Store = stubStore{}

func (stubStore) Insert(
	context.Context,
	database.SQLQueryExecutor,
	*operations.Operation,
) (*operations.Operation, error) {
	return nil, nil
}

func (stubStore) Get(context.Context, string) (*operations.Operation, error) { return nil, nil }

func (stubStore) GetMany(context.Context, []string) ([]*operations.Operation, error) { return nil, nil }

func (stubStore) List(
	context.Context,
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

func (stubStore) WithTransaction(_ context.Context, fn func(database.SQLQueryExecutor) error) error {
	return fn(nil)
}

// streamingStore holds one operation a test can drive to completion, which is
// all the watch path needs to be exercised end to end through the handler.
type streamingStore struct {
	stubStore

	op *operations.Operation
	mu sync.Mutex
}

func newStreamingStore(id, owner string) *streamingStore {
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

// finish moves the operation to its terminal state, which is what closes the
// subscription and therefore the stream.
func (s *streamingStore) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.op.State = operations.StateSucceeded
	s.op.Done = true
	s.op.Revision++
}

func (s *streamingStore) Get(_ context.Context, id string) (*operations.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id != s.op.ID {
		return nil, platformerrors.Wrapf(operations.ErrOperationNotFound, "operation %q", id)
	}

	clone := *s.op

	return &clone, nil
}

func (s *streamingStore) GetMany(_ context.Context, ids []string) ([]*operations.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if slices.Contains(ids, s.op.ID) {
		clone := *s.op

		return []*operations.Operation{&clone}, nil
	}

	return nil, nil
}
