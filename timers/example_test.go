package timers_test

import (
	"context"
	"io/fs"
	"log"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/migrate"
	"github.com/primandproper/platform-go/v13/database/postgres/pgnotify"
	"github.com/primandproper/platform-go/v13/timers"
	"github.com/primandproper/platform-go/v13/timers/migrations"
)

// Every example here is compiled but not run, and carries a nolint saying so.
// A timer set is Postgres-only, so an example with an Output comment would need
// a live server to produce it — which is what the container-backed suite is for.
// These exist to show the shape of the calls, not to verify them.

// A key is whatever names a timer in the consumer's domain. It is comparable, so
// its JSON rendering is stable — see timers.DefaultKeyCodec.
type trialID string

// Schedule now, fire later — with the loop this package supplies.
//
//nolint:testableexamples // Postgres-only: producing output would need a live server.
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

// A caller who wants its own loop uses Claim, Complete, Release, and Wait, which
// is what the Worker is built from.
//
//nolint:testableexamples // Postgres-only: producing output would need a live server.
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
//nolint:testableexamples // Postgres-only: producing output would need a live server.
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
//nolint:testableexamples // Postgres-only: producing output would need a live server.
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
