package meteringcfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/metering"

	"github.com/primandproper/primitives-go/analytics"
	"github.com/primandproper/primitives-go/cache"
	"github.com/primandproper/primitives-go/capitalism"
	"github.com/primandproper/primitives-go/config/injection"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/observability"

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
// The analytics reporter is optional: absent, usage events are recorded and
// mirrored nowhere, which is metering.WithRecorderAnalytics' documented default.
// A deployment that wants the mirror registers an analytics.EventReporter — the
// named noop is for one that wants the wiring without the traffic.
//
// Prerequisites: *Config, metering.Store (see RegisterStore),
// *metering.Registry (the application's meter definitions), and
// metering.PeriodResolver must be registered in the injector before the Recorder
// is invoked.
func RegisterRecorder(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*metering.DurableRecorder, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		reporter, err := injection.InvokeOptional[analytics.EventReporter](i)
		if err != nil {
			return nil, err
		}

		opts := []Option{WithPillars(pillars)}
		if reporter != nil {
			opts = append(opts, WithRecorderAnalytics(reporter))
		}

		return NewRecorder(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[metering.Store](i),
			do.MustInvoke[*metering.Registry](i),
			do.MustInvoke[metering.PeriodResolver](i),
			opts...,
		)
	})
}

// RegisterEnforcer registers a *metering.Enforcer with the injector. The totals
// cache and the quota source are both optional: without the cache the enforcer
// reads the store on every decision, which is metering.NewEnforcer's documented
// uncached behavior, and without a quota source the Registry's static quotas
// serve every subject. Register entitlementscfg.NewQuotaSource's output as a
// metering.QuotaSource where the catalog's plan limits are the enforced ones.
//
// Prerequisites: *Config, database.Client, metering.Store (see RegisterStore),
// *metering.Registry, and metering.PeriodResolver must be registered in the
// injector before the Enforcer is invoked.
func RegisterEnforcer(i do.Injector) {
	do.Provide(i, func(i do.Injector) (metering.Enforcer, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		quotas, err := injection.InvokeOptional[metering.QuotaSource](i)
		if err != nil {
			return nil, err
		}

		totals, err := injection.InvokeOptional[cache.Cache[metering.CachedTotal]](i)
		if err != nil {
			return nil, err
		}

		opts := []Option{WithPillars(pillars)}
		if quotas != nil {
			opts = append(opts, WithEnforcerQuotaSource(quotas))
		}
		if totals != nil {
			opts = append(opts, WithEnforcerCache(totals))
		}

		// Built into a variable and returned only once err is known to be nil:
		// NewEnforcer returns a *metering.QuotaEnforcer, and returning it
		// straight through would register a non-nil metering.Enforcer wrapping a
		// nil pointer whenever construction failed.
		enforcer, err := NewEnforcer(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			do.MustInvoke[metering.Store](i),
			do.MustInvoke[*metering.Registry](i),
			do.MustInvoke[metering.PeriodResolver](i),
			opts...,
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
