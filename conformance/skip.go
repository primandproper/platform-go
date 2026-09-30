package conformance

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Skip skips t, recording that it did and why, for Skips.
//
// Every skip in these suites goes through here or Skipf rather than t.Skip,
// and a test in this package holds them to it. A skip is the suite saying it
// asserted nothing, and a run that sets every seam to the value that makes the
// assertions run is a run where any skip is a wiring regression rather than a
// deployment's choice — which only a harness that can see its skips can say.
func Skip(t *testing.T, args ...any) {
	t.Helper()

	record(t.Name(), fmt.Sprint(args...))
	t.Skip(args...)
}

// Skipf is Skip with a format.
func Skipf(t *testing.T, format string, args ...any) {
	t.Helper()

	record(t.Name(), fmt.Sprintf(format, args...))
	t.Skipf(format, args...)
}

// Skipped is one skip a suite made.
type Skipped struct {
	// Test is the skipped test's name below the test Skips was asked about,
	// as t.Name spells it.
	Test string
	// Reason is what the skip printed.
	Reason string
}

// skips is every skip recorded in this process, in the order they were made.
//
// Process-wide rather than per Run because a suite's helpers skip with the
// *testing.T they were handed and no Session, and a test's name already says
// which run it belongs to.
var skips struct {
	made []Skipped
	mu   sync.Mutex
}

func record(test, reason string) {
	skips.mu.Lock()
	defer skips.mu.Unlock()

	skips.made = append(skips.made, Skipped{Test: test, Reason: reason})
}

// Skips reports every skip a suite made beneath t, each named relative to it.
//
// It is for a harness to ask once the suites it ran under t have finished —
// from a t.Cleanup, since the suites run as parallel subtests and t's own
// function returns before they do — so it can fail a skip it did not expect:
// a subject that sets every seam and action to the value that makes assertions
// run should skip nothing it cannot name in advance.
func Skips(t *testing.T) []Skipped {
	t.Helper()

	return skipsUnder(t.Name())
}

// skipsUnder is Skips for the test named parent.
func skipsUnder(parent string) []Skipped {
	prefix := parent + "/"

	skips.mu.Lock()
	defer skips.mu.Unlock()

	var under []Skipped

	for i := range skips.made {
		if name, ok := strings.CutPrefix(skips.made[i].Test, prefix); ok {
			under = append(under, Skipped{Test: name, Reason: skips.made[i].Reason})
		}
	}

	return under
}
