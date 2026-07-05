package partitioned

import (
	"testing"

	"github.com/primandproper/platform-go/v4/circuitbreaking"
	cbnoop "github.com/primandproper/platform-go/v4/circuitbreaking/noop"
)

func BenchmarkKeyedCircuitBreaker(b *testing.B) {
	x := NewKeyedCircuitBreaker(cbnoop.NewCircuitBreaker(), map[string]circuitbreaking.CircuitBreaker{
		"registered": cbnoop.NewCircuitBreaker(),
	})

	b.Run("For_dedicated", func(b *testing.B) {
		for b.Loop() {
			boolSink = x.For("registered").CanProceed()
		}
	})

	b.Run("For_global", func(b *testing.B) {
		for b.Loop() {
			boolSink = x.For("unregistered").CanProceed()
		}
	})
}

var boolSink bool
