package numbers_test

import (
	"testing"

	"github.com/primandproper/platform-go/v5/numbers"
)

func BenchmarkNumbers(b *testing.B) {
	b.Run("RoundToDecimalPlaces", func(b *testing.B) {
		for b.Loop() {
			f32Sink = numbers.RoundToDecimalPlaces(3.14159, 2)
		}
	})

	b.Run("Scale", func(b *testing.B) {
		for b.Loop() {
			f32Sink = numbers.Scale(2.5, 2.0)
		}
	})

	b.Run("ScaleToYield", func(b *testing.B) {
		for b.Loop() {
			f32Sink = numbers.ScaleToYield(2.0, 4, 6)
		}
	})
}

var f32Sink float32
