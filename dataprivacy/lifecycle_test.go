package dataprivacy

import (
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// There is no fulfiller lifecycle test here any more, and its absence is the
// port. The loop it drove — Run, Close, a poll interval, a claim that could
// fail — belongs to an operations.Worker now, and the end-to-end equivalent
// is TestFulfillment, which runs a real worker on each dialect.

func TestSweeper_Lifecycle(T *testing.T) {
	T.Parallel()

	T.Run("a store error is reported without stopping the other chores", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		// Overdue counting fails; lapse, expire, and reap still ran, so the
		// result is partial rather than absent.
		env.sweeper.store = &failingOverdueStore{Store: env.sweeper.store}

		result, err := env.sweeper.Sweep(t.Context())
		must.Error(t, err)
		must.NotNil(t, result)
		test.StrContains(t, err.Error(), "counting overdue")
	})
}
