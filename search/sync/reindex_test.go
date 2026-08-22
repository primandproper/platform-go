package searchsync

import (
	"context"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/jobs"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func newTestReindexer(t *testing.T, source Scanner[testDoc], target Target[testDoc], opts ...ReindexOption) *Reindexer[testDoc] {
	t.Helper()

	reindexer, err := NewReindexer("orders", source, target, opts...)
	must.NoError(t, err)

	return reindexer
}

func TestNewReindexer(T *testing.T) {
	T.Parallel()

	T.Run("builds with a name, a source and a target", func(t *testing.T) {
		t.Parallel()

		reindexer, err := NewReindexer("orders", &stubSource{}, &stubTarget{})
		must.NoError(t, err)
		must.NotNil(t, reindexer)
		test.EqOp(t, "orders", reindexer.Name())
		test.EqOp(t, DefaultReindexBatchSize, reindexer.batchSize)
	})

	T.Run("refuses an empty name", func(t *testing.T) {
		t.Parallel()

		_, err := NewReindexer("", &stubSource{}, &stubTarget{})
		test.ErrorIs(t, err, ErrEmptyName)
	})

	T.Run("refuses a nil source", func(t *testing.T) {
		t.Parallel()

		_, err := NewReindexer[testDoc]("orders", nil, &stubTarget{})
		test.ErrorIs(t, err, ErrNilSource)
	})

	T.Run("refuses a nil target", func(t *testing.T) {
		t.Parallel()

		_, err := NewReindexer[testDoc]("orders", &stubSource{}, nil)
		test.ErrorIs(t, err, ErrNilTarget)
	})

	T.Run("ignores a nil pruner", func(t *testing.T) {
		t.Parallel()

		// Taking it would turn "I have no enumeration" into "the index holds
		// nothing", and the rebuild would delete every document in it.
		reindexer := newTestReindexer(t, &stubSource{}, &stubTarget{}, WithReindexPruner(nil))
		test.Nil(t, reindexer.pruner)
	})
}

func TestReindexer_Reindex(T *testing.T) {
	T.Parallel()

	T.Run("upserts every source document", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: pagedDocs("a", "b", "c")}
		target := &stubTarget{}

		result, err := newTestReindexer(t, source, target).Reindex(context.Background())
		must.NoError(t, err)

		test.EqOp(t, int64(3), result.Scanned)
		test.EqOp(t, int64(3), result.Upserted)
		test.EqOp(t, int64(0), result.Pruned)
		test.Eq(t, []string{"a", "b", "c"}, target.upserted)
	})

	T.Run("walks by keyset, resuming after the last ID of each page", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: pagedDocs("a", "b", "c", "d", "e")}
		target := &stubTarget{}

		result, err := newTestReindexer(t, source, target, WithReindexBatchSize(2)).Reindex(context.Background())
		must.NoError(t, err)

		test.EqOp(t, int64(5), result.Scanned)
		test.Eq(t, []string{"", "b", "d"}, source.scanned)
		test.Eq(t, []int{2, 2, 2}, source.scanLimits)
		// Two full batches and the remainder.
		test.EqOp(t, int64(3), result.Batches)
	})

	T.Run("ends the walk on a short page without an extra scan", func(t *testing.T) {
		t.Parallel()

		// Four documents at a batch size of two would need a third, empty page
		// to terminate if a short page were not the signal.
		source := &stubSource{scanFunc: pagedDocs("a", "b", "c")}
		target := &stubTarget{}

		_, err := newTestReindexer(t, source, target, WithReindexBatchSize(2)).Reindex(context.Background())
		must.NoError(t, err)

		test.Eq(t, []string{"", "b"}, source.scanned)
	})

	T.Run("handles an empty source", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: pagedDocs()}
		target := &stubTarget{}

		result, err := newTestReindexer(t, source, target).Reindex(context.Background())
		must.NoError(t, err)

		test.EqOp(t, int64(0), result.Scanned)
		test.EqOp(t, int64(0), result.Batches)
		test.EqOp(t, 0, target.upsertCalls)
	})

	T.Run("does not delete anything without a pruner", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: pagedDocs("a")}
		target := &stubTarget{}

		result, err := newTestReindexer(t, source, target).Reindex(context.Background())
		must.NoError(t, err)

		test.EqOp(t, 0, target.deleteCalls)
		test.EqOp(t, int64(0), result.Pruned)
	})

	T.Run("prunes the documents the source no longer has", func(t *testing.T) {
		t.Parallel()

		// The index is behind on both counts: it is missing b and d, and still
		// holds x and z, whose rows are gone.
		source := &stubSource{scanFunc: pagedDocs("a", "b", "c", "d")}
		pruner := &stubEnumerator{scanFunc: pagedIDs("a", "c", "x", "z")}
		target := &stubTarget{}

		result, err := newTestReindexer(t, source, target, WithReindexPruner(pruner)).Reindex(context.Background())
		must.NoError(t, err)

		test.EqOp(t, int64(4), result.Scanned)
		test.EqOp(t, int64(4), result.Upserted)
		test.EqOp(t, int64(2), result.Pruned)
		test.Eq(t, []string{"a", "b", "c", "d"}, target.upserted)
		test.Eq(t, []string{"x", "z"}, target.deleted)
	})

	T.Run("prunes an index whose source is empty", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: pagedDocs()}
		pruner := &stubEnumerator{scanFunc: pagedIDs("a", "b")}
		target := &stubTarget{}

		result, err := newTestReindexer(t, source, target, WithReindexPruner(pruner)).Reindex(context.Background())
		must.NoError(t, err)

		test.EqOp(t, int64(2), result.Pruned)
		test.Eq(t, []string{"a", "b"}, target.deleted)
	})

	T.Run("prunes nothing when the two sides already agree", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: pagedDocs("a", "b", "c")}
		pruner := &stubEnumerator{scanFunc: pagedIDs("a", "b", "c")}
		target := &stubTarget{}

		result, err := newTestReindexer(t, source, target, WithReindexPruner(pruner)).Reindex(context.Background())
		must.NoError(t, err)

		test.EqOp(t, int64(3), result.Upserted)
		test.EqOp(t, int64(0), result.Pruned)
		test.EqOp(t, 0, target.deleteCalls)
	})

	T.Run("merges across page boundaries on both sides", func(t *testing.T) {
		t.Parallel()

		// A batch size of two puts the merge's interesting moments — a stream
		// running out mid-comparison, a page boundary falling between two IDs
		// that must be compared — in the middle of the walk rather than at its
		// edges.
		source := &stubSource{scanFunc: pagedDocs("a", "c", "e", "g", "i")}
		pruner := &stubEnumerator{scanFunc: pagedIDs("b", "c", "d", "j", "k")}
		target := &stubTarget{}

		result, err := newTestReindexer(t, source, target,
			WithReindexPruner(pruner), WithReindexBatchSize(2)).Reindex(context.Background())
		must.NoError(t, err)

		test.Eq(t, []string{"a", "c", "e", "g", "i"}, target.upserted)
		test.Eq(t, []string{"b", "d", "j", "k"}, target.deleted)
		test.EqOp(t, int64(5), result.Upserted)
		test.EqOp(t, int64(4), result.Pruned)
	})

	T.Run("rejects a source page that is not in ascending byte order", func(t *testing.T) {
		t.Parallel()

		// This is the failure that would otherwise delete live documents: the
		// merge infers "the source has passed this index ID, so its row is
		// gone", and that inference is only true if both sides agree on what
		// passing means. A locale collation that sorts case-insensitively is
		// enough to break it.
		source := &stubSource{scanFunc: func(string, int) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: "b", Body: &testDoc{}}, {ID: "a", Body: &testDoc{}}}, nil
		}}
		target := &stubTarget{}

		_, err := newTestReindexer(t, source, target).Reindex(context.Background())
		test.ErrorIs(t, err, ErrUnsortedScan)
		// The whole page is checked before any of it is applied, so a
		// disordered page cannot be half-written.
		test.EqOp(t, 0, target.upsertCalls)
	})

	T.Run("rejects a repeated ID", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: func(string, int) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: "a", Body: &testDoc{}}, {ID: "a", Body: &testDoc{}}}, nil
		}}

		_, err := newTestReindexer(t, source, &stubTarget{}).Reindex(context.Background())
		test.ErrorIs(t, err, ErrUnsortedScan)
	})

	T.Run("rejects a page that does not advance past the cursor", func(t *testing.T) {
		t.Parallel()

		// A scanner ignoring `after` would page the same rows forever.
		calls := 0
		source := &stubSource{scanFunc: func(string, int) ([]Document[testDoc], error) {
			calls++

			return []Document[testDoc]{{ID: "a", Body: &testDoc{}}, {ID: "b", Body: &testDoc{}}}, nil
		}}

		_, err := newTestReindexer(t, source, &stubTarget{}, WithReindexBatchSize(2)).Reindex(context.Background())
		test.ErrorIs(t, err, ErrUnsortedScan)
		test.EqOp(t, 2, calls)
	})

	T.Run("rejects an index page that is not in ascending byte order", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: pagedDocs("a")}
		pruner := &stubEnumerator{scanFunc: func(string, int) ([]string, error) {
			return []string{"z", "y"}, nil
		}}
		target := &stubTarget{}

		_, err := newTestReindexer(t, source, target, WithReindexPruner(pruner)).Reindex(context.Background())
		test.ErrorIs(t, err, ErrUnsortedScan)
		test.EqOp(t, 0, target.deleteCalls)
	})

	T.Run("rejects a document with no ID", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: func(string, int) ([]Document[testDoc], error) {
			return []Document[testDoc]{{Body: &testDoc{}}}, nil
		}}

		_, err := newTestReindexer(t, source, &stubTarget{}).Reindex(context.Background())
		test.ErrorIs(t, err, ErrEmptyDocumentID)
	})

	T.Run("stops on a scan failure and reports what landed", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("database is down")
		calls := 0
		source := &stubSource{scanFunc: func(after string, limit int) ([]Document[testDoc], error) {
			calls++
			if calls > 1 {
				return nil, expected
			}

			return pagedDocs("a", "b", "c", "d")(after, limit)
		}}
		target := &stubTarget{}

		result, err := newTestReindexer(t, source, target, WithReindexBatchSize(2)).Reindex(context.Background())

		test.ErrorIs(t, err, expected)
		// The first batch landed and is accounted for; the next rebuild starts
		// over from the beginning and repeats it, which is idempotent.
		must.NotNil(t, result)
		test.EqOp(t, int64(2), result.Upserted)
	})

	T.Run("stops on an upsert failure", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("index is down")
		source := &stubSource{scanFunc: pagedDocs("a", "b", "c", "d")}
		target := &stubTarget{upsertFunc: func(...Document[testDoc]) error { return expected }}

		result, err := newTestReindexer(t, source, target, WithReindexBatchSize(2)).Reindex(context.Background())

		test.ErrorIs(t, err, expected)
		test.EqOp(t, int64(0), result.Upserted)
		test.EqOp(t, 1, target.upsertCalls)
	})

	T.Run("stops on a prune failure", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("index is down")
		source := &stubSource{scanFunc: pagedDocs()}
		pruner := &stubEnumerator{scanFunc: pagedIDs("x", "y")}
		target := &stubTarget{deleteFunc: func(...string) error { return expected }}

		result, err := newTestReindexer(t, source, target, WithReindexPruner(pruner)).Reindex(context.Background())

		test.ErrorIs(t, err, expected)
		test.EqOp(t, int64(0), result.Pruned)
	})
}

func TestReindexer_Job(T *testing.T) {
	T.Parallel()

	T.Run("registers under a prefixed, stable name", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{scanFunc: pagedDocs("a")}
		target := &stubTarget{}

		job := newTestReindexer(t, source, target).Job(jobs.MustCron("0 4 * * *"), time.Hour)

		test.EqOp(t, ReindexJobPrefix+"orders", job.Name)
		test.EqOp(t, time.Hour, job.LeaseTTL)
		must.NotNil(t, job.Schedule)
		// The name is the scheduler's lock key, so it has to be a name a
		// Scheduler will actually accept.
		must.NoError(t, job.Run(context.Background()))
		test.Eq(t, []string{"a"}, target.upserted)
	})

	T.Run("surfaces a failed rebuild to the scheduler", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("index is down")
		source := &stubSource{scanFunc: pagedDocs("a")}
		target := &stubTarget{upsertFunc: func(...Document[testDoc]) error { return expected }}

		job := newTestReindexer(t, source, target).Job(jobs.MustCron("0 4 * * *"), time.Hour)
		test.ErrorIs(t, job.Run(context.Background()), expected)
	})
}
