package waitlists

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// inSuite runs body as the one assertion of a suite over a deployment whose
// visitors land in the global scope and whose n-th caller is minted into the
// waitlists tenant scopeFor answers, and hands back the test it ran as once the
// run is over.
func inSuite(t *testing.T, scopeFor func(n int64) tenancy.Scope, body func(t *testing.T, s *conformance.Session)) *testing.T {
	t.Helper()

	var (
		minted atomic.Int64
		inner  *testing.T
	)

	seams := conformance.Seams{
		VisitorScope: new(tenancy.Global()),
		NewSubject: func(context.Context, ...conformance.SubjectOption) (*conformance.Subject, error) {
			return &conformance.Subject{
				Scope:  tenancy.Global(),
				Scopes: map[string]tenancy.Scope{surface: scopeFor(minted.Add(1))},
			}, nil
		},
	}

	// Run's suites are parallel subtests, and t.Run returns only once its
	// parallel subtests have finished, which is what lets inner be read after.
	t.Run("run", func(t *testing.T) {
		conformance.Run(t, seams, conformance.Suite{
			Name: surface,
			Run: func(t *testing.T, s *conformance.Session) {
				t.Helper()

				inner = t
				body(t, s)
			},
		})
	})

	return inner
}

func TestElsewhere(t *testing.T) {
	t.Parallel()

	t.Run("a tenant apart from the one visitors land in is returned", func(t *testing.T) {
		t.Parallel()

		perAccount := func(n int64) tenancy.Scope { return tenancy.Of(fmt.Sprintf("account-%d", n)) }

		var got *conformance.Subject

		inner := inSuite(t, perAccount, func(t *testing.T, s *conformance.Session) {
			t.Helper()

			got = elsewhere(t, s, waitlistspb.WaitlistsService_CreateList_FullMethodName)
		})

		must.NotNil(t, inner)
		test.False(t, inner.Skipped())
		must.NotNil(t, got)
		test.NotEqOp(t, tenancy.Global(), got.ScopeFor(surface))
	})

	t.Run("a deployment serving waitlists from the global scope skips", func(t *testing.T) {
		t.Parallel()

		global := func(int64) tenancy.Scope { return tenancy.Global() }

		reached := false

		inner := inSuite(t, global, func(t *testing.T, s *conformance.Session) {
			t.Helper()

			elsewhere(t, s, waitlistspb.WaitlistsService_CreateList_FullMethodName)
			reached = true
		})

		must.NotNil(t, inner)
		test.True(t, inner.Skipped(), test.Sprint("an operator in the global scope was taken for one in another tenant"))
		test.False(t, reached)
	})
}
