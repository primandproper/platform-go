package healthcheck

import (
	"context"
	"errors"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type mockChecker struct {
	checkFn func(ctx context.Context) error
	name    string
}

func (m *mockChecker) Name() string {
	return m.name
}

func (m *mockChecker) Check(ctx context.Context) error {
	if m.checkFn != nil {
		return m.checkFn(ctx)
	}
	return nil
}

func TestRegistry_CheckAll(T *testing.T) {
	T.Parallel()

	T.Run("empty registry returns up", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		ctx := context.Background()

		result := reg.CheckAll(ctx)

		must.NotNil(t, result)
		test.EqOp(t, StatusUp, result.Status)
		test.MapEmpty(t, result.Components)
	})

	T.Run("a check receives a bounded (timeout) context", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		reg.Register(&mockChecker{name: "a", checkFn: func(ctx context.Context) error {
			// Each check must run under a deadline so a hung check can't stall the probe.
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("expected a deadline on the check context")
			}
			return nil
		}})

		result := reg.CheckAll(context.Background())
		must.NotNil(t, result)
		test.EqOp(t, StatusUp, result.Status)
	})

	T.Run("all checkers up", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		reg.Register(&mockChecker{name: "a"})
		reg.Register(&mockChecker{name: "b"})
		ctx := context.Background()

		result := reg.CheckAll(ctx)

		must.NotNil(t, result)
		test.EqOp(t, StatusUp, result.Status)
		test.MapLen(t, 2, result.Components)
		test.EqOp(t, ComponentResult{Status: StatusUp}, result.Components["a"])
		test.EqOp(t, ComponentResult{Status: StatusUp}, result.Components["b"])
	})

	T.Run("one checker down", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		reg.Register(&mockChecker{name: "up"})
		reg.Register(&mockChecker{
			name: "down",
			checkFn: func(context.Context) error {
				return errors.New("connection refused")
			},
		})
		ctx := context.Background()

		result := reg.CheckAll(ctx)

		must.NotNil(t, result)
		test.EqOp(t, StatusDown, result.Status)
		test.MapLen(t, 2, result.Components)
		test.EqOp(t, ComponentResult{Status: StatusUp}, result.Components["up"])
		test.EqOp(t, ComponentResult{Status: StatusDown, Message: "connection refused"}, result.Components["down"])
	})

	T.Run("ignores nil checker", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		reg.Register(nil)
		reg.Register(&mockChecker{name: "a"})
		ctx := context.Background()

		result := reg.CheckAll(ctx)

		must.NotNil(t, result)
		test.EqOp(t, StatusUp, result.Status)
		test.MapLen(t, 1, result.Components)
	})
}
