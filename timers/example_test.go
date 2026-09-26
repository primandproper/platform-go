package timers_test

import (
	"context"
	"io/fs"
	"log"
	"time"

	"github.com/primandproper/platform-go/v14/outbox"
	"github.com/primandproper/platform-go/v14/timers"
	"github.com/primandproper/platform-go/v14/timers/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/migrate"
	"github.com/primandproper/primitives-go/v2/database/postgres/pgnotify"
)

// Every example here is compiled but not run, and carries a nolint saying so.
// A timer set needs a database, so an example with an Output comment would need
// a live server to produce it — which is what the container-backed suite is for.
// These exist to show the shape of the calls, not to verify them.

// exampleClient stands in for the database.Client a consumer builds through
// database/config. It is a package-level value because the examples below call
// methods on it rather than only handing it to a constructor, and nothing here
// is run — see the note above.
var exampleClient database.Client

// A key is whatever names a timer in the consumer's domain. It is comparable, so
// its JSON rendering is stable — see timers.DefaultKeyCodec.
type trialID string

// Schedule now, fire later — with the loop this package supplies.
//
//nolint:testableexamples // producing output would need a live database.
func Example() {
	ctx := context.Background()

	var client database.Client // built through database/config, speaking Postgres

	set, err := timers.New[trialID](ctx, &timers.Config{Name: "trials"}, client)
	if err != nil {
		log.Fatal(err)
	}

	// The trial starts, so its expiry is written down. It is a row before this
	// returns, which is what makes it survive the deploy that happens next
	// Tuesday.
	if err = set.ScheduleIn(ctx, "trial-9f1c", 14*24*time.Hour, nil); err != nil {
		log.Print(err)

		return
	}

	worker, err := timers.NewWorker(ctx, &timers.WorkerConfig{}, set,
		func(ctx context.Context, due timers.Due[trialID]) error {
			// Idempotent, always: a lease that lapses while its holder is merely
			// slow hands the same firing to somebody else.
			return expireTrial(ctx, due.Key)
		})
	if err != nil {
		log.Print(err)

		return
	}

	// Run blocks until the context is done.
	if err = worker.Run(ctx); err != nil {
		log.Print(err)
	}
}

// Schedule does not join the caller's transaction, so the subject commits first
// and its timer is written afterwards.
//
//nolint:testableexamples // needs a live database, as above.
func ExampleTimers_Schedule_afterCommit() {
	ctx := context.Background()

	var set *timers.Timers[trialID]

	trial := trialID("trial-9f1c")

	err := exampleClient.WithTransaction(ctx, func(tx database.Tx) error {
		return startTrial(ctx, tx, trial)
	})
	if err != nil {
		log.Print(err)

		return
	}

	// Only now. A Schedule inside that callback would outlive the transaction's
	// rollback and fire for a trial that was never created.
	if err = set.ScheduleIn(ctx, trial, 14*24*time.Hour, nil); err != nil {
		// The trial is committed and now has no expiry, which nothing
		// downstream will notice on its own — hence the route below when that
		// matters.
		log.Print(err)
	}
}

// The route for a schedule that must not be lost: the fact goes into the
// transaction that created the subject, and the timer is written from whatever
// consumes it.
//
//nolint:testableexamples // needs a live database, as above.
func ExampleTimers_Schedule_outbox() {
	ctx := context.Background()

	var (
		writer *outbox.Writer
		set    *timers.Timers[trialID]
	)

	trial := trialID("trial-9f1c")

	// outbox.Writer.Enqueue takes the caller's transaction, so the message
	// lives or dies with the trial row.
	err := exampleClient.WithTransaction(ctx, func(tx database.Tx) error {
		if err := startTrial(ctx, tx, trial); err != nil {
			return err
		}

		return writer.Enqueue(ctx, tx, outbox.Message{Topic: "trials", Payload: trial})
	})
	if err != nil {
		log.Print(err)

		return
	}

	// The consumer schedules, and is retried until the row lands. Scheduling a
	// key that already has a timer for the same instant is not a move, so a
	// redelivered message costs nothing.
	_ = func(ctx context.Context, id trialID, expiry time.Time) error {
		return set.ScheduleAt(ctx, id, expiry, nil)
	}
}

// A caller who wants its own loop uses Claim, Complete, Release, and Wait, which
// is what the Worker is built from.
//
//nolint:testableexamples // producing output would need a live database.
func Example_ownLoop() {
	ctx := context.Background()

	var set *timers.Timers[trialID]

	for {
		due, err := set.Claim(ctx, 20, time.Minute)
		if err != nil {
			log.Print(err)

			return
		}

		fired := make([]timers.Due[trialID], 0, len(due))

		for _, one := range due {
			if err = expireTrial(ctx, one.Key); err != nil {
				// Hand it back with a delay and a reason. Skipping this is safe
				// too — the lease lapses and the firing returns anyway, just
				// later and without the recorded cause.
				_ = set.Release(ctx, time.Minute, err, one)

				continue
			}

			// The whole Due value, not its key: the instant it carries is what
			// stops this retiring a schedule that moved while we were working.
			fired = append(fired, one)
		}

		if err = set.Complete(ctx, fired...); err != nil {
			log.Print(err)
		}

		// Only when there was nothing to do. Wait floors its sleep, so pacing a
		// drain with it would cost a floor per batch.
		if len(due) == 0 {
			if err = set.Wait(ctx, time.Minute); err != nil {
				return
			}
		}
	}
}

// Rescheduling moves a timer, in either direction, and cancelling reports
// whether it beat the firing.
//
//nolint:testableexamples // producing output would need a live database.
func Example_rescheduling() {
	ctx := context.Background()

	var set *timers.Timers[trialID]

	// Support extends the trial by a week. The new instant wins outright — a
	// merge rule that only moved things earlier could not express this.
	if err := set.ScheduleIn(ctx, "trial-9f1c", 21*24*time.Hour, nil); err != nil {
		log.Print(err)

		return
	}

	// They convert to a paid plan instead, so the expiry is called off. A zero
	// here means it had already fired.
	cancelled, err := set.Cancel(ctx, "trial-9f1c")
	if err != nil {
		log.Print(err)

		return
	}

	if cancelled == 0 {
		log.Print("the trial had already expired; reversing it instead")
	}
}

// Without a wakeup, a poller with nothing due for an hour sleeps for an hour —
// so a timer scheduled thirty seconds out, landing a moment later, fires an hour
// late. A notification is only ever the news that a row exists.
//
//nolint:testableexamples // producing output would need a live database.
func Example_wakeup() {
	ctx := context.Background()

	var client database.Client

	listener, err := pgnotify.NewListener(ctx, &pgnotify.Config{
		ConnectionString: "postgres://localhost:5432/app",
		Channel:          "timers",
	})
	if err != nil {
		log.Print(err)

		return
	}

	go listener.Run()

	defer func() { _ = listener.Close(ctx) }()

	// The same channel on both ends: Config.NotifyChannel makes Schedule emit the
	// notification once its rows have landed.
	set, err := timers.New[trialID](ctx, &timers.Config{
		Name:          "trials",
		NotifyChannel: "timers",
	}, client, timers.WithWakeup(listener.Signal()))
	if err != nil {
		log.Print(err)

		return
	}

	_ = set
}

// The table is created by the consumer's own migration run, at a version they
// choose — the platform ships no numbered migration, because the number would
// collide with theirs.
//
//nolint:testableexamples // rendering DDL produces no output worth pinning here.
func ExampleSQL() {
	ddl, err := migrations.SQL(dialect.Postgres, timers.DefaultTablePrefix)
	if err != nil {
		log.Fatal(err)
	}

	var consumerMigrations fs.FS

	m, err := migrate.New(dialect.Postgres, consumerMigrations,
		migrate.WithGeneratedMigration(42, "create_timer_tables", ddl),
	)
	if err != nil {
		log.Fatal(err)
	}

	_ = m
}

func expireTrial(context.Context, trialID) error { return nil }

func startTrial(context.Context, database.Tx, trialID) error { return nil }
