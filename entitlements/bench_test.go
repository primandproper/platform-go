package entitlements

import (
	"testing"

	"github.com/primandproper/platform-go/v13/authorization"
)

// Check is the entitlements counterpart to authorization's Grants.Has: a
// question asked on the way into a request, whose answer decides whether the
// request proceeds. The difference is that this one can miss a cache and go
// looking for the account's plan, so the rows below are organized around where
// the answer comes from rather than around what is being asked.
//
// The cached row is the one a steady-state service pays. The uncached row is
// what it pays on the first request after an invalidation, multiplied by
// however many requests arrive before the first one finishes.

// benchChecker builds a checker whose plan lookups are served from the
// in-memory assignment cache, which is the configuration a real deployment
// runs: resolving a plan is a database read, and doing it per request is what
// the cache exists to prevent.
func benchChecker(b *testing.B) *PlanChecker {
	b.Helper()

	return newChecker(b, staticPlans(planPro), WithCache(newAssignmentCache(b)))
}

func BenchmarkPlanChecker_Check(b *testing.B) {
	ctx := b.Context()
	checker := benchChecker(b)

	// Warm the assignment cache so the loop measures hits.
	_, err := checker.Check(ctx, "acct_01HZY0000000000000", featureSearch)
	if err != nil {
		b.Fatal(err)
	}

	// A boolean feature the plan includes: the cheapest yes there is.
	b.Run("allowed/cached", func(b *testing.B) {
		for b.Loop() {
			decisionSink, _ = checker.Check(ctx, "acct_01HZY0000000000000", featureSearch)
		}
	})

	// A feature the plan does not include. This is a refusal rather than a
	// fault, and it costs materially more than the grant does — the catalog
	// lookup fails, and the decision is then assembled along the path that
	// records why. Worth knowing, because which accounts ask for features they
	// do not have is not entirely up to the service.
	b.Run("denied/cached", func(b *testing.B) {
		for b.Loop() {
			decisionSink, _ = checker.Check(ctx, "acct_01HZY0000000000000", "no_such_feature")
		}
	})

	// A distinct account per iteration, so every call misses the assignment
	// cache and resolves the plan afresh. This is the cold path, and the reason
	// the cache is not optional in practice.
	b.Run("allowed/uncached", func(b *testing.B) {
		var i int
		for b.Loop() {
			i++
			decisionSink, _ = checker.Check(ctx, "acct_"+itoa(i), featureSearch)
		}
	})
}

// BenchmarkPlanChecker_CheckQuantity prices the quota path, which asks the
// metering enforcer as well as the catalog and is therefore the more expensive
// of the two questions this package answers.
func BenchmarkPlanChecker_CheckQuantity(b *testing.B) {
	ctx := b.Context()
	checker := benchChecker(b)

	_, err := checker.CheckQuantity(ctx, "acct_01HZY0000000000000", featureSearch, 1)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("quantity=1", func(b *testing.B) {
		for b.Loop() {
			decisionSink, _ = checker.CheckQuantity(ctx, "acct_01HZY0000000000000", featureSearch, 1)
		}
	})

	b.Run("quantity=100", func(b *testing.B) {
		for b.Loop() {
			decisionSink, _ = checker.CheckQuantity(ctx, "acct_01HZY0000000000000", featureSearch, 100)
		}
	})
}

// BenchmarkPlanChecker_Permissions prices the bulk question — everything this
// account is entitled to — which a session builds once rather than per check.
//
// It is cheaper than a single Check, which is the useful and slightly
// surprising result: a caller that will ask about more than one feature is
// better off materializing the whole set than asking twice, and a session that
// already holds one should be consulted rather than re-checked.
func BenchmarkPlanChecker_Permissions(b *testing.B) {
	ctx := b.Context()
	checker := benchChecker(b)

	for b.Loop() {
		permissionsSink, _ = checker.Permissions(ctx, "acct_01HZY0000000000000")
	}
}

// itoa is a tiny non-allocating-ish integer formatter for building distinct
// account identifiers inside a benchmark loop, kept local so the loop does not
// pay for strconv's generality on a value that is never large.
func itoa(i int) string {
	var buf [20]byte

	pos := len(buf)
	for {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10

		if i == 0 {
			break
		}
	}

	return string(buf[pos:])
}

var (
	decisionSink    *Decision
	permissionsSink *authorization.PermissionSet
)
