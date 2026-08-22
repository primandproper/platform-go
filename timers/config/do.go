package timerscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/timers"

	"github.com/samber/do/v2"
)

// RegisterTimers registers a *timers.Timers[K] with the injector. It is generic
// because a set schedules against one concrete key type; an application running
// two kinds of timer registers each separately.
//
// Prerequisites: *Config and database.Client must be registered in the injector
// before the set is invoked.
//
// This is the registration a process that only schedules wants — an application
// server writing timers that a separate worker fleet fires. It starts nothing
// and needs no shutdown.
func RegisterTimers[K comparable](i do.Injector) {
	do.Provide(i, func(i do.Injector) (*timers.Timers[K], error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewTimers[K](
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}

// RegisterWorker registers a *timers.Worker[K] built over the registered
// *timers.Timers[K], so the two share one set and one set of metrics.
//
// Prerequisites: everything RegisterTimers needs, plus RegisterTimers itself and
// a timers.Handler[K]. The handler is registered rather than passed because it
// is the one dependency that is genuinely the application's — a container that
// resolves a worker has to be able to say what firing means.
//
// A Worker starts nothing on its own: Run blocks, and the injector will not call
// it. Run it from wherever you start the rest of your background work, and stop
// it by cancelling that context.
func RegisterWorker[K comparable](i do.Injector) {
	do.Provide(i, func(i do.Injector) (*timers.Worker[K], error) {
		return timers.NewWorker(
			do.MustInvoke[context.Context](i),
			&do.MustInvoke[*Config](i).Worker,
			do.MustInvoke[*timers.Timers[K]](i),
			do.MustInvoke[timers.Handler[K]](i),
		)
	})
}
