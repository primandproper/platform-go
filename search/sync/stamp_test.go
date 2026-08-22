package searchsync

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/batching"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubStamper records what a Syncer said the index accepted.
type stubStamper struct {
	added [][]string
	mu    sync.Mutex
}

var _ Stamper = (*stubStamper)(nil)

func (s *stubStamper) Add(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.added = append(s.added, ids)
}

func (s *stubStamper) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []string
	for _, batch := range s.added {
		out = append(out, batch...)
	}

	return out
}

func TestSyncer_stamp(T *testing.T) {
	T.Parallel()

	T.Run("stamps a document the index accepted", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}
		stamper := &stubStamper{}

		syncer := newTestSyncer(t, source, &stubTarget{}, WithSyncerStamper(stamper))
		must.NoError(t, syncer.Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))

		test.Eq(t, []string{"doc-1"}, stamper.all())
	})

	T.Run("stamps every document a fetch produced", func(t *testing.T) {
		t.Parallel()

		// What the index took is what the column is a statement about, so a
		// Fetcher that expands one changed row into several documents stamps
		// all of them rather than the event's id.
		source := &stubSource{fetchFunc: func(...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{
				{ID: "doc-1a", Body: &testDoc{Name: "first"}},
				{ID: "doc-1b", Body: &testDoc{Name: "second"}},
			}, nil
		}}
		stamper := &stubStamper{}

		syncer := newTestSyncer(t, source, &stubTarget{}, WithSyncerStamper(stamper))
		must.NoError(t, syncer.Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))

		test.Eq(t, []string{"doc-1a", "doc-1b"}, stamper.all())
	})

	T.Run("a delete stamps nothing", func(t *testing.T) {
		t.Parallel()

		stamper := &stubStamper{}

		syncer := newTestSyncer(t, &stubSource{}, &stubTarget{}, WithSyncerStamper(stamper))
		must.NoError(t, syncer.Apply(context.Background(), NewEvent(OpDelete, "doc-1")))

		test.SliceEmpty(t, stamper.all())
	})

	T.Run("a vanished row stamps nothing", func(t *testing.T) {
		t.Parallel()

		// The row went away between the event and this moment, so the upsert
		// was applied as a delete. There is no document to have indexed.
		source := &stubSource{fetchFunc: func(...string) ([]Document[testDoc], error) {
			return nil, nil
		}}
		target := &stubTarget{}
		stamper := &stubStamper{}

		syncer := newTestSyncer(t, source, target, WithSyncerStamper(stamper))
		must.NoError(t, syncer.Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))

		test.Eq(t, []string{"doc-1"}, target.deleted)
		test.SliceEmpty(t, stamper.all())
	})

	T.Run("a failed index write stamps nothing", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}
		target := &stubTarget{upsertFunc: func(...Document[testDoc]) error {
			return platformerrors.New("index unavailable")
		}}
		stamper := &stubStamper{}

		syncer := newTestSyncer(t, source, target, WithSyncerStamper(stamper))
		test.Error(t, syncer.Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))

		test.SliceEmpty(t, stamper.all())
	})

	T.Run("without a stamper nothing is recorded and nothing panics", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}

		syncer := newTestSyncer(t, source, &stubTarget{})
		must.NoError(t, syncer.Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))
	})

	T.Run("a nil stamper is ignored rather than stored", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}

		syncer := newTestSyncer(t, source, &stubTarget{}, WithSyncerStamper(nil))
		must.Nil(t, syncer.stamper)
		must.NoError(t, syncer.Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))
	})
}

func TestNewStampBuffer(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil write function", func(t *testing.T) {
		t.Parallel()

		_, err := NewStampBuffer(nil)
		test.ErrorIs(t, err, batching.ErrNilWriteFunc)
	})

	T.Run("flushes the whole coalesced set through one call", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var batches [][]string

			buffer, err := NewStampBuffer(func(_ context.Context, ids []string) error {
				batches = append(batches, ids)

				return nil
			}, batching.WithFlushInterval(time.Second))
			must.NoError(t, err)

			// The same document indexed three times is one row to stamp: the
			// collapse is what the buffer is for.
			buffer.Add("doc-2")
			buffer.Add("doc-1")
			buffer.Add("doc-2")

			time.Sleep(2 * time.Second)
			synctest.Wait()

			must.NoError(t, buffer.Close(context.Background()))

			must.SliceLen(t, 1, batches)
			test.Eq(t, []string{"doc-1", "doc-2"}, batches[0])
		})
	})

	T.Run("pins the id ordering against an opts slice that would unset it", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var flushed []string

			// Ordering is the half that keeps the buffered write queueing
			// behind other writers of those rows rather than deadlocking
			// against them, so it is not a caller's to override.
			buffer, err := NewStampBuffer(func(_ context.Context, ids []string) error {
				flushed = ids

				return nil
			}, batching.WithOrder(func(a, b string) int { return strings.Compare(b, a) }))
			must.NoError(t, err)

			buffer.Add("doc-3", "doc-1", "doc-2")

			must.NoError(t, buffer.Close(context.Background()))

			test.Eq(t, []string{"doc-1", "doc-2", "doc-3"}, flushed)
		})
	})

	T.Run("satisfies the seam a Syncer stamps through", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var flushed []string

			buffer, err := NewStampBuffer(func(_ context.Context, ids []string) error {
				flushed = append(flushed, ids...)

				return nil
			})
			must.NoError(t, err)

			source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
				return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
			}}

			syncer := newTestSyncer(t, source, &stubTarget{}, WithSyncerStamper(buffer))
			must.NoError(t, syncer.Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))

			must.NoError(t, buffer.Close(context.Background()))

			test.Eq(t, []string{"doc-1"}, flushed)
		})
	})
}
