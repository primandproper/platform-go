package sagacfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/retention"
	"github.com/primandproper/platform-go/v14/saga"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/jobs"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRetentionConfig(T *testing.T) {
	T.Parallel()

	T.Run("defaults to the ruled windows and an hourly job", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()

		test.EqOp(t, 7*24*time.Hour, cfg.Retention.Completed)
		test.EqOp(t, 30*24*time.Hour, cfg.Retention.Compensated)
		test.EqOp(t, DefaultRetentionInterval, cfg.Retention.Job.Interval)
		test.EqOp(t, DefaultRetentionLeaseTTL, cfg.Retention.Job.LeaseTTL)
		test.False(t, cfg.Retention.Job.Disabled)
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a window under an hour", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Retention.Completed = time.Minute

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewRetentionPolicies(T *testing.T) {
	T.Parallel()

	T.Run("one policy per terminal status, each with its own window", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{TablePrefix: "app", Retention: RetentionConfig{Completed: 2 * time.Hour}}

		policies, err := NewRetentionPolicies(cfg)
		must.NoError(t, err)
		must.SliceLen(t, 2, policies)

		test.EqOp(t, CompletedRetentionPolicyName, policies[0].Name)
		test.EqOp(t, 2*time.Hour, policies[0].Age)
		test.Eq(t, retention.Target(saga.RetentionTarget{TablePrefix: "app", Status: saga.StatusCompleted}), policies[0].Target)

		test.EqOp(t, CompensatedRetentionPolicyName, policies[1].Name)
		test.EqOp(t, saga.DefaultCompensatedRetention, policies[1].Age)
		test.Eq(t, retention.Target(saga.RetentionTarget{TablePrefix: "app", Status: saga.StatusCompensated}), policies[1].Target)
	})

	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewRetentionPolicies(nil)
		test.Error(t, err)
	})
}

func TestNewJobs(T *testing.T) {
	T.Parallel()

	T.Run("renders a retention job that sweeps the table", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)
		cfg := validConfig()
		migrate(t, client, cfg.TablePrefix)

		built, err := NewJobs(t.Context(), cfg, client)
		must.NoError(t, err)
		must.SliceLen(t, 1, built)

		test.EqOp(t, RetentionJobName, built[0].Name)
		test.EqOp(t, DefaultRetentionInterval, built[0].Interval)
		test.NoError(t, built[0].Run(t.Context()))
	})

	T.Run("renders nothing when disabled", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Retention: RetentionConfig{Job: jobscfg.JobConfig{Disabled: true}}}

		built, err := NewJobs(t.Context(), cfg, newClient(t))
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})

	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), nil, newClient(t))
		test.Error(t, err)
	})

	T.Run("a nil client is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), validConfig(), nil)
		test.Error(t, err)
	})
}

func TestRegisterJobs(T *testing.T) {
	T.Parallel()

	T.Run("registers the retention job under its key", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterJobs(i)

		built, err := do.InvokeNamed[[]jobs.Job](i, JobsKey)
		must.NoError(t, err)
		must.SliceLen(t, 1, built)
		test.EqOp(t, RetentionJobName, built[0].Name)
	})
}
