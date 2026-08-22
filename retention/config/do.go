package retentioncfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/retention"

	"github.com/samber/do/v2"
)

// RegisterSweeper registers a *retention.Sweeper with the injector.
//
// The audit.Recorder is resolved optionally: a container that registers one
// gets sweeps accounted for in the audit log, and one that does not gets a
// sweeper that still runs. That is the same distinction the rest of this
// module's DI draws — absent is fine, registered-but-failing is an error — and
// it is worth knowing which side of it a deployment is on, because the second
// half of this package's value is on the other side of that registration.
//
// Prerequisites: *Config, database.Client, and []retention.Policy must be
// registered in the injector before the Sweeper is invoked.
func RegisterSweeper(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*retention.Sweeper, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		recorder, err := injection.InvokeOptional[audit.Recorder](i)
		if err != nil {
			return nil, err
		}

		opts := []Option{WithPillars(pillars)}
		if recorder != nil {
			opts = append(opts, WithSweeperOptions(retention.WithSweeperAuditRecorder(recorder)))
		}

		return NewSweeper(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			do.MustInvoke[[]retention.Policy](i),
			opts...,
		)
	})
}
