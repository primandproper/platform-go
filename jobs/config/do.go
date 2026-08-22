package jobscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterPool registers a *jobs.Pool with the injector.
//
// Prerequisites: *PoolConfig and jobs.Handler (the application's dispatch
// function) must be registered in the injector before the Pool is invoked.
func RegisterPool(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*jobs.Pool, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewPool(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*PoolConfig](i),
			do.MustInvoke[jobs.Handler](i),
			WithPillars(pillars),
		)
	})
}

// RegisterScheduler registers a *jobs.Scheduler with the injector.
//
// Prerequisites: *SchedulerConfig and database.Client must be registered in
// the injector before the Scheduler is invoked.
func RegisterScheduler(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*jobs.Scheduler, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewScheduler(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*SchedulerConfig](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}
