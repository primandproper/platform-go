package conformance

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSubject_ScopeFor(t *testing.T) {
	t.Parallel()

	t.Run("a surface Scopes does not name reads Scope", func(t *testing.T) {
		t.Parallel()

		sub := &Subject{Scope: tenancy.Global()}

		test.EqOp(t, tenancy.Global(), sub.ScopeFor("billing"))
	})

	t.Run("a surface Scopes names reads its own entry", func(t *testing.T) {
		t.Parallel()

		account := tenancy.Of("account")
		sub := &Subject{
			Scope:  tenancy.Global(),
			Scopes: map[string]tenancy.Scope{"issuereports": account},
		}

		test.EqOp(t, account, sub.ScopeFor("issuereports"))
		test.EqOp(t, tenancy.Global(), sub.ScopeFor("billing"))
	})
}

func TestInTenant(t *testing.T) {
	t.Parallel()

	account := tenancy.Of("account")
	req := NewSubjectRequest(InTenant("issuereports", account))

	must.NotNil(t, req.Scope)
	test.EqOp(t, account, *req.Scope)
	test.EqOp(t, "issuereports", req.Surface)

	fresh := NewSubjectRequest()
	test.Nil(t, fresh.Scope)
	test.EqOp(t, "", fresh.Surface)
}

// accountsOnOneDirectory is a subject factory for a deployment whose issue
// reports are confined to the caller's account while its billing is served
// from the global scope — every caller shares one tenant there.
func accountsOnOneDirectory() *Session {
	var minted atomic.Int64

	return &Session{seams: Seams{NewSubject: func(context.Context, ...SubjectOption) (*Subject, error) {
		account := tenancy.Of(fmt.Sprintf("account-%d", minted.Add(1)))

		return &Subject{
			Scope:  tenancy.Global(),
			Scopes: map[string]tenancy.Scope{"issuereports": account},
		}, nil
	}}}
}

func TestSession_TwoTenants(t *testing.T) {
	t.Parallel()

	t.Run("callers apart on the surface are returned", func(t *testing.T) {
		t.Parallel()

		mine, theirs := accountsOnOneDirectory().TwoTenants(t, "issuereports")

		must.NotNil(t, mine)
		must.NotNil(t, theirs)
		test.NotEqOp(t, mine.ScopeFor("issuereports"), theirs.ScopeFor("issuereports"))
	})

	t.Run("a surface served from the global scope skips", func(t *testing.T) {
		t.Parallel()

		// A parallel subtest finishes before its parent's cleanups run, which is
		// what lets the parent read how it ended.
		var inner *testing.T
		t.Cleanup(func() { test.True(t, inner.Skipped()) })

		t.Run("billing", func(t *testing.T) {
			inner = t
			t.Parallel()

			accountsOnOneDirectory().TwoTenants(t, "billing")
			t.Error("TwoTenants returned for two callers sharing the global scope")
		})
	})
}

func TestSeparation(t *testing.T) {
	t.Parallel()

	test.EqOp(t, separate, separation(tenancy.Of("a"), tenancy.Of("b")))
	test.EqOp(t, separate, separation(tenancy.Of("a"), tenancy.Global()))
	test.EqOp(t, sharedGlobal, separation(tenancy.Global(), tenancy.Global()))
	test.EqOp(t, sharedTenant, separation(tenancy.Of("a"), tenancy.Of("a")))
}
