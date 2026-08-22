package eventstreamcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/eventstream"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterEventStreamUpgrader registers an eventstream.EventStreamUpgrader with the injector.
func RegisterEventStreamUpgrader(i do.Injector) {
	do.Provide(i, func(i do.Injector) (eventstream.EventStreamUpgrader, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewEventStreamUpgrader(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}

// RegisterBidirectionalEventStreamUpgrader registers an eventstream.BidirectionalEventStreamUpgrader with the injector.
func RegisterBidirectionalEventStreamUpgrader(i do.Injector) {
	do.Provide(i, func(i do.Injector) (eventstream.BidirectionalEventStreamUpgrader, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewBidirectionalEventStreamUpgrader(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}
