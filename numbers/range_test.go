package numbers

import (
	"context"
	"testing"

	"github.com/shoenig/test"
)

func TestMinRange_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("valid", func(t *testing.T) {
		t.Parallel()

		x := &MinRange[float32]{Min: 1.0}
		test.NoError(t, x.ValidateWithContext(context.Background()))
	})

	T.Run("a zero minimum is a valid range", func(t *testing.T) {
		t.Parallel()

		// Min is a value type that is always present; a range starting at 0 is
		// legitimate and must not be rejected as "missing".
		test.NoError(t, (&MinRange[float32]{}).ValidateWithContext(context.Background()))
		test.NoError(t, (&MinRange[uint16]{}).ValidateWithContext(context.Background()))
		test.NoError(t, (&MinRange[uint32]{}).ValidateWithContext(context.Background()))
	})

	T.Run("max below min is invalid", func(t *testing.T) {
		t.Parallel()

		maxVal := 2
		x := &MinRange[int]{Min: 5, Max: &maxVal}
		test.Error(t, x.ValidateWithContext(context.Background()))
	})

	T.Run("max at or above min is valid", func(t *testing.T) {
		t.Parallel()

		eq := 5
		above := 10
		test.NoError(t, (&MinRange[int]{Min: 5, Max: &eq}).ValidateWithContext(context.Background()))
		test.NoError(t, (&MinRange[int]{Min: 5, Max: &above}).ValidateWithContext(context.Background()))
	})
}
