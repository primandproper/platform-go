package workqueuecfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/workqueue"

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
