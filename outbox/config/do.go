package outboxcfg

import (
	"context"

	"github.com/primandproper/platform-go/v15/outbox"

	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// WriterOptions are outbox.WriterOptions an application contributes to the
// Writer RegisterWriter builds, by registering a value of this type with the
// injector before the Writer is invoked.
//
// It is how a Writer that needs an option configuration cannot express — a
// side effect applied to every row it writes, say — is still built by
// RegisterWriter rather than beside it. A second provider for *outbox.Writer is
// one do refuses, so without this a consumer needing any option had to leave
// the outbox, and everything service.Register builds from it, out of its
// service.Config and wire them by hand.
//
// A named type rather than a bare []outbox.WriterOption, because the injector
// keys a value by its type, and a key this package owns is one nothing else
// registers by accident.
type WriterOptions []outbox.WriterOption

// RegisterWriter registers an *outbox.Writer with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Writer is invoked. WriterOptions are applied if
// registered, after the options derived from configuration, so they can
// override any of them; absent, the Writer is configuration's alone. One that
// is registered and fails to build is an error rather than an absence.
func RegisterWriter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*outbox.Writer, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		writerOptions, err := injection.InvokeOptional[WriterOptions](i)
		if err != nil {
			return nil, err
		}

		return NewWriter(ctx, cfg, client, WithPillars(pillars), WithWriterOptions(writerOptions...))
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

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewRelay(ctx, cfg, client, WithPillars(pillars))
	})
}
