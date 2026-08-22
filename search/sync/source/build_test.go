package syncsource

import (
	"context"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	nooplogging "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	noopmetrics "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	nooptracing "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	searchsync "github.com/primandproper/platform-go/v13/search/sync"
	textsearchmock "github.com/primandproper/platform-go/v13/search/text/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func indexMock() *textsearchmock.IndexMock[exampleDoc] {
	return &textsearchmock.IndexMock[exampleDoc]{
		IndexFunc:  func(context.Context, string, any) error { return nil },
		DeleteFunc: func(context.Context, string) error { return nil },
	}
}

// errBrokenInstrument stands in for a metrics provider that cannot build its
// instruments, which is the one way the searchsync constructors fail after this
// package has already validated everything it can.
var errBrokenInstrument = platformerrors.New("instrument unavailable")

func brokenMetricsProvider() *metricsmock.ProviderMock {
	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			return nil, errBrokenInstrument
		},
	}
}

func indexedIDs(index *textsearchmock.IndexMock[exampleDoc]) []string {
	calls := index.IndexCalls()

	out := make([]string, 0, len(calls))
	for i := range calls {
		out = append(out, calls[i].ID)
	}

	return out
}

func TestNewSyncer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a"))

		syncer, err := NewSyncer(source, indexMock())
		must.NoError(t, err)
		test.EqOp(t, "example", syncer.Name())
	})

	T.Run("takes the pillars as options", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a"))

		syncer, err := NewSyncer(source, indexMock(),
			WithLogger(nooplogging.NewLogger()),
			WithTracerProvider(nooptracing.NewTracerProvider()),
			WithMetricsProvider(noopmetrics.NewMetricsProvider()))
		must.NoError(t, err)
		must.NotNil(t, syncer)
	})

	T.Run("refuses a nil source", func(t *testing.T) {
		t.Parallel()

		syncer, err := NewSyncer[exampleRow, exampleDoc](nil, indexMock())
		test.ErrorIs(t, err, searchsync.ErrNilSource)
		test.Nil(t, syncer)
	})

	T.Run("refuses a nil index, naming the index it was building", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a"))

		syncer, err := NewSyncer(source, nil)
		must.ErrorIs(t, err, searchsync.ErrNilIndex)
		test.StrContains(t, err.Error(), "example")
		test.Nil(t, syncer)
	})

	T.Run("surfaces an instrument that could not be built, naming the index", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a"))

		syncer, err := NewSyncer(source, indexMock(), WithMetricsProvider(brokenMetricsProvider()))
		must.ErrorIs(t, err, errBrokenInstrument)
		test.StrContains(t, err.Error(), "example")
		test.Nil(t, syncer)
	})

	T.Run("applies an upsert through the source", func(t *testing.T) {
		t.Parallel()

		index := indexMock()
		source := sourceForTest(t, newTable("a", "b"))

		syncer, err := NewSyncer(source, index)
		must.NoError(t, err)

		must.NoError(t, syncer.Apply(t.Context(), searchsync.NewEvent(searchsync.OpUpsert, "a")))

		test.Eq(t, []string{"a"}, indexedIDs(index))
		test.SliceEmpty(t, index.DeleteCalls())
	})

	T.Run("applies an upsert of a vanished row as a delete", func(t *testing.T) {
		t.Parallel()

		// The omission Fetch makes is what the Syncer reads as "the row is
		// gone", so this is the two halves meeting.
		index := indexMock()
		source := sourceForTest(t, newTable("a"))

		syncer, err := NewSyncer(source, index)
		must.NoError(t, err)

		must.NoError(t, syncer.Apply(t.Context(), searchsync.NewEvent(searchsync.OpUpsert, "gone")))

		test.SliceEmpty(t, indexedIDs(index))
		must.SliceLen(t, 1, index.DeleteCalls())
		test.EqOp(t, "gone", index.DeleteCalls()[0].ID)
	})

	T.Run("passes syncer options through", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a"))

		// A nil option is dropped by the Syncer rather than panicking, which is
		// what makes a conditionally-built list of them safe to pass.
		syncer, err := NewSyncer(source, indexMock(), WithSyncerOptions(nil))
		must.NoError(t, err)
		must.NotNil(t, syncer)
	})
}

func TestNewReindexer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a"))

		reindexer, err := NewReindexer(source, indexMock())
		must.NoError(t, err)
		test.EqOp(t, "example", reindexer.Name())
	})

	T.Run("refuses a nil source", func(t *testing.T) {
		t.Parallel()

		reindexer, err := NewReindexer[exampleRow, exampleDoc](nil, indexMock())
		test.ErrorIs(t, err, searchsync.ErrNilSource)
		test.Nil(t, reindexer)
	})

	T.Run("refuses a nil index, naming the index it was building", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a"))

		reindexer, err := NewReindexer(source, nil)
		must.ErrorIs(t, err, searchsync.ErrNilIndex)
		test.StrContains(t, err.Error(), "example")
		test.Nil(t, reindexer)
	})

	T.Run("surfaces an instrument that could not be built, naming the index", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a"))

		reindexer, err := NewReindexer(source, indexMock(), WithMetricsProvider(brokenMetricsProvider()))
		must.ErrorIs(t, err, errBrokenInstrument)
		test.StrContains(t, err.Error(), "example")
		test.Nil(t, reindexer)
	})

	T.Run("walks the whole source", func(t *testing.T) {
		t.Parallel()

		index := indexMock()
		tb := newTable("a", "b", "c", "d", "e")
		source := sourceForTest(t, tb)

		reindexer, err := NewReindexer(source, index,
			WithReindexOptions(searchsync.WithReindexBatchSize(2)))
		must.NoError(t, err)

		result, err := reindexer.Reindex(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(5), result.Scanned)
		test.EqOp(t, int64(5), result.Upserted)
		test.Eq(t, tb.order, indexedIDs(index))
	})

	T.Run("does not stop early on a page a vanished row shortened", func(t *testing.T) {
		t.Parallel()

		// This is what the refill in Scan buys, end to end. The Reindexer reads
		// a page shorter than the batch size as the end of the stream, so a
		// Scan that let one deleted row shorten a full page would rebuild two
		// documents out of four and report success.
		index := indexMock()
		tb := newTable("a", "b", "c", "d", "e")
		delete(tb.rows, "b")

		source := sourceForTest(t, tb)

		reindexer, err := NewReindexer(source, index,
			WithReindexOptions(searchsync.WithReindexBatchSize(2)))
		must.NoError(t, err)

		result, err := reindexer.Reindex(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(4), result.Upserted)
		test.Eq(t, []string{"a", "c", "d", "e"}, indexedIDs(index))
	})

	T.Run("aborts a rebuild whose IDs do not ascend", func(t *testing.T) {
		t.Parallel()

		source, err := New("example", newTable("a", "b").fetch,
			func(context.Context, string, int) ([]string, error) {
				return []string{"b", "a"}, nil
			}, convertExample)
		must.NoError(t, err)

		index := indexMock()

		reindexer, err := NewReindexer(source, index)
		must.NoError(t, err)

		result, err := reindexer.Reindex(t.Context())
		must.ErrorIs(t, err, searchsync.ErrUnsortedScan)
		test.EqOp(t, int64(0), result.Upserted)
		test.SliceEmpty(t, indexedIDs(index))
	})
}
