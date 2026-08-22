package meteringcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/analytics"
	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/capitalism"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/metering"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a metering.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Store is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (metering.Store, error) {
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
}

// RegisterRecorder registers a *metering.DurableRecorder with the injector.
//
// Prerequisites: *Config, metering.Store (see RegisterStore),
// *metering.Registry (the application's meter definitions),
// metering.PeriodResolver, and analytics.EventReporter must be registered in
// the injector before the Recorder is invoked. Where no analytics reporting is
// wanted, register the named noop reporter.
func RegisterRecorder(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*metering.DurableRecorder, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewRecorder(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[metering.Store](i),
			do.MustInvoke[*metering.Registry](i),
			do.MustInvoke[metering.PeriodResolver](i),
			do.MustInvoke[analytics.EventReporter](i),
			WithPillars(pillars),
		)
	})
}

// RegisterEnforcer registers a *metering.Enforcer with the injector. The
// totals cache is optional: absent, the enforcer reads the store on every
// decision, which is metering.NewEnforcer's documented uncached behavior.
//
// Prerequisites: *Config, metering.Store (see RegisterStore),
// *metering.Registry, metering.PeriodResolver, and metering.QuotaSource must
// be registered in the injector before the Enforcer is invoked.
// metering.NewRegistryQuotaSource adapts the Registry where quotas live in
// meter definitions.
func RegisterEnforcer(i do.Injector) {
	do.Provide(i, func(i do.Injector) (metering.Enforcer, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		totals, err := injection.InvokeOptional[cache.Cache[metering.CachedTotal]](i)
		if err != nil {
			return nil, err
		}

		// Built into a variable and returned only once err is known to be nil:
		// NewEnforcer returns a *metering.QuotaEnforcer, and returning it
		// straight through would register a non-nil metering.Enforcer wrapping a
		// nil pointer whenever construction failed.
		enforcer, err := NewEnforcer(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[metering.Store](i),
			do.MustInvoke[*metering.Registry](i),
			do.MustInvoke[metering.PeriodResolver](i),
			do.MustInvoke[metering.QuotaSource](i),
			totals,
			WithPillars(pillars),
		)
		if err != nil {
			return nil, err
		}

		return enforcer, nil
	})
}

// RegisterFlusher registers a *metering.Flusher with the injector.
//
// Prerequisites: *Config, metering.Store (see RegisterStore),
// metering.ProviderMapper, and capitalism.UsageReporter must be registered in
// the injector before the Flusher is invoked. Where no provider push is
// wanted, register the named noop reporter.
func RegisterFlusher(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*metering.Flusher, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewFlusher(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[metering.Store](i),
			do.MustInvoke[metering.ProviderMapper](i),
			do.MustInvoke[capitalism.UsageReporter](i),
			WithPillars(pillars),
		)
	})
}
