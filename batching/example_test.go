package batching_test

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/batching"
)

// exampleTimeout bounds every wait in this file.
//
// Examples are executable tests that run in the package's one test binary, and
// they run it to completion: an example that blocks blocks every test after it,
// where a unit test that blocks fails only its own case. Each wait below is a
// wait on work another goroutine has to finish, so each of them is a wait that
// something else in this package could stop happening — which is exactly what
// mutation testing does to it on purpose.
const exampleTimeout = 30 * time.Second

// view is one row of a "times seen" table, the shape a hot read path writes.
type view struct {
	page  string
	count int
}

// Concurrent callers block until their own rows have landed, and land together.
func ExampleGroupCommit() {
	var (
		mu     sync.Mutex
		totals = map[string]int{}
	)

	// The write function is the caller's, and receives one merged, key-ordered
	// batch per flush — which is what makes it safe to write with a single
	// multi-row statement. Standing in for the table here is a map, so that the
	// example's output does not depend on how the three callers interleaved.
	commit, err := batching.NewGroupCommit(func(_ context.Context, rows []view) error {
		mu.Lock()
		defer mu.Unlock()

		for _, row := range rows {
			totals[row.page] += row.count
		}

		return nil
	},
		batching.WithMerge(
			func(v view) string { return v.page },
			func(existing, incoming view) view {
				return view{page: existing.page, count: existing.count + incoming.count}
			},
		),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Submit blocks until the caller's own rows have landed, so the context it
	// is given is what decides how long it can be kept waiting.
	ctx, cancel := context.WithTimeout(context.Background(), exampleTimeout)
	defer cancel()

	defer func() { _ = commit.Close(ctx) }()

	var wg sync.WaitGroup

	for _, page := range []string{"/pricing", "/home", "/pricing"} {
		wg.Go(func() {
			if submitErr := commit.Submit(ctx, view{page: page, count: 1}); submitErr != nil {
				log.Print(submitErr)
			}
		})
	}

	wg.Wait()

	if err = commit.Close(ctx); err != nil {
		log.Print(err)
	}

	// Each page was written once per flush it appeared in, never once per
	// caller, and the two /pricing views were summed by the merge rather than
	// racing each other.
	var written []string

	mu.Lock()
	for page, count := range totals {
		written = append(written, fmt.Sprintf("%s=%d", page, count))
	}
	mu.Unlock()

	sort.Strings(written)
	fmt.Println(strings.Join(written, " "))
	// Output: /home=1 /pricing=2
}

// A Buffer's callers never block, and a caller that has to write a key itself
// takes it back first.
func ExampleBuffer_Take() {
	flushed := make(chan []string, 1)

	buffer, err := batching.NewBuffer(func(_ context.Context, keys []string) error {
		flushed <- keys

		return nil
	},
		batching.WithOrder(strings.Compare),
		batching.WithFlushInterval(time.Hour), // this example flushes on Close
	)
	if err != nil {
		log.Fatal(err)
	}

	// Take waits out a flush that is already carrying the key it wants, and this
	// is the bound the doc comment promises that wait has.
	ctx, cancel := context.WithTimeout(context.Background(), exampleTimeout)
	defer cancel()

	defer func() { _ = buffer.Close(ctx) }()

	// A read path marking what it touched, on the way out of a request handler.
	buffer.Add("session-1", "session-2", "session-3")

	// Something else is about to rewrite session-2 itself, and needs the
	// buffered write not to land in the middle of it.
	taken, err := buffer.Take(ctx, "session-2")
	if err != nil {
		log.Print(err)

		return
	}

	fmt.Println("taken:", strings.Join(taken, " "))

	if err = buffer.Close(ctx); err != nil {
		log.Print(err)
	}

	// Close returns only once the write function has run, so the batch is in the
	// channel by now or there was never going to be one — nothing to wait for
	// either way.
	select {
	case keys := <-flushed:
		fmt.Println("flushed:", strings.Join(keys, " "))
	default:
		fmt.Println("flushed: nothing")
	}
	// Output:
	// taken: session-2
	// flushed: session-1 session-3
}
