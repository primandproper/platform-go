package inboundcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/webhooks/inbound"

	"github.com/samber/do/v2"
)

// RegisterVerifier registers an inbound.Verifier with the injector.
//
// Prerequisites: *Config must be registered in the injector before the Verifier
// is invoked.
//
// It is registered separately from the Receiver because a consumer on the other
// end of the topic may want to re-verify a Delivery it took off the queue, and
// that consumer has no receiver and no router.
func RegisterVerifier(i do.Injector) {
	do.Provide(i, func(i do.Injector) (inbound.Verifier, error) {
		return NewVerifier(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
		)
	})
}

// RegisterReceiver registers a *inbound.Receiver with the injector.
//
// Prerequisites: *Config and messagequeue.PublisherProvider (see
// messagequeuecfg.RegisterMessageQueue) must be registered in the injector
// before the Receiver is invoked.
//
// One receiver per container, because one Config describes one provider
// endpoint. A service taking webhooks from two providers wires its two
// receivers explicitly through NewReceiver rather than through this
// registration, which has one *Config to read and so can only build one.
func RegisterReceiver(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*inbound.Receiver, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewReceiver(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[messagequeue.PublisherProvider](i),
			WithPillars(pillars),
		)
	})
}
