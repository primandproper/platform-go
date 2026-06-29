package msgconfig

import (
	"context"

	"github.com/primandproper/platform-go/v2/messagequeue"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterMessageQueue registers both messagequeue.ConsumerProvider and
// messagequeue.PublisherProvider with the injector.
func RegisterMessageQueue(i do.Injector) {
	do.Provide[messagequeue.ConsumerProvider](i, func(i do.Injector) (messagequeue.ConsumerProvider, error) {
		return ProvideConsumerProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
			do.MustInvoke[*Config](i),
		)
	})
	do.Provide[messagequeue.PublisherProvider](i, func(i do.Injector) (messagequeue.PublisherProvider, error) {
		return ProvidePublisherProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
			do.MustInvoke[*Config](i),
		)
	})
}
