package timers_test

import (
	"context"
	"io/fs"
	"log"
	"time"

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

	// exampleClient is built through database/config, speaking Postgres.
	set, err := timers.New[trialID](ctx, &timers.Config{Name: "trials"}, exampleClient)
	if err != nil {
		log.Fatal(err)
	}

	// The trial starts, so its expiry is written down in the same transaction.
	// It is a row once that commits, which is what makes it survive the deploy
	// that happens next Tuesday.
	err = exampleClient.WithTransaction(ctx, func(tx database.Tx) error {
		if startErr := startTrial(ctx, tx, "trial-9f1c"); startErr != nil {
			return startErr
		}

		return set.ScheduleIn(ctx, tx, "trial-9f1c", 14*24*time.Hour, nil)
	})
	if err != nil {
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

// Schedule joins the caller's transaction, so the trial and the timer that
// expires it are one commit: a rollback leaves no timer firing for a trial that
// was never created, and a commit cannot leave a trial nothing will expire.
//
// A deadlock aborts the whole transaction, so it is the transaction that is
// retried, not the schedule — RetryOnConflict re-runs the callback.
//
//nolint:testableexamples // needs a live database, as above.
func ExampleTimers_Schedule_inTransaction() {
	ctx := context.Background()

	var set *timers.Timers[trialID]

	trial := trialID("trial-9f1c")

	err := database.WithTransaction(ctx, exampleClient, func(tx database.Tx) error {
		if err := startTrial(ctx, tx, trial); err != nil {
			return err
		}

		return set.ScheduleIn(ctx, tx, trial, 14*24*time.Hour, nil)
	}, database.RetryOnConflict(3))
	if err != nil {
		log.Print(err)
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
	err := exampleClient.WithTransaction(ctx, func(tx database.Tx) error {
		return set.ScheduleIn(ctx, tx, "trial-9f1c", 21*24*time.Hour, nil)
	})
	if err != nil {
		log.Print(err)

		return
	}

	// They convert to a paid plan instead, so the expiry is called off in the
	// transaction that records the conversion. A zero here means it had already
	// fired.
	var cancelled int64

	err = exampleClient.WithTransaction(ctx, func(tx database.Tx) error {
		var cancelErr error
		cancelled, cancelErr = set.Cancel(ctx, tx, "trial-9f1c")

		return cancelErr
	})
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
	// notification on the caller's transaction, delivered when it commits.
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
