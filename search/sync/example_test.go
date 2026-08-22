package searchsync_test

import (
	"context"
	"fmt"
	"slices"
	"sort"

	searchsync "github.com/primandproper/platform-go/v13/search/sync"
)

// order is the domain object, and orderDoc is what the index holds. The
// application owns both, and owns the transform between them — this package
// never looks inside either.
type order struct {
	ID       string
	Customer string
	Status   string
}

type orderDoc struct {
	Customer string `json:"customer"`
	Status   string `json:"status"`
}

// orderStore stands in for the table. A real one runs two queries: one that
// loads orders by ID, and one that pages them by ID for a rebuild.
type orderStore struct {
	orders map[string]order
}

func (s *orderStore) document(o order) searchsync.Document[orderDoc] {
	return searchsync.Document[orderDoc]{
		ID:   o.ID,
		Body: &orderDoc{Customer: o.Customer, Status: o.Status},
	}
}

// Fetch is the change feed's half: current documents for the IDs asked about,
// omitting any whose row is gone. The omission is what tells the Syncer to
// remove the document rather than leave a tombstone.
func (s *orderStore) Fetch(_ context.Context, ids ...string) ([]searchsync.Document[orderDoc], error) {
	docs := make([]searchsync.Document[orderDoc], 0, len(ids))
	for _, id := range ids {
		if o, ok := s.orders[id]; ok {
			docs = append(docs, s.document(o))
		}
	}

	return docs, nil
}

// Scan is the rebuild's half: a keyset walk in ascending byte order. Against
// Postgres this is ORDER BY id COLLATE "C" — the default collation sorts
// case-insensitively, which is a different order, and the rebuild's pruning
// half compares this stream against the index's.
func (s *orderStore) Scan(_ context.Context, after string, limit int) ([]searchsync.Document[orderDoc], error) {
	ids := make([]string, 0, len(s.orders))
	for id := range s.orders {
		if id > after {
			ids = append(ids, id)
		}
	}

	sort.Strings(ids)
	ids = ids[:min(len(ids), limit)]

	docs := make([]searchsync.Document[orderDoc], 0, len(ids))
	for _, id := range ids {
		docs = append(docs, s.document(s.orders[id]))
	}

	return docs, nil
}

// memoryIndex is a stand-in for a search backend, and doubles as the
// Enumerator a rebuild prunes with — a real one walks the backend's own
// browse, scroll, or select.
type memoryIndex struct {
	docs map[string]*orderDoc
}

func (i *memoryIndex) Upsert(_ context.Context, docs ...searchsync.Document[orderDoc]) error {
	for _, doc := range docs {
		i.docs[doc.ID] = doc.Body
	}

	return nil
}

func (i *memoryIndex) Delete(_ context.Context, ids ...string) error {
	for _, id := range ids {
		delete(i.docs, id)
	}

	return nil
}

func (i *memoryIndex) Scan(_ context.Context, after string, limit int) ([]string, error) {
	ids := make([]string, 0, len(i.docs))
	for id := range i.docs {
		if id > after {
			ids = append(ids, id)
		}
	}

	sort.Strings(ids)

	return ids[:min(len(ids), limit)], nil
}

func (i *memoryIndex) contents() []string {
	ids := make([]string, 0, len(i.docs))
	for id := range i.docs {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	return ids
}

// Example applies a change feed. In a service the events arrive from a
// jobs.Pool consuming the topic the outbox relayed them to, and Handle is the
// Pool's handler; Apply is the same work with the decoding already done.
func Example() {
	ctx := context.Background()

	store := &orderStore{orders: map[string]order{
		"order-1": {ID: "order-1", Customer: "ada", Status: "placed"},
		"order-2": {ID: "order-2", Customer: "grace", Status: "placed"},
	}}
	index := &memoryIndex{docs: map[string]*orderDoc{}}

	syncer, err := searchsync.NewSyncer[orderDoc]("orders", store, index)
	if err != nil {
		panic(err)
	}

	for _, id := range []string{"order-1", "order-2"} {
		if err = syncer.Apply(ctx, searchsync.NewEvent(searchsync.OpUpsert, id)); err != nil {
			panic(err)
		}
	}

	fmt.Println("indexed:", index.contents())

	// The row changes, and the event says only which document to re-read.
	store.orders["order-1"] = order{ID: "order-1", Customer: "ada", Status: "shipped"}
	if err = syncer.Apply(ctx, searchsync.NewEvent(searchsync.OpUpsert, "order-1")); err != nil {
		panic(err)
	}

	fmt.Println("order-1 is now:", index.docs["order-1"].Status)

	// An upsert whose row has since been deleted applies as a delete: the
	// source is what the index converges toward, and it says the row is gone.
	delete(store.orders, "order-2")
	if err = syncer.Apply(ctx, searchsync.NewEvent(searchsync.OpUpsert, "order-2")); err != nil {
		panic(err)
	}

	fmt.Println("indexed:", index.contents())

	// Output:
	// indexed: [order-1 order-2]
	// order-1 is now: shipped
	// indexed: [order-1]
}

// ExampleReindexer_Reindex rebuilds an index that has drifted in both
// directions: missing a document the source has, and holding one whose row is
// gone. Repairing the second half needs the Enumerator — nothing in this module
// can enumerate a search index, so the walk over the index side is supplied.
func ExampleReindexer_Reindex() {
	ctx := context.Background()

	store := &orderStore{orders: map[string]order{
		"order-1": {ID: "order-1", Customer: "ada", Status: "placed"},
		"order-2": {ID: "order-2", Customer: "grace", Status: "placed"},
	}}

	index := &memoryIndex{docs: map[string]*orderDoc{
		"order-1": {Customer: "ada", Status: "placed"},
		"order-9": {Customer: "gone", Status: "placed"},
	}}

	reindexer, err := searchsync.NewReindexer[orderDoc]("orders", store, index,
		searchsync.WithReindexPruner(index))
	if err != nil {
		panic(err)
	}

	result, err := reindexer.Reindex(ctx)
	if err != nil {
		panic(err)
	}

	fmt.Printf("scanned %d, upserted %d, pruned %d\n", result.Scanned, result.Upserted, result.Pruned)
	fmt.Println("indexed:", index.contents())

	// Output:
	// scanned 2, upserted 2, pruned 1
	// indexed: [order-1 order-2]
}
