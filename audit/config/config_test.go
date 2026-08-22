package auditcfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func validConfig() *Config {
	return &Config{Dialect: dialect.SQLite}
}

func TestConfig(T *testing.T) {
	T.Parallel()

	T.Run("EnsureDefaults reaches the nested config", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()

		test.EqOp(t, audit.DefaultTablePrefix, cfg.TablePrefix)
		test.EqOp(t, audit.DefaultRetention, cfg.Retention.Retention)
		test.EqOp(t, audit.DefaultRetentionBatchSize, cfg.Retention.BatchSize)
	})

	T.Run("ValidateWithContext reaches the nested config", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()
		cfg.Retention.Retention = time.Second

		// ozzo dereferences a struct-value field before checking
		// ValidatableWithContext, so this only passes because of the By closure.
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Dialect: "cassandra"}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a table prefix that would not render", func(t *testing.T) {
		t.Parallel()

		// One field, checked once, feeding the Recorder, the Reader, and the
		// prune target — which is why it lives on this Config rather than on
		// any one of theirs.
		cfg := &Config{Dialect: dialect.SQLite, TablePrefix: "audit-"}
		cfg.EnsureDefaults()

		// Named rather than matched with errors.Is: ozzo renders a field's
		// error into its own Errors map by message, so the chain does not
		// survive validation.
		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), audit.ErrInvalidTablePrefix.Error())
	})
}

func TestNewRecorder(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		recorder, err := NewRecorder(t.Context(), validConfig())
		must.NoError(t, err)
		test.NotNil(t, recorder)
	})

	T.Run("registers the configured redactions", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Redactions = map[string]audit.Redaction{
			"user": {Drop: []string{"passwordHash"}},
		}

		recorder, err := NewRecorder(t.Context(), cfg)
		must.NoError(t, err)
		test.NotNil(t, recorder)
	})

	T.Run("passes the observability dependencies through", func(t *testing.T) {
		t.Parallel()

		recorder, err := NewRecorder(
			t.Context(),
			validConfig(),
		)
		must.NoError(t, err)
		test.NotNil(t, recorder)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewRecorder(t.Context(), nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("rejects an invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := NewRecorder(t.Context(), &Config{})
		test.Error(t, err)
	})
}

func TestNewReader(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(t.Context(), validConfig(), stubClient{})
		must.NoError(t, err)
		test.NotNil(t, reader)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewReader(t.Context(), nil, stubClient{})
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewReader(t.Context(), validConfig(), nil)
		test.ErrorIs(t, err, audit.ErrNilDatabaseClient)
	})

	T.Run("passes the observability dependencies through", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(
			t.Context(),
			validConfig(),
			stubClient{},
		)
		must.NoError(t, err)
		test.NotNil(t, reader)
	})
}

func TestNewPruneTarget(T *testing.T) {
	T.Parallel()

	T.Run("carries the configured prefix and page", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Dialect: dialect.SQLite, TablePrefix: "ddb"}
		cfg.Retention.ScopePageSize = 7

		target, err := NewPruneTarget(t.Context(), cfg)
		must.NoError(t, err)

		// The prefix is the one the Recorder and Reader take, which is the
		// point of it being a field of this Config.
		test.EqOp(t, "ddb_audit_log_entries", target.Describe())
		test.EqOp(t, 7, target.ScopePageSize)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewPruneTarget(t.Context(), nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("rejects an invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := NewPruneTarget(t.Context(), &Config{})
		test.Error(t, err)
	})
}

func TestNewRetentionPolicy(T *testing.T) {
	T.Parallel()

	T.Run("carries the window, the bound, and the basis", func(t *testing.T) {
		t.Parallel()

		policy, err := NewRetentionPolicy(t.Context(), validConfig())
		must.NoError(t, err)

		test.EqOp(t, audit.DefaultRetentionPolicyName, policy.Name)
		test.EqOp(t, audit.DefaultRetention, policy.Age)
		test.EqOp(t, audit.DefaultRetentionBatchSize, policy.BatchSize)
		test.EqOp(t, audit.DefaultRetentionBasis, policy.Basis)
		test.EqOp(t, "audit_log_entries", policy.Target.Describe())

		// Empty: a fleet-wide sweep belongs to no tenant, and the empty scope
		// is the chain platform-level events are recorded in.
		test.EqOp(t, "", policy.Scope)
	})

	T.Run("takes a configured window and basis", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Retention.Retention = 30 * 24 * time.Hour
		cfg.Retention.Basis = "a regulation somebody can name"

		policy, err := NewRetentionPolicy(t.Context(), cfg)
		must.NoError(t, err)

		test.EqOp(t, 30*24*time.Hour, policy.Age)
		test.EqOp(t, "a regulation somebody can name", policy.Basis)
	})

	T.Run("refuses a window shorter than an hour", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()
		cfg.Retention.Retention = time.Second

		// retention.Policy would accept it — a zero age is legal there — so
		// this floor has to be enforced on the way to building the policy.
		_, err := NewRetentionPolicy(t.Context(), cfg)
		test.Error(t, err)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewRetentionPolicy(t.Context(), nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})
}

// stubClient satisfies database.Client for the constructors, which only hold
// onto it. Every method panics, so a constructor that started issuing queries
// would fail loudly rather than be covered by accident.
type stubClient struct{}

var _ database.Client = (*stubClient)(nil)

func (stubClient) Reader() database.SQLQueryExecutor { panic("unexpected read") }
func (stubClient) Writer() database.SQLQueryExecutor { panic("unexpected write") }

func (stubClient) WithTransaction(context.Context, func(database.SQLQueryExecutor) error) error {
	panic("unexpected transaction")
}

func (stubClient) Dialect() dialect.Dialect { return dialect.SQLite }

func (stubClient) Close() error           { return nil }
func (stubClient) CurrentTime() time.Time { panic("unexpected clock read") }

// Both component constructors attach a logger, tracer provider, and metrics
// provider only when they were given one, so that an absent dependency stays
// absent rather than becoming an option carrying nil. Those branches are what
// WithPillars exists to feed.
func TestConstructors_observabilityIsAttachedWhenSupplied(T *testing.T) {
	T.Parallel()

	pillars := func() *observability.Pillars {
		return &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}
	}

	T.Run("NewRecorder", func(t *testing.T) {
		t.Parallel()

		recorder, err := NewRecorder(t.Context(), validConfig(), WithPillars(pillars()))
		must.NoError(t, err)
		test.NotNil(t, recorder)
	})

	T.Run("NewReader", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(t.Context(), validConfig(), stubClient{}, WithPillars(pillars()))
		must.NoError(t, err)
		test.NotNil(t, reader)
	})
}
