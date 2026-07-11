package config

import (
	"github.com/primandproper/platform-go/v4/eventstream"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterEventStreamUpgrader registers an eventstream.EventStreamUpgrader with the injector.
func RegisterEventStreamUpgrader(i do.Injector) {
	do.Provide[eventstream.EventStreamUpgrader](i, func(i do.Injector) (eventstream.EventStreamUpgrader, error) {
		return NewEventStreamUpgrader(
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[*Config](i),
		)
	})
}

// RegisterBidirectionalEventStreamUpgrader registers an eventstream.BidirectionalEventStreamUpgrader with the injector.
func RegisterBidirectionalEventStreamUpgrader(i do.Injector) {
	do.Provide[eventstream.BidirectionalEventStreamUpgrader](i, func(i do.Injector) (eventstream.BidirectionalEventStreamUpgrader, error) {
		return NewBidirectionalEventStreamUpgrader(
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[*Config](i),
		)
	})
}
