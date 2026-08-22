package jobs_test

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/distributedlock/memory"
	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/retry"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"
)

// ExampleNewPool consumes a topic with a bounded set of workers, retrying a
// failing message and dead-lettering it once it has run out of attempts.
func ExampleNewPool() {
	ctx := context.Background()

	// A real service passes the ConsumerProvider it built at startup — kafka,
	// pubsub, redis, or sqs. This one is this package's test double.
	queue := newFakeQueue()

	handled := make(chan string, 1)
	deadLetters := make(chan jobs.DeadLetter, 1)

	pool, err := jobs.NewPool(ctx, &jobs.PoolConfig{
		Topic:       "orders",
		Concurrency: 4,
		Retry:       retrycfg.Config{MaxAttempts: 2, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}, queue.provider, func(_ context.Context, payload []byte) error {
		if string(payload) == "malformed" {
			// Nothing will make this parse on a second attempt, so skip the
			// remaining ones and go straight to the dead-letter path.
			return retry.Unretryable(fmt.Errorf("cannot decode order"))
		}

		handled <- string(payload)

		return nil
	}, jobs.WithPoolDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
		deadLetters <- msg

		return nil
	}))
	if err != nil {
		panic(err)
	}

	go pool.Run()

	queue.publish("order-1")
	fmt.Println("handled:", <-handled)

	queue.publish("malformed")

	dead := <-deadLetters
	fmt.Printf("dead-lettered %q after %d attempt(s)\n", dead.Payload, dead.Attempts)

	// Close drains: the consumer stops, and the workers finish what they are
	// already holding before it returns.
	if err = pool.Close(ctx); err != nil {
		panic(err)
	}

	// Output:
	// handled: order-1
	// dead-lettered "malformed" after 1 attempt(s)
}

// ExampleNewPoolGroup drains three topics from one process, and shows what
// happens when the third pool cannot be built: the two that came up are drained
// rather than left consuming a process that is on its way out.
func ExampleNewPoolGroup() {
	ctx := context.Background()

	// A real service passes the ConsumerProvider it built at startup. This one
	// is this package's test double, and it has been told to refuse the third
	// subscription — a broker that is briefly unreachable at exactly the wrong
	// moment.
	broker := newFakeBroker()
	broker.refuse("emails", errBroker)

	handled := make(chan string, 1)

	spec := func(topic string) jobs.PoolSpec {
		return jobs.PoolSpec{
			Topic: topic,
			// Nil would do here: a spec that names only its topic and handler
			// takes the package defaults.
			Config: &jobs.PoolConfig{Concurrency: 4, Retry: retrycfg.Config{MaxAttempts: 1}},
			Handler: func(_ context.Context, payload []byte) error {
				handled <- topic + ": " + string(payload)

				return nil
			},
		}
	}

	group, err := jobs.NewPoolGroup(ctx, []jobs.PoolSpec{
		spec("orders"),
		spec("invoices"),
		spec("emails"),
	}, broker.provider())
	if err != nil {
		panic(err)
	}

	// Start is all-or-nothing. It brought up orders and invoices, failed on
	// emails, and drained the first two before returning.
	if err = group.Start(ctx); err != nil {
		fmt.Println("start failed:", err)
	}

	// So there is nothing left consuming, and Close has nothing to do.
	if err = group.Close(ctx); err != nil {
		panic(err)
	}

	fmt.Println("topics:", group.Topics())

	// Output:
	// start failed: building job pool for topic "emails": building consumer for topic "emails": broker refused the subscription
	// topics: [orders invoices emails]
}

// ExampleNewScheduler runs periodic work under a lease, so that only one
// replica executes a given job per tick.
func ExampleNewScheduler() {
	ctx := context.Background()

	// A fleet uses the postgres or redis locker. The memory locker is real
	// mutual exclusion within one process, which is what a single-replica
	// deployment — and this example — needs.
	locker, err := memory.NewLocker()
	if err != nil {
		panic(err)
	}
	defer func() { _ = locker.Close() }()

	scheduler, err := jobs.NewScheduler(ctx, &jobs.SchedulerConfig{}, locker)
	if err != nil {
		panic(err)
	}

	swept := make(chan struct{}, 1)

	if err = scheduler.Register(jobs.Job{
		Name:     "sweep-expired-sessions",
		Interval: time.Hour,
		LeaseTTL: 5 * time.Minute,
		// Without this the first sweep is an hour away, and a service that
		// deploys more often than that would never run it at all.
		RunOnStart: true,
		Run: func(context.Context) error {
			fmt.Println("sweeping expired sessions")
			swept <- struct{}{}

			return nil
		},
	}); err != nil {
		panic(err)
	}

	go scheduler.Run()

	<-swept

	if err = scheduler.Close(ctx); err != nil {
		panic(err)
	}

	// Output:
	// sweeping expired sessions
}

// ExampleCron schedules work by the calendar rather than by frequency, for a
// job that belongs at an hour rather than at an interval.
func ExampleCron() {
	ctx := context.Background()

	locker, err := memory.NewLocker()
	if err != nil {
		panic(err)
	}
	defer func() { _ = locker.Close() }()

	// Timezone is what a service whose jobs all belong to one calendar sets
	// once, instead of prefixing every expression. Left empty it is UTC, which
	// is the choice that does not depend on the image — and the one that has no
	// daylight saving to miss or repeat a job over.
	scheduler, err := jobs.NewScheduler(ctx, &jobs.SchedulerConfig{
		Timezone: "America/Chicago",
	}, locker)
	if err != nil {
		panic(err)
	}

	// Read in Chicago, from the config above. An expression carrying its own
	// CRON_TZ= prefix, or one built with CronIn, keeps its zone instead.
	nightly, err := jobs.Cron("0 3 * * *")
	if err != nil {
		panic(err)
	}

	compacted := make(chan struct{}, 1)

	if err = scheduler.Register(jobs.Job{
		Name:     "compact-audit-log",
		Schedule: nightly,
		LeaseTTL: 30 * time.Minute,
		// Fires once at startup too, so the example has something to print
		// without waiting until 03:00.
		RunOnStart: true,
		Run: func(context.Context) error {
			fmt.Println("compacting the audit log")
			compacted <- struct{}{}

			return nil
		},
	}); err != nil {
		panic(err)
	}

	go scheduler.Run()

	<-compacted

	if err = scheduler.Close(ctx); err != nil {
		panic(err)
	}

	// Output:
	// compacting the audit log
}
