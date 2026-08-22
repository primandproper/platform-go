package syncsource_test

import (
	"context"
	"fmt"
	"maps"
	"slices"

	searchsync "github.com/primandproper/platform-go/v13/search/sync"
	syncsource "github.com/primandproper/platform-go/v13/search/sync/source"
)

// orderRepository stands in for the application's repository. Both of the
// functions a Source is built from are already on it — a get-by-ID for the
// change feed, and a keyset walk over IDs for a reindex — and neither exists
// for the search sync's benefit.
type orderRepository struct {
	orders map[string]*order
}

type order struct {
	ID       string
	Customer string
	Status   string
	Internal string
}

// orderDoc is the subset that is actually indexed. It is a different type from
// the row on purpose: the row carries fields nobody searches on.
type orderDoc struct {
	Customer string `json:"customer"`
	Status   string `json:"status"`
}

func convertOrder(o *order) *orderDoc {
	return &orderDoc{Customer: o.Customer, Status: o.Status}
}

func (r *orderRepository) GetOrder(_ context.Context, id string) (*order, error) {
	return r.orders[id], nil
}

func (r *orderRepository) ScanOrderIDsForReindex(_ context.Context, after string, limit int) ([]string, error) {
	// A real one is SELECT id FROM orders WHERE id > $1 ORDER BY id COLLATE "C"
	// LIMIT $2. The collation is not optional — see the package documentation.
	page := make([]string, 0, limit)
	for _, id := range slices.Sorted(maps.Keys(r.orders)) {
		if id > after {
			page = append(page, id)
		}

		if len(page) == limit {
			break
		}
	}

	return page, nil
}

// memoryIndex is a stand-in for Algolia or Elasticsearch, satisfying
// textsearch.IndexManager.
type memoryIndex struct {
	docs map[string]any
}

func (i *memoryIndex) Index(_ context.Context, id string, value any) error {
	i.docs[id] = value

	return nil
}

func (i *memoryIndex) Delete(_ context.Context, id string) error {
	delete(i.docs, id)

	return nil
}

func (i *memoryIndex) Wipe(context.Context) error {
	clear(i.docs)

	return nil
}

// Example wires one entity's index sync: the three repository functions become
// a Source, and the Source becomes both the change feed's consumer and the
// rebuild's walk.
func Example() {
	ctx := context.Background()

	repo := &orderRepository{orders: map[string]*order{
		"order-1": {ID: "order-1", Customer: "ana", Status: "shipped", Internal: "not indexed"},
		"order-2": {ID: "order-2", Customer: "bo", Status: "pending", Internal: "not indexed"},
	}}
	index := &memoryIndex{docs: map[string]any{}}

	source, err := syncsource.New("orders", repo.GetOrder, repo.ScanOrderIDsForReindex, convertOrder)
	if err != nil {
		panic(err)
	}

	// The rebuild: register reindexer.Job with a jobs.Scheduler, whose
	// distributed lock runs it once across the fleet rather than once per
	// replica.
	reindexer, err := syncsource.NewReindexer(source, index)
	if err != nil {
		panic(err)
	}

	result, err := reindexer.Reindex(ctx)
	if err != nil {
		panic(err)
	}

	fmt.Println("reindexed:", result.Upserted)

	// The change feed: syncer.Handle is a jobs.Handler, so a jobs.Pool supplies
	// the consumption, concurrency, retry and dead-lettering around it.
	syncer, err := syncsource.NewSyncer(source, index)
	if err != nil {
		panic(err)
	}

	repo.orders["order-2"].Status = "shipped"
	if err = syncer.Apply(ctx, searchsync.NewEvent(searchsync.OpUpsert, "order-2")); err != nil {
		panic(err)
	}

	// An upsert whose row has since been deleted is applied as a delete: the
	// source is what the index converges toward, and the source says it is
	// gone.
	delete(repo.orders, "order-1")
	if err = syncer.Apply(ctx, searchsync.NewEvent(searchsync.OpUpsert, "order-1")); err != nil {
		panic(err)
	}

	for _, id := range slices.Sorted(maps.Keys(index.docs)) {
		doc, ok := index.docs[id].(*orderDoc)
		if !ok {
			panic("unexpected document type")
		}

		fmt.Printf("%s: %s is %s\n", id, doc.Customer, doc.Status)
	}

	// Output:
	// reindexed: 2
	// order-2: bo is shipped
}
