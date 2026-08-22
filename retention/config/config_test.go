package retentioncfg

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/retention"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

func newTestClient(t *testing.T) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(), &testClientConfig{
		connectionString: filepath.Join(t.TempDir(), "retention.db"),
	})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return client
}

func testPolicies() []retention.Policy {
	return []retention.Policy{{
		Name:   "widgets",
		Target: retention.Table{Name: "widgets", Column: "created_at"},
		Age:    24 * time.Hour,
	}}
}

func TestConfig(T *testing.T) {
	T.Parallel()

	T.Run("EnsureDefaults reaches the nested sweeper config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, retention.DefaultBatchSize, cfg.Sweeper.BatchSize)
		test.EqOp(t, retention.DefaultMaxBatches, cfg.Sweeper.MaxBatches)
		test.EqOp(t, retention.DefaultBacklogCeiling, cfg.Sweeper.BacklogCeiling)
	})

	T.Run("validates a defaulted config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("the nested config is actually validated", func(t *testing.T) {
		t.Parallel()

		// ozzo dereferences a struct-value field before checking
		// ValidatableWithContext, so without the validation.By closure this
		// would pass.
		test.Error(t, (&Config{}).ValidateWithContext(t.Context()))
	})
}

func TestNewSweeper(T *testing.T) {
	T.Parallel()

	T.Run("builds a sweeper from a zero config", func(t *testing.T) {
		t.Parallel()

		sweeper, err := NewSweeper(t.Context(), &Config{}, newTestClient(t), testPolicies())
		must.NoError(t, err)
		must.NotNil(t, sweeper)

		must.SliceLen(t, 1, sweeper.Policies())
		test.EqOp(t, "widgets", sweeper.Policies()[0].Name)
	})

	T.Run("carries the configured knobs through", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Sweeper: retention.SweeperConfig{BatchSize: 25, BatchPause: time.Second}}

		sweeper, err := NewSweeper(t.Context(), cfg, newTestClient(t), testPolicies())
		must.NoError(t, err)
		must.NotNil(t, sweeper)

		test.EqOp(t, 25, cfg.Sweeper.BatchSize)
		test.EqOp(t, retention.DefaultMaxBatches, cfg.Sweeper.MaxBatches)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewSweeper(t.Context(), nil, newTestClient(t), testPolicies())
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("surfaces the leaf package's objection to a policy", func(t *testing.T) {
		t.Parallel()

		_, err := NewSweeper(t.Context(), &Config{}, newTestClient(t), nil)
		test.ErrorIs(t, err, retention.ErrNoPolicies)
	})

	T.Run("explicit options run after the config-derived ones", func(t *testing.T) {
		t.Parallel()

		// WithSweeperOptions is the only way the audit recorder gets in, so it
		// has to reach the constructor.
		var applied bool

		_, err := NewSweeper(t.Context(), &Config{}, newTestClient(t), testPolicies(),
			WithSweeperOptions(func(*retention.Sweeper) { applied = true }),
		)
		must.NoError(t, err)
		test.True(t, applied)
	})
}
