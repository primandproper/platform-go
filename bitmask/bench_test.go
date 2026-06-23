package bitmask

import "testing"

func BenchmarkBitmask(b *testing.B) {
	const (
		flagA uint8 = 1 << iota
		flagB
		flagC
		flagD
	)

	m := New(flagA, flagC, flagD)

	b.Run("Has", func(b *testing.B) {
		for b.Loop() {
			boolSink = m.Has(flagC)
		}
	})

	b.Run("Set", func(b *testing.B) {
		for b.Loop() {
			maskSink = m.Set(flagB)
		}
	})

	b.Run("Count", func(b *testing.B) {
		for b.Loop() {
			intSink = m.Count()
		}
	})
}

var (
	boolSink bool
	maskSink Bitmask[uint8]
	intSink  int
)
