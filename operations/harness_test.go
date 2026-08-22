package operations

import (
	"context"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
)

// fakeStore is a Store the in-package tests drive by hand.
//
// It is written here rather than taken from operations/mock, which the package's
// own tests cannot import without a cycle. It is also more useful than a
// generated mock would be for the two things these tests actually do: hold a
// small set of rows and let a test decide what one call returns.
type fakeStore struct {
	// progressFunc, when set, answers Progress. Everything else reads and
	// writes ops.
	progressFunc func(id string, progress Progress) (Ack, error)

	// getManyErr, when set, is what GetMany returns instead of rows. It is how a
	// watcher test makes a read fail without a database.
	getManyErr error

	// getErr, when set, is what Get returns instead of a row. It is how a test
	// makes the duplicate path's read fail the way a database blinking would.
	getErr error

	ops map[string]*Operation

	// recorded is every Progress the store was handed, in order.
	recorded []Progress

	// finished records the terminal writes, so a worker test can assert on the
	// outcome without re-reading.
	finished []finishCall

	// released records the retry writes.
	released []string

	mu sync.Mutex
}

type finishCall struct {
	result       *Result
	opErr        *Error
	id           string
	state        State
	unitsAllDone bool
}

var _ Store = (*fakeStore)(nil)

func newFakeStore(ops ...*Operation) *fakeStore {
	s := &fakeStore{ops: map[string]*Operation{}}
	for _, op := range ops {
		s.ops[op.ID] = op
	}

	return s
}

// put replaces a row, bumping its revision the way a real write would.
func (s *fakeStore) put(op *Operation) {
	s.mu.Lock()
	defer s.mu.Unlock()

	op.Revision++
	op.Done = op.State.Terminal()

	clone := *op
	s.ops[op.ID] = &clone
}

func (s *fakeStore) snapshot(id string) *Operation {
	s.mu.Lock()
	defer s.mu.Unlock()

	op, ok := s.ops[id]
	if !ok {
		return nil
	}

	clone := *op

	return &clone
}

func (s *fakeStore) recordedProgress() []Progress {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Progress(nil), s.recorded...)
}

func (s *fakeStore) lastProgress() Progress {
	recorded := s.recordedProgress()
	if len(recorded) == 0 {
		return Progress{}
	}

	return recorded[len(recorded)-1]
}

func (s *fakeStore) finishes() []finishCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]finishCall(nil), s.finished...)
}

func (s *fakeStore) releases() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.released...)
}

func (s *fakeStore) Insert(_ context.Context, _ database.SQLQueryExecutor, op *Operation) (*Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.ops[op.ID]; exists {
		return nil, platformerrors.Wrapf(ErrDuplicateOperation, "operation %q", op.ID)
	}

	clone := *op
	clone.Revision = 1
	s.ops[op.ID] = &clone

	inserted := clone

	return &inserted, nil
}

func (s *fakeStore) Get(_ context.Context, id string) (*Operation, error) {
	s.mu.Lock()
	getErr := s.getErr
	s.mu.Unlock()

	if getErr != nil {
		return nil, getErr
	}

	if op := s.snapshot(id); op != nil {
		return op, nil
	}

	return nil, platformerrors.Wrapf(ErrOperationNotFound, "operation %q", id)
}

func (s *fakeStore) GetMany(_ context.Context, ids []string) ([]*Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.getManyErr != nil {
		return nil, s.getManyErr
	}

	out := make([]*Operation, 0, len(ids))

	for _, id := range ids {
		if op, ok := s.ops[id]; ok {
			clone := *op
			out = append(out, &clone)
		}
	}

	return out, nil
}

func (s *fakeStore) List(
	_ context.Context,
	_ *ListScope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Operation], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*Operation, 0, len(s.ops))
	for _, op := range s.ops {
		clone := *op
		out = append(out, &clone)
	}

	return filtering.NewQueryFilteredResult(out, uint64(len(out)), uint64(len(out)),
		func(o *Operation) string { return o.ID }, filter), nil
}

func (s *fakeStore) Begin(_ context.Context, id string, attempts int, _ time.Duration) (*Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	op, ok := s.ops[id]
	if !ok || op.State.Terminal() {
		return nil, platformerrors.Wrapf(ErrOperationNotFound, "operation %q is not claimable", id)
	}

	op.State = StateRunning
	op.Attempts = attempts
	op.Revision++

	clone := *op

	return &clone, nil
}

func (s *fakeStore) Progress(_ context.Context, id string, progress Progress, _ time.Duration) (Ack, error) {
	s.mu.Lock()
	progressFunc := s.progressFunc
	s.recorded = append(s.recorded, progress)
	s.mu.Unlock()

	if progressFunc != nil {
		return progressFunc(id, progress)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	op, ok := s.ops[id]
	if !ok || op.State != StateRunning {
		return Ack{}, nil
	}

	op.Progress = progress
	op.Revision++

	return Ack{Held: true, CancelRequested: op.CancelRequested, Revision: op.Revision}, nil
}

func (s *fakeStore) Finish(
	_ context.Context,
	id string,
	state State,
	result *Result,
	opErr *Error,
	unitsAllDone bool,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.finished = append(s.finished, finishCall{
		id: id, state: state, result: result, opErr: opErr, unitsAllDone: unitsAllDone,
	})

	op, ok := s.ops[id]
	if !ok || op.State.Terminal() {
		return platformerrors.Wrapf(ErrOperationNotFound, "operation %q is no longer active", id)
	}

	op.State = state
	op.Result = result
	op.Error = opErr
	op.Done = true
	op.Revision++

	return nil
}

func (s *fakeStore) Release(_ context.Context, id string, _ *Error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.released = append(s.released, id)

	if op, ok := s.ops[id]; ok {
		op.State = StatePending
		op.Revision++
	}

	return nil
}

func (s *fakeStore) RequestCancel(ctx context.Context, id string) (*Operation, error) {
	s.mu.Lock()

	op, ok := s.ops[id]
	if ok && !op.State.Terminal() {
		op.CancelRequested = true
		op.Revision++

		if op.State == StatePending {
			op.State = StateCancelled
			op.Done = true
		}
	}

	s.mu.Unlock()

	return s.Get(ctx, id)
}

func (s *fakeStore) Stranded(_ context.Context, _ time.Duration, limit int) ([]*Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*Operation, 0, limit)

	for _, op := range s.ops {
		if len(out) >= limit {
			break
		}

		if !op.State.Terminal() {
			clone := *op
			out = append(out, &clone)
		}
	}

	return out, nil
}

func (s *fakeStore) Reap(_ context.Context, _ time.Duration, _ int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var reaped int64

	for id, op := range s.ops {
		if op.State.Terminal() {
			delete(s.ops, id)

			reaped++
		}
	}

	return reaped, nil
}

func (s *fakeStore) WithTransaction(_ context.Context, fn func(q database.SQLQueryExecutor) error) error {
	return fn(nil)
}
