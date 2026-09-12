package auditcfg

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"

	"github.com/caarlos0/env/v11"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The retention block is an audit.RetentionConfig nested with no envPrefix of
// its own, so each of its fields is a variable at this Config's prefix. This
// pins the four names, because a field whose env name abbreviates the field is
// a deployment break rather than a compile break: nothing stops the process
// starting, the knob is simply never read.
//
// service.Config nests this one at envPrefix:"AUDIT_", so that is the prefix
// parsed under. The environment is supplied to the parser rather than to the
// process, so the test stays parallel-safe and does not read whatever the
// developer happens to have exported.
func TestConfigEnvironmentNames(T *testing.T) {
	T.Parallel()

	T.Run("every retention knob is reachable at its spelled-out name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "AUDIT_",
			Environment: map[string]string{
				"AUDIT_DIALECT":         "sqlite",
				"AUDIT_TABLE_PREFIX":    "ddb",
				"AUDIT_BASIS":           "a regulation names it",
				"AUDIT_RETENTION":       "720h",
				"AUDIT_BATCH_SIZE":      "250",
				"AUDIT_SCOPE_PAGE_SIZE": "7",
			},
		}))

		test.EqOp(t, "ddb", cfg.TablePrefix)
		test.EqOp(t, "a regulation names it", cfg.Retention.Basis)
		test.EqOp(t, 720*time.Hour, cfg.Retention.Retention)
		test.EqOp(t, 250, cfg.Retention.BatchSize)
		test.EqOp(t, 7, cfg.Retention.ScopePageSize)
	})

	// The name the field carried before was SCOPE_PAGE, which parses as nothing
	// now. The assertion is that it is not read: a deployment still setting it
	// gets the default, and the miss is visible as the default rather than as a
	// value silently landing on a field it no longer names.
	T.Run("the abbreviated name is not a second spelling of the same knob", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix:      "AUDIT_",
			Environment: map[string]string{"AUDIT_SCOPE_PAGE": "7"},
		}))

		test.EqOp(t, 0, cfg.Retention.ScopePageSize)

		cfg.EnsureDefaults()
		test.EqOp(t, audit.DefaultScopePageSize, cfg.Retention.ScopePageSize)
	})
}
