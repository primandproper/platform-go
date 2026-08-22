package searchsync

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/outbox"
)

// serviceName names this package's loggers, spans, and metrics.
const serviceName = "search_sync"

// Observability keys for this package's spans and log fields. Declared once so
// a field set on a span and the same field logged beside it cannot drift, and
// so the search_sync. prefix is applied uniformly — an un-namespaced attribute
// name collides with every other component writing to the same trace. The
// index name is not here: it is keys.IndexNameKey, because a reader correlating
// a sync against a search is correlating on the index, not on this package.
const (
	documentIDKey = "search_sync.document_id"
	opKey         = "search_sync.op"
	lagKey        = "search_sync.lag_ms"
	vanishedKey   = "search_sync.vanished"
	batchSizeKey  = "search_sync.batch_size"
	pruningKey    = "search_sync.pruning"
	scannedKey    = "search_sync.scanned"
	upsertedKey   = "search_sync.upserted"
	prunedKey     = "search_sync.pruned"
	batchesKey    = "search_sync.batches"
)

// Op says what happened to the source row an Event describes.
type Op string

const (
	// OpUpsert says the row was created or updated. The Syncer reads it back
	// and indexes whatever it finds — including nothing, which it applies as a
	// delete.
	OpUpsert Op = "upsert"

	// OpDelete says the row is gone. The Syncer removes the document without
	// reading anything back; there is nothing left to read.
	OpDelete Op = "delete"
)

// Valid reports whether this is an Op the Syncer knows how to apply.
func (o Op) Valid() bool {
	return o == OpUpsert || o == OpDelete
}

// Event is one index-relevant change, written into the outbox by the same
// transaction that changed the row and consumed by a Syncer.
//
// It names a document; it does not carry one. The Syncer reads the row back
// through the Source before indexing, which is what makes the index converge
// regardless of the order events arrive in or how many times each one does —
// see the package documentation.
//
// The wire format is JSON, because the outbox's is: a payload is marshaled at
// Enqueue with encoding.EncodeJSON and republished verbatim inside a
// json.RawMessage.
type Event struct {
	// OccurredAt is when the change happened, stamped by the writing process.
	// It exists for one reason: the difference between it and the instant the
	// Syncer applies the event is the indexing lag, which is the only number
	// that says whether search results are current.
	//
	// It is therefore read across a process boundary, and is only as good as
	// the clock agreement between the writer and the consumer. A consumer whose
	// clock runs behind the writer's reports zero lag rather than a negative
	// one.
	OccurredAt time.Time `json:"occurredAt"`

	// DocumentID identifies the document in both the source and the index. The
	// two must agree — it is the whole basis of "last write wins on the same
	// doc ID", and of the reindex's merge of the two.
	DocumentID string `json:"documentID"`

	// Op says whether the row was written or removed.
	Op Op `json:"op"`
}

// NewEvent stamps an event for documentID at the current instant.
//
// Event's fields are exported and it has no unexported state, so a test that
// needs a specific OccurredAt builds one directly rather than threading a clock
// through a package-level function.
func NewEvent(op Op, documentID string) Event {
	return Event{
		OccurredAt: time.Now().UTC(),
		DocumentID: documentID,
		Op:         op,
	}
}

// Message renders the event as an outbox.Message bound for topic, ready to hand
// to outbox.Writer.Enqueue inside the transaction that made the change:
//
//	err := client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
//	    if err := updateOrder(ctx, q, order); err != nil {
//	        return err
//	    }
//
//	    return writer.Enqueue(ctx, q,
//	        searchsync.NewEvent(searchsync.OpUpsert, order.ID).Message("orders-index"))
//	})
//
// The document ID becomes the outbox message key, which is what buys per-
// document ordering: the outbox admits a keyed message only when no older
// message with that key is still pending, so at most one event per document is
// ever in flight across the whole relay fleet. Events for different documents
// stay free to interleave.
func (e Event) Message(topic string) outbox.Message {
	return outbox.Message{
		Payload: e,
		Topic:   topic,
		Key:     e.DocumentID,
	}
}

// validate reports whether the event can be applied at all. A failure here is
// terminal rather than retryable: a payload missing a document ID will be
// missing it on every redelivery.
func (e Event) validate() error {
	if e.DocumentID == "" {
		return platformerrors.Wrap(ErrInvalidEvent, "empty document ID")
	}

	if !e.Op.Valid() {
		return platformerrors.Wrapf(ErrInvalidEvent, "unknown op %q", e.Op)
	}

	return nil
}

// Document is what the index holds: an ID and a body the application shaped.
//
// The body is the application's business entirely — this package never looks
// inside it. What this package owns is that whatever the Source produces is
// what the index ends up holding.
type Document[T any] struct {
	// Body is the indexable form of the domain object. It is handed to the
	// index as-is.
	Body *T

	// ID must match the DocumentID of the events describing this document, and
	// the IDs an Enumerator reports. It is the key everything here joins on.
	ID string

	// Embedding is the document's vector, and is used only by a vector Target —
	// a text Target ignores it. A vector Target rejects a document without one,
	// because an unembedded vector document is not a document the index can
	// hold.
	Embedding []float32
}

type (
	// Fetcher reads documents back out of the source. It is the application's
	// half of the change feed: this package decides when a document needs
	// indexing, and the Fetcher decides what that document looks like.
	//
	// Fetch is variadic because the reindex path and the change-feed path share
	// it, and they ask for very different numbers of documents at a time — the
	// change feed asks for one per event, a batch loader for many.
	Fetcher[T any] interface {
		// Fetch returns the current document for each of ids, in any order,
		// omitting any whose row no longer exists.
		//
		// Omission is meaningful and must not be an error: it is how the Syncer
		// learns a row was deleted between the event being written and the
		// event being applied, and it removes the document rather than leaving
		// a tombstone in the index.
		Fetch(ctx context.Context, ids ...string) ([]Document[T], error)
	}

	// Scanner walks every document in the source, for a reindex.
	//
	// It is separate from Fetcher because the two paths need different things
	// and most applications implement them against different queries. A type
	// that does both satisfies both.
	Scanner[T any] interface {
		// Scan returns up to limit documents whose IDs sort strictly after
		// `after`, in ascending byte order. An empty `after` starts at the
		// beginning, and a page shorter than limit ends the walk.
		//
		// Ascending *byte* order, as Go's < compares strings, not whatever
		// collation the database defaults to. Postgres's en_US.UTF-8 sorts
		// case-insensitively and ignores punctuation, which is a different
		// order; a keyset walk wants ORDER BY id COLLATE "C". The Reindexer
		// checks the order it is given rather than trusting it, because the
		// pruning half of a reindex compares two ordered streams and a
		// disagreement between their orders would delete live documents.
		Scan(ctx context.Context, after string, limit int) ([]Document[T], error)
	}

	// Enumerator lists the IDs an index currently holds, so a reindex can
	// remove the ones the source no longer has. See WithReindexPruner.
	//
	// It is not part of Target, and no implementation ships here, because
	// neither textsearch.Index nor vectorsearch.Index can enumerate: Algolia
	// browses, Elasticsearch scrolls, pgvector selects, and the narrow
	// upsert/delete/query interfaces those sit behind deliberately do not model
	// any of it. An application that wants pruning has the backend's own client
	// already and is the only party that can say how to walk it.
	Enumerator interface {
		// Scan returns up to limit document IDs sorting strictly after
		// `after`, in ascending byte order — the same contract, and the same
		// collation caveat, as Scanner.Scan.
		Scan(ctx context.Context, after string, limit int) ([]string, error)
	}

	// Target is the index side of the sync, narrowed to the two operations
	// convergence needs. TextTarget and VectorTarget adapt this module's search
	// packages to it; an application indexing into something else implements
	// two methods.
	//
	// Both operations must be idempotent, and both already are for every
	// backend here: an upsert is last-write-wins on the document ID and a
	// delete of an absent document is not an error. That is what lets the whole
	// pipeline run on at-least-once delivery without a deduplication key.
	Target[T any] interface {
		// Upsert inserts or replaces documents, keyed by ID.
		Upsert(ctx context.Context, docs ...Document[T]) error

		// Delete removes documents by ID. IDs the index does not hold are not
		// an error.
		Delete(ctx context.Context, ids ...string) error
	}
)
