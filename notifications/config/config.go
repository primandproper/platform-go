/*
Package notificationscfg assembles a notifications store — the in-app inbox and
the device registry — from environment configuration.

There is one thing to configure and one thing to build. The dialect comes from
the database.Client so it cannot disagree with the database the statements run
against, and everything else about the store is either the schema's or an
option.

The table prefix is the exception, and it has to be here because it must match
the prefix the migrations were rendered with — a deployment sharing one database
between applications sets both from the same value.

# The half this package exists to reach

notifications/mobile can prune a dead device token the moment a provider says
the handset is gone, but only if it has been handed the registry holding it.
That wiring was expressible in Go and nowhere else, so a deployment assembled
from configuration kept pushing to uninstalled apps forever. [RegisterStore]
registers the registry under its interface, and mobilecfg's RegisterPushSender
resolves it optionally: register both halves and the feedback loop is closed,
register one and nothing changes.

There is deliberately no EnsureDefaults here. The one field's default is the
empty prefix — notifications.DefaultTablePrefix — so an unset field is already
the default, and a method that assigned "" to "" would be a defaulting step that
never defaulted anything.
*/
package notificationscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/notifications/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a notifications store.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix namespaces the two notifications tables. It must match the
	// prefix the migrations were rendered with. Empty — the default — renders
	// notifications_inbox and notifications_devices.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config.
//
// The prefix is vetted against the identifiers it actually renders rather than
// against a pattern, so a prefix that is legal in isolation but produces an
// over-long index name fails here instead of at the first migration.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := validation.ValidateStructWithContext(ctx, cfg); err != nil {
		return err
	}

	return migrations.ValidatePrefix(cfg.TablePrefix)
}

// NewStore builds the Store. client must be the database holding the
// notifications tables.
//
// It returns [notifications.Store] — both seams at once — rather than either
// half, because a caller who configured a store configured both halves of one
// and narrowing here would pick which of them a deployment gets.
// [RegisterStore] is where the consumer-facing narrowings happen, and it
// registers all three.
//
// It returned the concrete *notifications.SQLStore before, which made every
// method that type happens to export part of what a configured deployment may
// depend on. Every sibling config package narrows to its own package's Store;
// this one now does too.
//
// The store is built into a variable and returned only once its error is known
// to be nil. notifications.NewSQLStore returns its own concrete type, so
// returning it straight through would convert a nil *notifications.SQLStore
// into a non-nil notifications.Store on the error path, and a caller testing the
// result against nil would find a store that panics on first use.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (notifications.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating notifications config")
	}

	options := newOptions(opts)

	base := []notifications.SQLStoreOption{
		notifications.WithTablePrefix(cfg.TablePrefix),
		notifications.WithStoreLogger(options.logger),
		notifications.WithStoreTracerProvider(options.tracerProvider),
		notifications.WithStoreMetricsProvider(options.metricsProvider),
	}

	store, storeErr := notifications.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}
