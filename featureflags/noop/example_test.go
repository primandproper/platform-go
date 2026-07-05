package noop_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v4/featureflags"
	"github.com/primandproper/platform-go/v4/featureflags/noop"
)

func ExampleNewFeatureFlagManager() {
	mgr := noop.NewFeatureFlagManager()
	defer func() { _ = mgr.Close() }()

	canUse, err := mgr.CanUseFeature(
		context.Background(),
		"dark-mode",
		featureflags.EvaluationContext{TargetingKey: "user-1"},
	)
	if err != nil {
		panic(err)
	}

	fmt.Println(canUse)
	// Output: false
}
