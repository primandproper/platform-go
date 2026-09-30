package oauth2serverstorecfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/internal/scheduledjob"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"

	"github.com/samber/do/v2"
)

const (
	// SweepJobName is the name the sweep job registers under, and so its lock
	// key.
	SweepJobName = "oauth2-server-sweep"

	// DefaultSweepJobLeaseTTL bounds one scheduled sweep: one statement per
	// table, in one transaction.
	DefaultSweepJobLeaseTTL = time.Minute

	// JobsKey is the name RegisterJobs registers the sweep job under.
	JobsKey = "oauth2serverstore.jobs"
)

// NewJobs renders the sweep job over store, for a jobs.Scheduler to run once
// across a fleet.
//
// It renders nothing under the memory provider, whose records live in each
// replica's own maps: a fleet-wide lock would sweep one replica's and leave the
// rest to their in-process sweep anyway, which is the only sweep that reaches
// them. A disabled job is left out of the result as well.
func NewJobs(ctx context.Context, cfg *Config, store oauth2server.Store) ([]jobs.Job, error) {
	if cfg == nil || store == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating oauth2 server config")
	}

	if cfg.provider() != ProviderDatabase {
		return nil, nil
	}

	return scheduledjob.Build(&cfg.SweepJob, SweepJobName, scheduledjob.Sweep(store.Sweep))
}

// RegisterJobs registers the jobs NewJobs renders under JobsKey, over the
// registered oauth2server.Store.
//
// Prerequisites: context.Context, *Config, and an oauth2server.Store (see
// RegisterStore).
func RegisterJobs(i do.Injector) {
	do.ProvideNamed(i, JobsKey, func(i do.Injector) ([]jobs.Job, error) {
		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		store, err := do.Invoke[oauth2server.Store](i)
		if err != nil {
			return nil, err
		}

		return NewJobs(ctx, cfg, store)
	})
}
