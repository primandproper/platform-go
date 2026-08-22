package operations_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/primandproper/platform-go/v13/operations"
)

// exportRequest is what a caller asks for when they start an export. It is the
// application's type, and this package never looks inside it.
type exportRequest struct {
	SubjectID string `json:"subjectID"`
	Format    string `json:"format"`
}

// Example_register shows the shape of a Definition: a name, a function, and how
// its progress should read.
//
// The Runner reports through the two tiers. SetUnits gives the outer one its
// denominator — here, the data domains the work fans out over — and Advance
// moves the inner one, which has no total because a collector cannot say how
// many records it will find without fetching them first.
func Example_register() {
	registry := operations.NewRegistry()

	domains := []string{"identity", "webhooks", "mealplanning"}

	err := operations.Register(registry, operations.Definition[exportRequest]{
		Kind:       "dataprivacy.export",
		CountLabel: "records",
		Run: func(_ context.Context, req exportRequest, rep operations.Reporter) (*operations.Result, error) {
			rep.SetUnits(len(domains))

			for _, domain := range domains {
				// Between units is where a Runner can stop and describe where it
				// got to, which is why the check belongs here rather than in the
				// middle of collecting one.
				select {
				case <-rep.Cancelled():
					return nil, operations.Unretryable(
						operations.Fail("cancelled", "stopped after %s", domain))
				default:
				}

				rep.StartUnit(domain)
				rep.Sayf("collecting %s", domain)

				// Whatever the application already does to collect a domain. The
				// count is advisory and buffered, so this is cheap to call per
				// record.
				rep.Advance(1_000)

				rep.FinishUnit()
			}

			detail, _ := json.Marshal(map[string]int{"domains": len(domains)})

			return &operations.Result{
				URI:    "s3://exports/" + req.SubjectID + ".zip",
				Detail: detail,
			}, nil
		},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(registry.Kinds())

	// Output: [dataprivacy.export]
}

// ExampleProgress_Fraction shows the case a progress surface has to handle and
// usually does not: work that never declared a denominator.
//
// ok is false rather than the fraction being zero, so a caller cannot render a
// bar that sits at 0% forever and call it progress. The count is still there,
// and "4,300 records collected" is a perfectly good thing to show.
func ExampleProgress_Fraction() {
	withUnits := operations.Progress{UnitsDone: 3, UnitsTotal: new(9)}
	if fraction, ok := withUnits.Fraction(); ok {
		fmt.Printf("%.0f%%\n", fraction*100)
	}

	withoutUnits := operations.Progress{Count: 4300, CountLabel: "records"}
	if _, ok := withoutUnits.Fraction(); !ok {
		fmt.Printf("%d %s collected\n", withoutUnits.Count, withoutUnits.CountLabel)
	}

	// Output:
	// 33%
	// 4300 records collected
}

// ExampleState_Terminal shows the one field a client is obliged to understand.
//
// Everything else on an Operation is there to be used and safe to ignore, which
// is what lets a client written against one kind of operation work against every
// other.
func ExampleState_Terminal() {
	for _, state := range []operations.State{
		operations.StatePending,
		operations.StateRunning,
		operations.StateSucceeded,
		operations.StateFailed,
		operations.StateCancelled,
	} {
		fmt.Printf("%s: done=%t\n", state, state.Terminal())
	}

	// Output:
	// pending: done=false
	// running: done=false
	// succeeded: done=true
	// failed: done=true
	// cancelled: done=true
}
