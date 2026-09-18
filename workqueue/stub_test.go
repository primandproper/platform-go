package workqueue

import (
	"context"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v14/workqueue/internal/workqueuedb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"

	"github.com/shoenig/test/must"
)

// The runner's loop and the queue's fence need a server to say anything about
// leases, and the container tests drive them there. What a stubbed querier
// covers is everything above the statement: which calls a pass makes, with which
// items, and in which order — the parts that are decided in Go and would
// otherwise only ever be exercised behind a Docker daemon.

// stubQuerier answers every query in the generated interface, recording the
// arguments it was handed and returning whatever a test set up.
//
// It is hand-written rather than generated because the interface it satisfies is
// itself generated, from an internal package that ships no mock of its own; a
// //go:generate directive here would be a mock of a mock's worth of machinery
// for the six methods these tests actually drive.
type stubQuerier struct {
	claim   func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error)
	extend  func(workqueuedb.ExtendItemsParams) (int64, error)
	release func(workqueuedb.ReleaseItemsParams) (int64, error)

	completed []workqueuedb.CompleteItemsParams
	released  []workqueuedb.ReleaseItemsParams
	extended  []workqueuedb.ExtendItemsParams

	mu sync.Mutex
}

var _ workqueuedb.Querier = (*stubQuerier)(nil)

func (s *stubQuerier) ClaimDueItems(
	_ context.Context,
	_ workqueuedb.DBTX,
	arg workqueuedb.ClaimDueItemsParams,
) ([]workqueuedb.ClaimDueItemsRow, error) {
	s.mu.Lock()
	claim := s.claim
	s.mu.Unlock()

	if claim == nil {
		return nil, nil
	}

	return claim(arg)
}

func (s *stubQuerier) CompleteItems(
	_ context.Context,
	_ workqueuedb.DBTX,
	arg workqueuedb.CompleteItemsParams,
) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.completed = append(s.completed, arg)

	return int64(len(arg.ItemKeys)), nil
}

func (s *stubQuerier) ExtendItems(
	_ context.Context,
	_ workqueuedb.DBTX,
	arg workqueuedb.ExtendItemsParams,
) (int64, error) {
	s.mu.Lock()
	s.extended = append(s.extended, arg)
	extend := s.extend
	s.mu.Unlock()

	if extend != nil {
		return extend(arg)
	}

	return int64(len(arg.ItemKeys)), nil
}

//nolint:gocritic // the signature is the generated interface's, heavy params included.
func (s *stubQuerier) ReleaseItems(
	_ context.Context,
	_ workqueuedb.DBTX,
	arg workqueuedb.ReleaseItemsParams,
) (int64, error) {
	s.mu.Lock()
	s.released = append(s.released, arg)
	release := s.release
	s.mu.Unlock()

	if release != nil {
		return release(arg)
	}

	return int64(len(arg.ItemKeys)), nil
}

func (s *stubQuerier) EnqueueItems(context.Context, workqueuedb.DBTX, workqueuedb.EnqueueItemsParams) error {
	return nil
}

func (s *stubQuerier) ReadQueueStats(
	context.Context,
	workqueuedb.DBTX,
	workqueuedb.ReadQueueStatsParams,
) (workqueuedb.ReadQueueStatsRow, error) {
	return workqueuedb.ReadQueueStatsRow{}, nil
}

func (s *stubQuerier) ReapCompletedItems(
	context.Context,
	workqueuedb.DBTX,
	workqueuedb.ReapCompletedItemsParams,
) (int64, error) {
	return 0, nil
}

func (s *stubQuerier) RemoveItems(context.Context, workqueuedb.DBTX, workqueuedb.RemoveItemsParams) (int64, error) {
	return 0, nil
}

func (s *stubQuerier) RequeueItems(context.Context, workqueuedb.DBTX, workqueuedb.RequeueItemsParams) (int64, error) {
	return 0, nil
}

// calls hands back what the stub recorded, copied under the lock so a caller
// reading it while a pass is still running does not race the pass.
func (s *stubQuerier) calls() (
	completed []workqueuedb.CompleteItemsParams,
	released []workqueuedb.ReleaseItemsParams,
	extended []workqueuedb.ExtendItemsParams,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]workqueuedb.CompleteItemsParams(nil), s.completed...),
		append([]workqueuedb.ReleaseItemsParams(nil), s.released...),
		append([]workqueuedb.ExtendItemsParams(nil), s.extended...)
}

// stubbedClient is a database.Client that reports Postgres and hands back a nil
// executor, which is all a stubbed querier ever looks at.
func stubbedClient() database.Client {
	return &databasemock.ClientMock{
		DialectFunc: func() dialect.Dialect { return dialect.Postgres },
		WriterFunc:  func() database.SQLQueryExecutor { return nil },
		ReaderFunc:  func() database.SQLQueryExecutor { return nil },
	}
}

// stubbedQueue builds a Queue whose statements are answered by the returned
// stub rather than by a database.
func stubbedQueue(t *testing.T) (*Queue[string], *stubQuerier) {
	t.Helper()

	q, err := New[string](t.Context(), validConfig(), stubbedClient())
	must.NoError(t, err)

	t.Cleanup(func() { _ = q.Close(context.WithoutCancel(t.Context())) })

	stub := &stubQuerier{}
	q.q = stub

	return q, stub
}

// claimRows renders keys as the rows a claim would return, at attempt one.
func claimRows(keys ...string) []workqueuedb.ClaimDueItemsRow {
	rows := make([]workqueuedb.ClaimDueItemsRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, workqueuedb.ClaimDueItemsRow{ItemKey: key, Attempts: 1})
	}

	return rows
}
