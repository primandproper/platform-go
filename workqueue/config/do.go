package workqueuecfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/workqueue"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterQueue registers a *workqueue.Queue[K] with the injector. It is
// generic because a queue schedules work for one concrete key type; an
// application draining two kinds of work registers each separately.
//
// Prerequisites: *workqueue.Config and database.Client must be registered in the
// injector before the Queue is invoked.
//
// A Queue owns a goroutine and has to be Closed, and the injector will not do
// it: do recognizes a Shutdown method, and this module's background components
// spell that Close. Close it from the same place you shut the rest of them down
// — after ingress is gone, so a request still in flight can finish enqueueing.
func RegisterQueue[K comparable](i do.Injector) {
	do.Provide(i, func(i do.Injector) (*workqueue.Queue[K], error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewQueue[K](
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*workqueue.Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}

// RegisterRunner registers a *workqueue.Runner[K] built over the registered
// *workqueue.Queue[K], so the loop and whatever enqueues share one queue and one
// set of metrics.
//
// Prerequisites: everything RegisterQueue needs, plus RegisterQueue itself, a
// *workqueue.RunnerConfig and a workqueue.Handler[K]. The handler is registered
// rather than passed because it is the one dependency that is genuinely the
// application's — a container that resolves a runner has to be able to say what
// the work is.
//
// A Runner starts nothing on its own: Run blocks, and the injector will not call
// it. Run it from wherever you start the rest of your background work, and stop
// it by cancelling that context — which drains the batch it is holding rather
// than abandoning it.
func RegisterRunner[K comparable](i do.Injector) {
	do.Provide(i, func(i do.Injector) (*workqueue.Runner[K], error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewRunner[K](
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*workqueue.RunnerConfig](i),
			do.MustInvoke[*workqueue.Queue[K]](i),
			do.MustInvoke[workqueue.Handler[K]](i),
			WithPillars(pillars),
		)
	})
}
