package numbers

import (
	"testing"

	"github.com/shoenig/test"
)

func TestRoundToDecimalPlaces(T *testing.T) {
	T.Parallel()

	T.Run("round positive number to 2 decimals", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(3.14159, 2)
		expected := float32(3.14)

		test.EqOp(t, expected, result)
	})

	T.Run("round positive number to 0 decimals", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(3.7, 0)
		expected := float32(4.0)

		test.EqOp(t, expected, result)
	})

	T.Run("round positive number to 4 decimals", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(3.141592653, 4)
		expected := float32(3.1416)

		test.InDelta(t, expected, result, float32(0.0001))
	})

	T.Run("round negative number to 2 decimals", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(-3.14159, 2)
		expected := float32(-3.14)

		test.EqOp(t, expected, result)
	})

	T.Run("round negative number to 0 decimals", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(-3.7, 0)
		expected := float32(-4.0)

		test.EqOp(t, expected, result)
	})

	T.Run("round zero value", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(0.0, 2)
		expected := float32(0.0)

		test.EqOp(t, expected, result)
	})

	T.Run("round number that needs rounding up", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(2.555, 2)
		expected := float32(2.56)

		test.InDelta(t, expected, result, float32(0.01))
	})

	T.Run("round number that needs rounding down", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(2.554, 2)
		expected := float32(2.55)

		test.InDelta(t, expected, result, float32(0.01))
	})

	T.Run("round large number", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(12345.6789, 2)
		expected := float32(12345.68)

		test.InDelta(t, expected, result, float32(0.01))
	})

	T.Run("round very small number", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(0.001234, 3)
		expected := float32(0.001)

		test.InDelta(t, expected, result, float32(0.0001))
	})

	T.Run("round number with high precision", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(1.23456789, 5)
		expected := float32(1.23457)

		test.InDelta(t, expected, result, float32(0.00001))
	})

	T.Run("round negative number that needs rounding up", func(t *testing.T) {
		t.Parallel()

		result := RoundToDecimalPlaces(-2.555, 2)
		expected := float32(-2.56)

		test.InDelta(t, expected, result, float32(0.01))
	})

	T.Run("large value does not saturate at int32", func(t *testing.T) {
		t.Parallel()

		// value*10^2 = 2e9 overflows int32; the result must remain 2e7, not the saturated value.
		result := RoundToDecimalPlaces(2e7, 2)
		expected := float32(2e7)

		test.EqOp(t, expected, result)
	})

	T.Run("high precision does not overflow to zero", func(t *testing.T) {
		t.Parallel()

		// A float32 multiplier saturates to +Inf at precision >= 39, previously yielding 0.
		result := RoundToDecimalPlaces(3.14, 39)

		test.InDelta(t, float32(3.14), result, float32(0.01))
	})
}

func TestScale(T *testing.T) {
	T.Parallel()

	T.Run("double a quantity with default precision", func(t *testing.T) {
		t.Parallel()

		result := Scale(2.5, 2.0)
		expected := float32(5.0)

		test.EqOp(t, expected, result)
	})

	T.Run("halve a quantity", func(t *testing.T) {
		t.Parallel()

		result := Scale(4.0, 0.5)
		expected := float32(2.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale with custom precision", func(t *testing.T) {
		t.Parallel()

		result := Scale(3.333, 3.0, 3)
		expected := float32(9.999)

		test.InDelta(t, expected, result, float32(0.001))
	})

	T.Run("scale with zero precision", func(t *testing.T) {
		t.Parallel()

		result := Scale(2.7, 2.0, 0)
		expected := float32(5.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale by factor of 1", func(t *testing.T) {
		t.Parallel()

		result := Scale(5.5, 1.0)
		expected := float32(5.5)

		test.EqOp(t, expected, result)
	})

	T.Run("scale zero value", func(t *testing.T) {
		t.Parallel()

		result := Scale(0.0, 5.0)
		expected := float32(0.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale by zero factor", func(t *testing.T) {
		t.Parallel()

		result := Scale(10.0, 0.0)
		expected := float32(0.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale with fractional factor", func(t *testing.T) {
		t.Parallel()

		result := Scale(2.5, 1.5)
		expected := float32(3.75)

		test.EqOp(t, expected, result)
	})

	T.Run("scale large value", func(t *testing.T) {
		t.Parallel()

		result := Scale(1000.0, 2.5)
		expected := float32(2500.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale very large value without int32 saturation", func(t *testing.T) {
		t.Parallel()

		// 2e7 * 2 = 4e7; the rounding intermediate (4e9) overflows int32 in the old implementation.
		result := Scale(2e7, 2.0)
		expected := float32(4e7)

		test.EqOp(t, expected, result)
	})

	T.Run("scale with high precision", func(t *testing.T) {
		t.Parallel()

		result := Scale(1.23456, 2.0, 5)
		expected := float32(2.46912)

		test.InDelta(t, expected, result, float32(0.00001))
	})

	T.Run("scale negative value", func(t *testing.T) {
		t.Parallel()

		result := Scale(-5.0, 2.0)
		expected := float32(-10.0)

		test.EqOp(t, expected, result)
	})
}

func TestScaleToYield(T *testing.T) {
	T.Parallel()

	T.Run("scale from 4 servings to 6 servings", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(2.0, 4, 6)
		expected := float32(3.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale from 4 servings to 2 servings", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(4.0, 4, 2)
		expected := float32(2.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale from 2 servings to 8 servings", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(1.5, 2, 8)
		expected := float32(6.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale with same yield", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(3.5, 4, 4)
		expected := float32(3.5)

		test.EqOp(t, expected, result)
	})

	T.Run("scale with custom precision", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(1.0, 3, 7, 3)
		expected := float32(2.333)

		test.InDelta(t, expected, result, float32(0.001))
	})

	T.Run("scale with zero precision", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(2.7, 4, 6, 0)
		expected := float32(4.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale with zero original yield returns original value", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(5.0, 0, 10)
		expected := float32(5.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale with negative original yield returns original value", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(5.0, -2, 10)
		expected := float32(5.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale fractional value", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(0.5, 4, 8)
		expected := float32(1.0)

		test.EqOp(t, expected, result)
	})

	T.Run("scale from small to large batch", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(0.25, 2, 12)
		expected := float32(1.5)

		test.EqOp(t, expected, result)
	})

	T.Run("scale complex ratio", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(2.5, 6, 9)
		expected := float32(3.75)

		test.EqOp(t, expected, result)
	})

	T.Run("scale with high precision for exact conversions", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(1.0, 3, 5, 4)
		expected := float32(1.6667)

		test.InDelta(t, expected, result, float32(0.0001))
	})

	T.Run("scale zero value", func(t *testing.T) {
		t.Parallel()

		result := ScaleToYield(0.0, 4, 8)
		expected := float32(0.0)

		test.EqOp(t, expected, result)
	})
}
