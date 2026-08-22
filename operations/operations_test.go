package operations

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestState_Terminal(T *testing.T) {
	T.Parallel()

	for name, tc := range map[string]struct {
		state    State
		terminal bool
	}{
		"pending":   {StatePending, false},
		"running":   {StateRunning, false},
		"succeeded": {StateSucceeded, true},
		"failed":    {StateFailed, true},
		"cancelled": {StateCancelled, true},
		// Not a state this package writes. It is reported as non-terminal, which
		// is the reading that keeps a corrupted row from looking finished — a
		// watcher will keep watching it and a worker will keep refusing it,
		// which is visible, where "done" would not be.
		"nonsense": {State("nonsense"), false},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.terminal, tc.state.Terminal())
		})
	}
}

func TestState_Valid(T *testing.T) {
	T.Parallel()

	for _, state := range []State{StatePending, StateRunning, StateSucceeded, StateFailed, StateCancelled} {
		test.True(T, state.Valid(), test.Sprintf("%q should be valid", state))
	}

	test.False(T, State("").Valid())
	test.False(T, State("succeeded ").Valid())
}

func TestProgress_Fraction(T *testing.T) {
	T.Parallel()

	T.Run("no denominator means no fraction", func(t *testing.T) {
		t.Parallel()

		// The case the whole two-tier design exists for: work that cannot say
		// how much there is. ok must be false rather than the fraction being
		// zero, so a caller cannot render a bar that sits at 0% forever and
		// call it progress.
		fraction, ok := Progress{Count: 4300}.Fraction()
		test.False(t, ok)
		test.EqOp(t, float64(0), fraction)
	})

	T.Run("a zero denominator is no denominator", func(t *testing.T) {
		t.Parallel()

		_, ok := Progress{UnitsTotal: pointer.To(0)}.Fraction()
		test.False(t, ok)
	})

	T.Run("partway", func(t *testing.T) {
		t.Parallel()

		fraction, ok := Progress{UnitsDone: 3, UnitsTotal: pointer.To(9)}.Fraction()
		must.True(t, ok)
		test.EqOp(t, float64(3)/float64(9), fraction)
	})

	T.Run("done", func(t *testing.T) {
		t.Parallel()

		fraction, ok := Progress{UnitsDone: 9, UnitsTotal: pointer.To(9)}.Fraction()
		must.True(t, ok)
		test.EqOp(t, float64(1), fraction)
	})

	T.Run("a numerator past its denominator is clamped", func(t *testing.T) {
		t.Parallel()

		// Reachable through the same door as any other double-count: a
		// reclaimed operation whose Runner re-ran units it had already
		// reported. Clamping is what keeps a client from rendering 133%.
		fraction, ok := Progress{UnitsDone: 12, UnitsTotal: pointer.To(9)}.Fraction()
		must.True(t, ok)
		test.EqOp(t, float64(1), fraction)
	})
}

func TestOperation_Terminal(T *testing.T) {
	T.Parallel()

	test.False(T, (*Operation)(nil).Terminal())
	test.False(T, (&Operation{State: StateRunning}).Terminal())
	test.True(T, (&Operation{State: StateSucceeded}).Terminal())
}

// The wire shape is a contract with every client, so the two decisions that are
// easy to undo by accident are pinned here.
func TestOperation_JSON(T *testing.T) {
	T.Parallel()

	T.Run("the request is not echoed back", func(t *testing.T) {
		t.Parallel()

		encoded, err := json.Marshal(&Operation{
			ID:      "abc",
			Kind:    "export",
			State:   StateRunning,
			Request: json.RawMessage(`{"email":"someone@example.com"}`),
		})
		must.NoError(t, err)

		test.StrNotContains(t, string(encoded), "someone@example.com")
		test.StrNotContains(t, string(encoded), "request")
	})

	T.Run("done is always present", func(t *testing.T) {
		t.Parallel()

		// It is the one field a client is obliged to understand, so it must not
		// be omitempty: a false that vanishes from the body is a client reading
		// undefined and deciding what it means.
		encoded, err := json.Marshal(&Operation{ID: "abc", State: StatePending})
		must.NoError(t, err)

		test.StrContains(t, string(encoded), `"done":false`)
	})

	T.Run("absent timestamps are omitted rather than zero", func(t *testing.T) {
		t.Parallel()

		encoded, err := json.Marshal(&Operation{ID: "abc", State: StatePending})
		must.NoError(t, err)

		test.StrNotContains(t, string(encoded), "startedAt")
		test.StrNotContains(t, string(encoded), "finishedAt")

		at := time.Now().UTC()
		encoded, err = json.Marshal(&Operation{ID: "abc", State: StateRunning, StartedAt: &at})
		must.NoError(t, err)

		test.StrContains(t, string(encoded), "startedAt")
	})
}
