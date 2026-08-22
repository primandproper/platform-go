package searchsync

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func newTestSyncer(t *testing.T, source Fetcher[testDoc], target Target[testDoc], opts ...SyncerOption) *Syncer[testDoc] {
	t.Helper()

	syncer, err := NewSyncer("orders", source, target, opts...)
	must.NoError(t, err)

	return syncer
}

func TestNewSyncer(T *testing.T) {
	T.Parallel()

	T.Run("builds with a name, a source and a target", func(t *testing.T) {
		t.Parallel()

		syncer, err := NewSyncer("orders", &stubSource{}, &stubTarget{})
		must.NoError(t, err)
		must.NotNil(t, syncer)
		test.EqOp(t, "orders", syncer.Name())
	})

	T.Run("refuses an empty name", func(t *testing.T) {
		t.Parallel()

		_, err := NewSyncer("", &stubSource{}, &stubTarget{})
		test.ErrorIs(t, err, ErrEmptyName)
	})

	T.Run("refuses a nil source", func(t *testing.T) {
		t.Parallel()

		_, err := NewSyncer[testDoc]("orders", nil, &stubTarget{})
		test.ErrorIs(t, err, ErrNilSource)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a nil target", func(t *testing.T) {
		t.Parallel()

		_, err := NewSyncer[testDoc]("orders", &stubSource{}, nil)
		test.ErrorIs(t, err, ErrNilTarget)
	})

	T.Run("tolerates a nil option", func(t *testing.T) {
		t.Parallel()

		_, err := NewSyncer("orders", &stubSource{}, &stubTarget{}, nil)
		must.NoError(t, err)
	})
}

func TestSyncer_Apply(T *testing.T) {
	T.Parallel()

	T.Run("reads the document back and indexes it", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}
		target := &stubTarget{}

		must.NoError(t, newTestSyncer(t, source, target).Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))

		must.SliceLen(t, 1, source.fetched)
		test.Eq(t, []string{"doc-1"}, source.fetched[0])
		test.Eq(t, []string{"doc-1"}, target.upserted)
		test.SliceEmpty(t, target.deleted)
	})

	T.Run("deletes without reading anything back", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{}
		target := &stubTarget{}

		must.NoError(t, newTestSyncer(t, source, target).Apply(context.Background(), NewEvent(OpDelete, "doc-1")))

		// There is nothing left in the source to read, so reading would only
		// be a round trip that can fail.
		test.SliceEmpty(t, source.fetched)
		test.Eq(t, []string{"doc-1"}, target.deleted)
	})

	T.Run("applies an upsert as a delete when the row is already gone", func(t *testing.T) {
		t.Parallel()

		// The row was deleted between the event being written and this moment.
		// The source is what the index converges toward, and it says the
		// document is gone — leaving it would strand a document no later event
		// will ever mention again.
		source := &stubSource{fetchFunc: func(...string) ([]Document[testDoc], error) {
			return nil, nil
		}}
		target := &stubTarget{}

		must.NoError(t, newTestSyncer(t, source, target).Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))

		test.SliceEmpty(t, target.upserted)
		test.Eq(t, []string{"doc-1"}, target.deleted)
	})

	T.Run("converges on the current row when events arrive out of order", func(t *testing.T) {
		t.Parallel()

		// Two upserts for one document, applied in the wrong order. Because
		// neither carries a body, both index whatever the row currently is.
		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "current"}}}, nil
		}}
		target := &stubTarget{}
		target.upsertFunc = func(docs ...Document[testDoc]) error {
			test.EqOp(t, "current", docs[0].Body.Name)

			return nil
		}

		syncer := newTestSyncer(t, source, target)

		newer := NewEvent(OpUpsert, "doc-1")
		older := Event{Op: OpUpsert, DocumentID: "doc-1", OccurredAt: newer.OccurredAt.Add(-time.Minute)}

		must.NoError(t, syncer.Apply(context.Background(), newer))
		must.NoError(t, syncer.Apply(context.Background(), older))

		test.Eq(t, []string{"doc-1", "doc-1"}, target.upserted)
	})

	T.Run("refuses an invalid event terminally", func(t *testing.T) {
		t.Parallel()

		target := &stubTarget{}
		err := newTestSyncer(t, &stubSource{}, target).Apply(context.Background(), Event{Op: OpUpsert})

		test.ErrorIs(t, err, ErrInvalidEvent)
		// Unretryable, because it will be just as invalid on redelivery, and
		// each of those attempts is latency the healthy events behind it spend
		// waiting.
		test.ErrorIs(t, err, retry.ErrUnretryable)
		test.EqOp(t, 0, target.upsertCalls)
	})

	T.Run("reports a fetch failure as retryable", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("database is down")
		source := &stubSource{fetchFunc: func(...string) ([]Document[testDoc], error) {
			return nil, expected
		}}
		target := &stubTarget{}

		err := newTestSyncer(t, source, target).Apply(context.Background(), NewEvent(OpUpsert, "doc-1"))

		test.ErrorIs(t, err, expected)
		test.False(t, stderrors.Is(err, retry.ErrUnretryable))
		test.EqOp(t, 0, target.upsertCalls)
	})

	T.Run("reports an index failure", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("index is down")
		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{}}}, nil
		}}
		target := &stubTarget{upsertFunc: func(...Document[testDoc]) error { return expected }}

		test.ErrorIs(t, newTestSyncer(t, source, target).Apply(context.Background(), NewEvent(OpUpsert, "doc-1")), expected)
	})

	T.Run("reports a delete failure", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("index is down")
		target := &stubTarget{deleteFunc: func(...string) error { return expected }}

		test.ErrorIs(t, newTestSyncer(t, &stubSource{}, target).Apply(context.Background(), NewEvent(OpDelete, "doc-1")), expected)
	})

	T.Run("tolerates an event with no occurred-at", func(t *testing.T) {
		t.Parallel()

		// No lag reading, but the event still applies. Recording one would mean
		// reporting a lag measured from the epoch.
		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{}}}, nil
		}}
		target := &stubTarget{}

		must.NoError(t, newTestSyncer(t, source, target).Apply(context.Background(),
			Event{Op: OpUpsert, DocumentID: "doc-1"}))

		test.Eq(t, []string{"doc-1"}, target.upserted)
	})

	T.Run("tolerates an occurred-at in the future", func(t *testing.T) {
		t.Parallel()

		// A writer whose clock runs ahead of this process's. The lag floors at
		// zero rather than going negative — a statement about clock skew does
		// not belong in a histogram wearing the units of latency.
		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{}}}, nil
		}}
		target := &stubTarget{}

		must.NoError(t, newTestSyncer(t, source, target).Apply(context.Background(),
			Event{Op: OpUpsert, DocumentID: "doc-1", OccurredAt: time.Now().UTC().Add(time.Hour)}))

		test.Eq(t, []string{"doc-1"}, target.upserted)
	})
}

func TestSyncer_Handle(T *testing.T) {
	T.Parallel()

	T.Run("applies a relayed event", func(t *testing.T) {
		t.Parallel()

		// The outbox marshals the payload with encoding.EncodeJSON and
		// republishes those exact bytes, so this is what reaches a consumer.
		payload, err := encoding.EncodeJSON(NewEvent(OpUpsert, "doc-1"))
		must.NoError(t, err)

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{}}}, nil
		}}
		target := &stubTarget{}

		must.NoError(t, newTestSyncer(t, source, target).Handle(context.Background(), payload))
		test.Eq(t, []string{"doc-1"}, target.upserted)
	})

	T.Run("round-trips the op through JSON", func(t *testing.T) {
		t.Parallel()

		payload, err := encoding.EncodeJSON(NewEvent(OpDelete, "doc-1"))
		must.NoError(t, err)

		target := &stubTarget{}
		must.NoError(t, newTestSyncer(t, &stubSource{}, target).Handle(context.Background(), payload))
		test.Eq(t, []string{"doc-1"}, target.deleted)
	})

	T.Run("dead-letters an undecodable payload immediately", func(t *testing.T) {
		t.Parallel()

		err := newTestSyncer(t, &stubSource{}, &stubTarget{}).Handle(context.Background(), []byte("{not json"))

		test.Error(t, err)
		test.ErrorIs(t, err, retry.ErrUnretryable)
	})
}
