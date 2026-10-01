// Package people mints the pair a person-owned confinement is asserted
// between: two different people in one directory.
//
// A privacy request and the operation fulfilling it are both confined to the
// person they are about, so the dataprivacy and operations suites owe the same
// neighbor the same refusal, and mint them the same way.
package people

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test/must"
)

// Two mints two different people in one directory: a caller, and a colleague
// in the caller's tenant on surface — or beside them in the global scope, where
// a deployment serves that surface from one.
//
// Not two tenants. A row confined to the person it is about owes its refusal to
// the neighbor sharing everything but the person; minting them apart would let
// a tenant wall answer for a subject check that is missing, and would skip
// every assertion on a deployment with no tenants at all, which is where two
// users in one directory are commonest. The wall between tenants is asserted on
// its own, with Session.TwoTenants.
//
// It refuses to proceed if the subject handed back one caller twice — every
// confinement assertion would then compare a person with themselves and pass.
//
// Each is minted Making the calls named for it, which are the calls it goes on
// to make.
func Two(t *testing.T, s *conformance.Session, surface string, mineCalls, theirCalls []string) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine = s.Subject(t, conformance.Making(mineCalls...))
	theirs = s.Subject(t, conformance.Making(theirCalls...), conformance.InTenant(surface, mine.ScopeFor(surface)))

	must.StrNotEqFold(t, mine.UserID, theirs.UserID, must.Sprint("the subject minted two callers as one user"))

	return mine, theirs
}
