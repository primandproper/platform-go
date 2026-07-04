package numbers

import "math"

// RoundToDecimalPlaces rounds a float32 value to the specified number of decimal places.
func RoundToDecimalPlaces(value float32, precision uint8) float32 {
	// Round in float64 so large values don't saturate int32 and high precision doesn't overflow
	// the multiplier to +Inf. math.Round rounds half away from zero, matching the prior behavior.
	multiplier := math.Pow(10, float64(precision))
	return float32(math.Round(float64(value)*multiplier) / multiplier)
}

// Scale multiplies a value by a scaling factor and rounds to the specified precision (default: 2).
// Useful for scaling quantities by a factor.
// For example, Scale(2.5, 2.0) would return 5.0 (doubling the quantity).
func Scale(value, factor float32, precision ...uint8) float32 {
	result := value * factor

	p := uint8(2)
	if len(precision) > 0 {
		p = precision[0]
	}

	return RoundToDecimalPlaces(result, p)
}

// ScaleToYield scales a quantity from an original yield to a desired yield.
// The optional precision parameter specifies the number of decimal places to round to (default: 2).
// For example, ScaleToYield(2.0, 4, 6) returns 3.0 (scaling from 4 units to 6).
func ScaleToYield(originalValue float32, originalYield, desiredYield int, precision ...uint8) float32 {
	if originalYield <= 0 {
		return originalValue
	}

	factor := float32(desiredYield) / float32(originalYield)
	return Scale(originalValue, factor, precision...)
}
