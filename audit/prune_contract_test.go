package audit_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v10/audit"
	"github.com/primandproper/platform-go/v10/retention"

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
