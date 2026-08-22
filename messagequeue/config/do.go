package messagequeuecfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterMessageQueue registers both messagequeue.ConsumerProvider and
// messagequeue.PublisherProvider with the injector.
func RegisterMessageQueue(i do.Injector) {
	do.Provide(i, func(i do.Injector) (messagequeue.ConsumerProvider, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewConsumerProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
	do.Provide(i, func(i do.Injector) (messagequeue.PublisherProvider, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewPublisherProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}
