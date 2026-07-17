package healthcheck_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v5/healthcheck"
)

// simpleChecker is a Checker that always reports healthy.
type simpleChecker struct{ name string }

func (c *simpleChecker) Name() string                  { return c.name }
func (c *simpleChecker) Check(_ context.Context) error { return nil }

func ExampleRegistry() {
	reg := healthcheck.NewRegistry()
	reg.Register(&simpleChecker{name: "database"})

	result := reg.CheckAll(context.Background())
	fmt.Println(result.Status)
	fmt.Println(result.Components["database"].Status)
	// Output:
	// up
	// up
}
