package outboxcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/outbox"

	"github.com/samber/do/v2"
)

// RegisterWriter registers an *outbox.Writer with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Writer is invoked.
func RegisterWriter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*outbox.Writer, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewWriter(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}

// RegisterRelay registers an *outbox.Relay with the injector. The Relay builds
// its own publisher provider from the config's Queue section.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Relay is invoked.
func RegisterRelay(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*outbox.Relay, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewRelay(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}
