package asynccfg

import (
	"context"

	"github.com/primandproper/platform-go/v10/internal/injection"
	"github.com/primandproper/platform-go/v10/messagequeue"
	"github.com/primandproper/platform-go/v10/notifications/async"
	"github.com/primandproper/platform-go/v10/observability"

	"github.com/samber/do/v2"
)

// RegisterAsyncNotifier registers an async.AsyncNotifier with the injector.
//
// The messagequeue providers are invoked optionally: a container that registers
// none still wires up, because a config that has not enabled fan-out never
// reaches them. A container that registered one it cannot build fails here
// rather than degrading to a notifier that looks configured and only delivers
// to whichever replica the publisher happened to land on.
func RegisterAsyncNotifier(i do.Injector) {
	do.Provide[async.AsyncNotifier](i, func(i do.Injector) (async.AsyncNotifier, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		publisherProvider, err := injection.InvokeOptional[messagequeue.PublisherProvider](i)
		if err != nil {
			return nil, err
		}

		consumerProvider, err := injection.InvokeOptional[messagequeue.ConsumerProvider](i)
		if err != nil {
			return nil, err
		}

		return NewAsyncNotifier(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			publisherProvider,
			consumerProvider,
			WithPillars(pillars),
		)
	})
}
