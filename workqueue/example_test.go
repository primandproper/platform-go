package workqueue_test

import (
	"context"
	"log"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/migrate"
	"github.com/primandproper/platform-go/v13/workqueue"
	"github.com/primandproper/platform-go/v13/workqueue/migrations"
)

// Every example here is compiled but not run, and carries a nolint saying so.
// The queue is Postgres-only, so an example with an Output comment would need a
// live server to produce it — which is what the container-backed suite is for.
// These exist to show the shape of the calls, not to verify them.

// A key is whatever names a unit of work in the consumer's domain. It is
// comparable, so its JSON rendering is stable — see workqueue.DefaultKeyCodec.
type tileKey struct {
	Layer string `json:"layer"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
}

// Enqueue, claim, work, complete — the whole loop a worker runs.
//
//nolint:testableexamples // Postgres-only: producing output would need a live server.
func Example() {
	ctx := context.Background()

	var client database.Client // built through database/config, speaking Postgres

	queue, err := workqueue.New[tileKey](ctx, &workqueue.Config{Name: "tiles"}, client)
	if err != nil {
		log.Fatal(err)
	}

	defer func() { _ = queue.Close(ctx) }()

	// Anything can offer work, including a request handler: concurrent enqueues
	// on one process merge into a single statement.
	if err = queue.EnqueueKeys(ctx, tileKey{Layer: "roads", X: 1, Y: 2}); err != nil {
		log.Print(err)

		return
	}

	items, err := queue.Claim(ctx, 100, 30*time.Second)
	if err != nil {
		log.Print(err)

		return
	}

	done := make([]tileKey, 0, len(items))

	for _, item := range items {
		if err = render(ctx, item.Key); err != nil {
			// Hand it back with a delay and a reason. Skipping this is safe
			// too — the lease lapses and the item returns anyway, just later
			// and without the recorded cause.
			_ = queue.Release(ctx, time.Minute, err, item.Key)

			continue
		}

		done = append(done, item.Key)
	}

	if err = queue.Complete(ctx, done...); err != nil {
		log.Print(err)
	}
}

// Priority and delay are how a consumer expresses scheduling policy; the queue
// itself has no opinion about what is urgent or what is stale.
//
//nolint:testableexamples // Postgres-only, as above.
func ExampleQueue_Enqueue() {
	ctx := context.Background()

	var queue *workqueue.Queue[tileKey]

	err := queue.Enqueue(ctx,
		// Somebody asked for this tile and got a stale one, so it jumps the
		// line. Re-enqueueing an item can only raise its priority, so a later
		// quieter caller cannot undo this.
		workqueue.Entry[tileKey]{Key: tileKey{Layer: "roads", X: 1, Y: 2}, Priority: 10},

		// Not worth doing until the upstream feed lands. The delay is measured
		// from the database's clock, not this process's.
		workqueue.Entry[tileKey]{Key: tileKey{Layer: "traffic", X: 1, Y: 2}, Delay: 15 * time.Minute},
	)
	if err != nil {
		log.Print(err)
	}
}

// A worker loop is just claim, work, complete, repeat. Competing claimers do not
// shrink each other's batches — a locked row is skipped and replaced rather than
// counted — so an empty claim is the only signal that there is nothing to do.
//
//nolint:testableexamples // Postgres-only, as above.
func ExampleQueue_Claim() {
	ctx := context.Background()

	var queue *workqueue.Queue[tileKey]

	for {
		items, err := queue.Claim(ctx, 100, 30*time.Second)
		if err != nil {
			log.Print(err)

			return
		}

		if len(items) == 0 {
			time.Sleep(time.Second)

			continue
		}

		for _, item := range items {
			// A reclaimed item is one whose previous holder's lease lapsed. The
			// work may already have been done once, which is why it has to be
			// idempotent.
			if item.Reclaimed {
				log.Printf("retrying %v (attempt %d)", item.Key, item.Attempts)
			}
		}
	}
}

// Reap and Stats are called on a schedule the consumer owns — the jobs package
// is the obvious place — because a component that starts its own timers is one
// that has to be told when to stop.
//
//nolint:testableexamples // Postgres-only, as above.
func ExampleQueue_Stats() {
	ctx := context.Background()

	var queue *workqueue.Queue[tileKey]

	stats, err := queue.Stats(ctx)
	if err != nil {
		log.Print(err)

		return
	}

	// Depth alone cannot tell a queue that is deep and moving from one that is
	// deep and stuck. The age can.
	if stats.OldestReadyAge > time.Hour {
		log.Printf("queue is %d deep and falling behind: oldest ready item is %s old",
			stats.Pending, stats.OldestReadyAge)
	}

	if _, err = queue.Reap(ctx); err != nil {
		log.Print(err)
	}
}

// The table is created by the consumer's own migration run, at a version they
// choose — the platform ships no numbered migration, since the number would
// collide with theirs.
//
//nolint:testableexamples // Postgres-only, as above.
func ExampleQueue_migrations() {
	body, err := migrations.SQL(dialect.Postgres, workqueue.DefaultTablePrefix)
	if err != nil {
		log.Print(err)

		return
	}

	m, err := migrate.New(dialect.Postgres, nil,
		migrate.WithGeneratedMigration(41, "create_work_queue_tables", body),
	)
	if err != nil {
		log.Print(err)

		return
	}

	_ = m
}

func render(context.Context, tileKey) error { return nil }
