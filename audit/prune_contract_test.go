package audit_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/retention"

	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
)

// PruneTarget satisfies retention.Target, and this is where that is checked.
//
// It cannot be checked in package audit. retention imports audit — it records
// an entry accounting for every sweep — so an import the other way would close
// a cycle, which is why the target satisfies the interface structurally rather
// than declaring it. An external test package has no such constraint: nothing
// imports audit_test, so it may import retention freely.
//
// This assertion is the whole reason this file exists. A method here that drifts
// from the interface — a renamed parameter type, a dropped dialect argument —
// is a build failure at this line rather than a policy set that will not
// compile in somebody else's application.
var _ retention.Target = audit.PruneTarget{}

func TestPruneTarget_IsARetentionTarget(T *testing.T) {
	T.Parallel()

	T.Run("assembles into a policy", func(t *testing.T) {
		t.Parallel()

		policy := retention.Policy{
			Name:   audit.DefaultRetentionPolicyName,
			Target: audit.PruneTarget{},
			Age:    audit.DefaultRetention,
			Basis:  audit.DefaultRetentionBasis,
		}

		test.EqOp(t, audit.DefaultRetentionPolicyName, policy.Name)
		test.EqOp(t, audit.DefaultRetentionBasis, policy.Basis)
		test.EqOp(t, "audit_log_entries", policy.Target.Describe())
		test.EqOp(t, 7*365*24*time.Hour, policy.Age)
	})
}

// The declarative target must refuse these tables, and this is where the two
// packages are checked against each other.
//
// retention.Table.Validate asks audit.IsAuditTable, so the names are spelled
// once — but "spelled once" is a claim about a call, and what a policy author
// actually types is the table name. This drives the refusal from the same
// prefixes a PruneTarget renders, so a rename that moved both the constant and
// the recognizer, and left the refusal matching nothing, fails here.
func TestPruneTarget_IsWhatSweepsTheseTables(T *testing.T) {
	T.Parallel()

	T.Run("a retention.Table pointed at them does not validate", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"", "ddb"} {
			name := audit.PruneTarget{TablePrefix: prefix}.Describe()

			err := retention.Table{Name: name, Column: "recorded_at"}.Validate(dialect.Postgres)
			test.ErrorIs(t, err, retention.ErrChainedTable, test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("the target itself still does", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, audit.PruneTarget{TablePrefix: "ddb"}.Validate(dialect.Postgres))
	})
}
