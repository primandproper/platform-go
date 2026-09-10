package searchsync

import (
	"context"
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testDoc is the indexable body these tests carry around. Nothing in this
// package looks inside it, which is the point of the type parameter.
type testDoc struct {
	Name string
}

// stubSource is a Fetcher and a Scanner that does whatever a test tells it to,
// so the Syncer's and Reindexer's decisions can be driven without a table
// behaving in exactly the awkward way each case needs.
type stubSource struct {
	fetchFunc  func(ids ...string) ([]Document[testDoc], error)
	scanFunc   func(after string, limit int) ([]Document[testDoc], error)
	fetched    [][]string
	scanned    []string
	scanLimits []int
}

var (
	_ Fetcher[testDoc] = (*stubSource)(nil)
	_ Scanner[testDoc] = (*stubSource)(nil)
)

func (s *stubSource) Fetch(_ context.Context, ids ...string) ([]Document[testDoc], error) {
	s.fetched = append(s.fetched, ids)

	if s.fetchFunc == nil {
		return nil, nil
	}

	return s.fetchFunc(ids...)
}

func (s *stubSource) Scan(_ context.Context, after string, limit int) ([]Document[testDoc], error) {
	s.scanned = append(s.scanned, after)
	s.scanLimits = append(s.scanLimits, limit)

	if s.scanFunc == nil {
		return nil, nil
	}

	return s.scanFunc(after, limit)
}

// stubTarget records what reached the index.
type stubTarget struct {
	upsertFunc  func(docs ...Document[testDoc]) error
	deleteFunc  func(ids ...string) error
	upserted    []string
	deleted     []string
	upsertCalls int
	deleteCalls int
}

var _ Target[testDoc] = (*stubTarget)(nil)

func (s *stubTarget) Upsert(_ context.Context, docs ...Document[testDoc]) error {
	s.upsertCalls++

	if s.upsertFunc != nil {
		if err := s.upsertFunc(docs...); err != nil {
			return err
		}
	}

	for _, doc := range docs {
		s.upserted = append(s.upserted, doc.ID)
	}

	return nil
}

func (s *stubTarget) Delete(_ context.Context, ids ...string) error {
	s.deleteCalls++

	if s.deleteFunc != nil {
		if err := s.deleteFunc(ids...); err != nil {
			return err
		}
	}

	s.deleted = append(s.deleted, ids...)

	return nil
}

// stubEnumerator pages a fixed, ordered set of index IDs.
type stubEnumerator struct {
	scanFunc func(after string, limit int) ([]string, error)
	scanned  []string
}

var _ Enumerator = (*stubEnumerator)(nil)

func (s *stubEnumerator) Scan(_ context.Context, after string, limit int) ([]string, error) {
	s.scanned = append(s.scanned, after)

	if s.scanFunc == nil {
		return nil, nil
	}

	return s.scanFunc(after, limit)
}

// pagedDocs turns a sorted slice of IDs into a Scanner function that pages
// them, so a test can state the source as a list and get the keyset walk for
// free.
func pagedDocs(ids ...string) func(after string, limit int) ([]Document[testDoc], error) {
	return func(after string, limit int) ([]Document[testDoc], error) {
		page := make([]Document[testDoc], 0, limit)
		for _, id := range ids {
			if id <= after {
				continue
			}

			page = append(page, Document[testDoc]{ID: id, Body: &testDoc{Name: id}})
			if len(page) == limit {
				break
			}
		}

		return page, nil
	}
}

// pagedIDs is pagedDocs for the index side of a prune.
func pagedIDs(ids ...string) func(after string, limit int) ([]string, error) {
	return func(after string, limit int) ([]string, error) {
		page := make([]string, 0, limit)
		for _, id := range ids {
			if id <= after {
				continue
			}

			page = append(page, id)
			if len(page) == limit {
				break
			}
		}

		return page, nil
	}
}

func TestOp_Valid(T *testing.T) {
	T.Parallel()

	T.Run("accepts the two ops", func(t *testing.T) {
		t.Parallel()

		test.True(t, OpUpsert.Valid())
		test.True(t, OpDelete.Valid())
	})

	T.Run("rejects anything else", func(t *testing.T) {
		t.Parallel()

		test.False(t, Op("").Valid())
		test.False(t, Op("UPSERT").Valid())
		test.False(t, Op("index").Valid())
	})
}

func TestNewEvent(T *testing.T) {
	T.Parallel()

	T.Run("stamps the current instant in UTC", func(t *testing.T) {
		t.Parallel()

		before := time.Now().UTC()
		event := NewEvent(OpUpsert, "doc-1")

		test.EqOp(t, OpUpsert, event.Op)
		test.EqOp(t, "doc-1", event.DocumentID)
		test.False(t, event.OccurredAt.IsZero())
		test.False(t, event.OccurredAt.Before(before))
		test.EqOp(t, time.UTC, event.OccurredAt.Location())
	})
}

func TestEvent_Message(T *testing.T) {
	T.Parallel()

	T.Run("keys the outbox message by document ID", func(t *testing.T) {
		t.Parallel()

		// The key is what buys per-document ordering out of the outbox: at most
		// one message per key is ever in flight, so a document's events cannot
		// overtake one another however many relays are running.
		event := NewEvent(OpUpsert, "doc-1")
		message := event.Message("orders-index")

		test.EqOp(t, "orders-index", message.Topic)
		test.EqOp(t, "doc-1", message.Key)
		payload, ok := message.Payload.(Event)
		must.True(t, ok)
		test.EqOp(t, event, payload)
	})
}

func TestEvent_validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a complete event", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, NewEvent(OpUpsert, "doc-1").validate())
		must.NoError(t, NewEvent(OpDelete, "doc-1").validate())
	})

	T.Run("rejects an event with no document ID", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, Event{Op: OpUpsert}.validate(), ErrInvalidEvent)
	})

	T.Run("rejects an unknown op", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, Event{Op: "reindex", DocumentID: "doc-1"}.validate(), ErrInvalidEvent)
	})

	T.Run("does not require an occurred-at", func(t *testing.T) {
		t.Parallel()

		// An event without one is applicable; it just contributes no lag
		// reading. Refusing it would make the one optional field mandatory.
		must.NoError(t, Event{Op: OpUpsert, DocumentID: "doc-1"}.validate())
	})
}
