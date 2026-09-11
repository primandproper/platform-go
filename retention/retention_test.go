package retention

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubTarget is a Target that does whatever a test tells it to, so the
// Sweeper's batching, error handling, and accounting can be driven without a
// table behaving in exactly the awkward way each case needs.
type stubTarget struct {
	sweepFunc    func(limit int) (int64, error)
	backlogFunc  func(ceiling int) (int64, error)
	validateErr  error
	name         string
	sweepCalls   int
	backlogCalls int
}

var _ Target = (*stubTarget)(nil)

func (s *stubTarget) Describe() string { return s.name }

func (s *stubTarget) Validate(dialect.Dialect) error { return s.validateErr }

func (s *stubTarget) Sweep(_ context.Context, _ database.Tx, _ dialect.Dialect, _ time.Time, limit int) (int64, error) {
	s.sweepCalls++

	if s.sweepFunc == nil {
		return 0, nil
	}

	return s.sweepFunc(limit)
}

func (s *stubTarget) Backlog(_ context.Context, _ database.SQLQueryExecutor, _ dialect.Dialect, _ time.Time, ceiling int) (int64, error) {
	s.backlogCalls++

	if s.backlogFunc == nil {
		return 0, nil
	}

	return s.backlogFunc(ceiling)
}

func TestPolicy_validate(T *testing.T) {
	T.Parallel()

	target := Table{Name: "widgets", Column: "created_at"}

	T.Run("accepts a complete policy", func(t *testing.T) {
		t.Parallel()

		policy := Policy{Name: "widgets", Scope: tenancy.Global(), Target: target, Age: 24 * time.Hour}
		must.NoError(t, policy.validate(dialect.SQLite))
	})

	T.Run("accepts a zero age", func(t *testing.T) {
		t.Parallel()

		// Zero is the correct age for a policy measured from an expires_at:
		// the row was already dead at the instant in the column, and the age is
		// the grace period after it.
		policy := Policy{Name: "tokens", Scope: tenancy.Global(), Target: Table{Name: "tokens", Column: "expires_at"}}
		must.NoError(t, policy.validate(dialect.Postgres))
	})

	T.Run("rejects an unnamed policy", func(t *testing.T) {
		t.Parallel()

		policy := Policy{Target: target, Age: time.Hour}
		test.ErrorIs(t, policy.validate(dialect.SQLite), ErrInvalidPolicy)
	})

	T.Run("rejects a policy with no target", func(t *testing.T) {
		t.Parallel()

		policy := Policy{Name: "widgets", Scope: tenancy.Global(), Age: time.Hour}
		test.ErrorIs(t, policy.validate(dialect.SQLite), ErrNilTarget)
	})

	T.Run("rejects negative knobs", func(t *testing.T) {
		t.Parallel()

		for name, policy := range map[string]Policy{
			"age":        {Name: "widgets", Scope: tenancy.Global(), Target: target, Age: -time.Hour},
			"batch size": {Name: "widgets", Scope: tenancy.Global(), Target: target, BatchSize: -1},
			"batch cap":  {Name: "widgets", Scope: tenancy.Global(), Target: target, MaxBatches: -1},
		} {
			test.ErrorIs(t, policy.validate(dialect.SQLite), ErrInvalidPolicy, test.Sprintf("negative %s", name))
		}
	})

	T.Run("surfaces the target's own objection, named", func(t *testing.T) {
		t.Parallel()

		policy := Policy{Name: "widgets", Scope: tenancy.Global(), Target: Table{Name: "not an identifier", Column: "created_at"}}

		err := policy.validate(dialect.SQLite)
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
		test.StrContains(t, err.Error(), `"widgets"`)
	})

	T.Run("requires a scope", func(t *testing.T) {
		t.Parallel()

		// A policy that named no scope validates, registers, sweeps, and then
		// fails on the entry that accounts for what it deleted. The rows are
		// gone by then. A fleet-wide sweep names tenancy.Global, which is a
		// scope like any other and the one platform-level events are recorded
		// in.
		policy := Policy{Name: "widgets", Target: target, Age: time.Hour}

		test.ErrorIs(t, policy.validate(dialect.SQLite), tenancy.ErrNoScope)

		policy.Scope = tenancy.Global()
		test.NoError(t, policy.validate(dialect.SQLite))
	})
}
