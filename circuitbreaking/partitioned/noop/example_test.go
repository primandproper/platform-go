package noop_test

import (
	"fmt"

	"github.com/primandproper/platform-go/v5/circuitbreaking/partitioned/noop"
)

func ExampleNewKeyedCircuitBreaker() {
	cb := noop.NewKeyedCircuitBreaker()

	fmt.Println(cb.For("123").CanProceed())

	cb.For("123").Failed()
	fmt.Println(cb.For("123").CanProceed())
	// Output:
	// true
	// true
}
