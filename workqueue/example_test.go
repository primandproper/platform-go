package workqueue_test

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/primandproper/platform-go/v14/outbox"
	"github.com/primandproper/platform-go/v14/workqueue"
	"github.com/primandproper/platform-go/v14/workqueue/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/migrate"
)

// Every example here is compiled but not run, and carries a nolint saying so.
// The queue is Postgres-only, so an example with an Output comment would need a
// live server to produce it — which is what the container-backed suite is for.
// These exist to show the shape of the calls, not to verify them.

// exampleClient stands in for the database.Client a consumer builds through
// database/config. It is a package-level value because the examples below call
// methods on it rather than only handing it to a constructor, and nothing here
// is run — see the note above.
var exampleClient database.Client

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

	// The items themselves rather than their keys: an item is addressed by its
	// key and the claim holding it together, so handing back what Claim gave
	// out is what keeps a lapsed lease from reporting on somebody else's work.
	done := make([]workqueue.Item[tileKey], 0, len(items))

	for _, item := range items {
		if err = render(ctx, item.Key); err != nil {
			// Hand it back with a delay and a reason. Skipping this is safe
			// too — the lease lapses and the item returns anyway, just later
			// and without the recorded cause.
			_ = queue.Release(ctx, time.Minute, err, item)

			continue
		}

		done = append(done, item)
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

// An enqueue never joins the caller's transaction, so the row commits first and
// the key is offered afterwards.
//
//nolint:testableexamples // Postgres-only, as above.
func ExampleQueue_Enqueue_afterCommit() {
	ctx := context.Background()

	var queue *workqueue.Queue[tileKey]

	key := tileKey{Layer: "roads", X: 1, Y: 2}

	// The row, and everything that has to live or die with it.
	err := exampleClient.WithTransaction(ctx, func(tx database.Tx) error {
		return recordTile(ctx, tx, key)
	})
	if err != nil {
		log.Print(err)

		return
	}

	// Only now. An Enqueue inside that callback would leave the key durably
	// queued even when the transaction rolled back, and a worker could claim a
	// name whose row is not there.
	if err = queue.EnqueueKeys(ctx, key); err != nil {
		// The row is committed, so this is not the caller's failure to report:
		// what is lost is the offer, and a sweep over rows with nothing queued
		// is what finds it — operations.Recover is that sweep.
		log.Print(err)
	}
}

// The other route, for work that must not be lost: the intent goes into the
// transaction and something else does the enqueueing.
//
//nolint:testableexamples // Postgres-only, as above.
func ExampleQueue_Enqueue_outbox() {
	ctx := context.Background()

	var (
		writer *outbox.Writer
		queue  *workqueue.Queue[tileKey]
	)

	key := tileKey{Layer: "roads", X: 1, Y: 2}

	// outbox.Writer.Enqueue takes the caller's transaction, so the message
	// lives or dies with the row — which is the half a work queue cannot offer.
	err := exampleClient.WithTransaction(ctx, func(tx database.Tx) error {
		if err := recordTile(ctx, tx, key); err != nil {
			return err
		}

		return writer.Enqueue(ctx, tx, outbox.Message{Topic: "tiles", Payload: key})
	})
	if err != nil {
		log.Print(err)

		return
	}

	// Whatever consumes that topic offers the key to the queue, and is retried
	// until it lands. A rolled-back transaction publishes nothing, so the queue
	// never learns a name that was never written.
	_ = func(ctx context.Context, tile tileKey) error {
		return queue.EnqueueKeys(ctx, tile)
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

// Whichever route enqueued it, a claimed key may name a row that is not there:
// the queue stores a name, and the thing named lives in a table this queue's
// write neither waited for nor rolled back with.
//
//nolint:testableexamples // Postgres-only, as above.
func ExampleQueue_Claim_missingSubject() {
	ctx := context.Background()

	var queue *workqueue.Queue[tileKey]

	items, err := queue.Claim(ctx, 100, 30*time.Second)
	if err != nil {
		log.Print(err)

		return
	}

	done := make([]workqueue.Item[tileKey], 0, len(items))

	for _, item := range items {
		switch err = render(ctx, item.Key); {
		case errors.Is(err, errNoSuchTile):
			// Completed rather than released. Nothing is coming to create the
			// row, so leaving the lease to lapse would only have us claim the
			// key again on the next pass and reach the same conclusion.
			done = append(done, item)

		case err != nil:
			_ = queue.Release(ctx, time.Minute, err, item)

		default:
			done = append(done, item)
		}
	}

	if err = queue.Complete(ctx, done...); err != nil {
		log.Print(err)
	}
}

// The same loop, written by nobody: NewRunner claims, hands each item to the
// handler, completes what worked and hands back what did not — extending the
// leases on running handlers for as long as they run, and draining the batch it
// is holding when the context is cancelled.
//
//nolint:testableexamples // Postgres-only, as above.
func ExampleNewRunner() {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	var queue *workqueue.Queue[tileKey]

	runner, err := workqueue.NewRunner(ctx, &workqueue.RunnerConfig{}, queue,
		func(ctx context.Context, item workqueue.Item[tileKey]) error {
			// Idempotent: a worker that is paused for longer than its lease has
			// its item handed to somebody else, and item.Reclaimed says when
			// that has happened before.
			return render(ctx, item.Key)
		})
	if err != nil {
		log.Print(err)

		return
	}

	// Blocks until ctx is done, then returns its error — after the batch it was
	// holding has been finished and recorded.
	if err = runner.Run(ctx); err != nil {
		log.Print(err)
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

// errNoSuchTile is what a handler returns for a key whose row is not there —
// the ordinary outcome of the two writes an enqueue cannot make one.
var errNoSuchTile = errors.New("no such tile")

func render(context.Context, tileKey) error { return nil }

func recordTile(context.Context, database.Tx, tileKey) error { return nil }
