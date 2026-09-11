package notificationscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/notifications"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/notifications/mobile"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers the notifications store with the injector, under four
// keys: the concrete *notifications.SQLStore, and the three seams it satisfies —
// notifications.Inbox, notifications.Registry and mobile.TokenInvalidator.
//
// The four are one store rather than four. The interfaces are registered as
// narrowings of the concrete registration, so a container that invokes the
// inbox and the registry gets the same value, holding one connection and one
// set of instruments.
//
// The last of them is what closes this package's feedback loop from
// configuration: mobilecfg.RegisterPushSender resolves a mobile.TokenInvalidator
// optionally and, finding one, hands it to the sender — so a container carrying
// both halves prunes tokens the providers have permanently rejected, and one
// carrying only the sender behaves exactly as it did before.
//
// The narrowing is registered here rather than resolved there because the
// direction it points is the one the module allows. mobile is a push provider
// behind an interface and this package owns the device table, so mobile may not
// name it; naming mobile.TokenInvalidator from this side costs an import of a
// package this one already builds on top of, and it means a sender never
// depends on a store to reach a method that takes two strings.
//
// Prerequisites: *Config and database.Client must be registered in the injector
// before the store is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*notifications.SQLStore, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewStore(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})

	// The three narrowings, each returning only once the store's error is known
	// to be nil: handing a nil *SQLStore straight back would make a non-nil
	// interface holding a nil pointer, and a caller testing the result against
	// nil would find a store that panics on first use.
	do.Provide(i, func(i do.Injector) (notifications.Inbox, error) {
		store, err := do.Invoke[*notifications.SQLStore](i)
		if err != nil {
			return nil, err
		}

		return store, nil
	})

	do.Provide(i, func(i do.Injector) (notifications.Registry, error) {
		store, err := do.Invoke[*notifications.SQLStore](i)
		if err != nil {
			return nil, err
		}

		return store, nil
	})

	do.Provide(i, func(i do.Injector) (mobile.TokenInvalidator, error) {
		store, err := do.Invoke[*notifications.SQLStore](i)
		if err != nil {
			return nil, err
		}

		return store, nil
	})
}
