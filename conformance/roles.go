package conformance

import (
	"testing"
)

// Roles is the deployment's role vocabulary: the names the identity and signin
// suites grant, for a deployment that refuses a name it does not declare.
//
// Role names are the consumer's. Nothing here asserts what a role permits, only
// that the name granted is the name held, so any name the deployment accepts
// will do. What no suite can do is guess one: a deployment whose roles are a
// closed vocabulary — a foreign key onto a table of them, a validator reading a
// list — refuses every name it did not declare, and a refused grant reads as a
// broken invitation, membership or registration rather than as the suite having
// spoken a language the deployment does not.
//
// Each field left empty is this package's own literal, so a deployment whose
// vocabulary is open supplies nothing and changes nothing.
type Roles struct {
	// Owner is the account role a registrant is given over the account they
	// register with. Empty is "owner".
	Owner string

	// Service is a service role an operator may grant a user. Empty is
	// "operator".
	Service string

	// Membership is two account roles an account's owner may assign a member.
	// Two, and distinct, because "setting a member's roles replaces rather
	// than merges" is only observable by setting one where both were held.
	// Both empty is "support" and "billing"; naming one and not the other is a
	// Run failure rather than a pair of vocabularies spliced together.
	Membership [2]string
}

const (
	defaultOwnerRole   = "owner"
	defaultServiceRole = "operator"
)

// defaultMembershipRoles is Roles.Membership where the subject named neither.
var defaultMembershipRoles = [2]string{"support", "billing"}

// resolved is r with each empty field replaced by its literal.
func (r Roles) resolved() Roles {
	if r.Owner == "" {
		r.Owner = defaultOwnerRole
	}

	if r.Service == "" {
		r.Service = defaultServiceRole
	}

	if r.Membership == [2]string{} {
		r.Membership = defaultMembershipRoles
	}

	return r
}

// problem is why Run refuses r, or empty where it does not.
func (r Roles) problem() string {
	switch first, second := r.Membership[0], r.Membership[1]; {
	case first == "" && second == "":
		return ""
	case first == "" || second == "":
		return "Seams.Roles.Membership names one role and not the other; name two the deployment accepts, or neither"
	case first == second:
		return "Seams.Roles.Membership names " + first + " twice; the assertion that roles are replaced rather than merged needs two distinct roles"
	default:
		return ""
	}
}

// checkRoles fails the run on a vocabulary no assertion could be made with.
func checkRoles(t *testing.T, roles Roles) {
	t.Helper()

	if problem := roles.problem(); problem != "" {
		t.Fatal("conformance: " + problem)
	}
}

// Roles is the subject's role vocabulary, with this package's literals wherever
// it named none.
func (s *Session) Roles() Roles { return s.seams.Roles.resolved() }
