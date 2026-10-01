// Package scheduledjob is the one place the self-scheduling stores turn their
// jobscfg.JobConfig into the jobs a scheduler registers.
//
// A store that owns a reaper — operations' recovery and reap, saga's retention,
// the token tables' sweeps — carries its job's configuration in its own config
// package, defaulted on, so that service.Register can schedule it without the
// application having remembered to. Two halves of that are easy to get wrong
// once per package, and so live here rather than beside each store.
//
// The defaulting has to leave a job the deployment shaped alone: a JobConfig
// with an Interval of its own, or a Schedule, is refused if it also acquires
// the other, so a default may fill in a frequency only where neither was said.
// And a Disabled job is skipped rather than rendered, since jobscfg.JobConfig.Job
// does not consult Disabled and a scheduler has no notion of a job that never
// fires.
package scheduledjob

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/jobs"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"
)

// EnsureDefaults gives job a frequency and a lease when the deployment named
// neither. A job whose Schedule or Interval was configured keeps it, and a
// LeaseTTL the deployment set is kept too.
func EnsureDefaults(job *jobscfg.JobConfig, interval, leaseTTL time.Duration) {
	if job.Schedule == "" && job.Interval == 0 {
		job.Interval = interval
	}

	if job.LeaseTTL == 0 {
		job.LeaseTTL = leaseTTL
	}
}

// Validate validates job as EnsureDefaults would leave it.
//
// A job that names neither a Schedule nor an Interval is one its config's
// EnsureDefaults will give a frequency, and an unset field that has a default
// is not a validation failure: a config validated before its defaults are
// applied has to pass. Every other rule — both set at once, a schedule that
// does not parse, a lease under a second — is checked as jobscfg checks it.
func Validate(ctx context.Context, job *jobscfg.JobConfig) error {
	probe := *job
	if probe.Schedule == "" && probe.Interval == 0 {
		probe.Interval = time.Second
	}

	return probe.ValidateWithContext(ctx)
}

// Build renders job under name, running run, or renders nothing when the job
// is Disabled.
func Build(job *jobscfg.JobConfig, name string, run func(context.Context) error) ([]jobs.Job, error) {
	if job.Disabled {
		return nil, nil
	}

	rendered, err := job.Job(name, run)
	if err != nil {
		return nil, err
	}

	return []jobs.Job{rendered}, nil
}

// Sweep adapts a sweep that reports what it removed to the shape a job runs.
// The count is the sweep's own telemetry already; a scheduler has nowhere to
// put it.
func Sweep(sweep func(context.Context) (int64, error)) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := sweep(ctx)

		return err
	}
}
