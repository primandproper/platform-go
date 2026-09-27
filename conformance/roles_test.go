package conformance

import (
	"testing"

	"github.com/shoenig/test"
)

func TestSession_Roles(t *testing.T) {
	t.Parallel()

	t.Run("a subject that names no vocabulary gets the literals", func(t *testing.T) {
		t.Parallel()

		roles := (&Session{}).Roles()

		test.EqOp(t, "owner", roles.Owner)
		test.EqOp(t, "service_admin", roles.Service)
		test.EqOp(t, [2]string{"support", "billing"}, roles.Membership)
	})

	t.Run("a named vocabulary is used as named", func(t *testing.T) {
		t.Parallel()

		named := Roles{
			Owner:      "account_admin",
			Service:    "platform_admin",
			Membership: [2]string{"account_admin", "account_member"},
		}

		test.EqOp(t, named, (&Session{seams: Seams{Roles: named}}).Roles())
	})

	t.Run("each empty field falls back on its own", func(t *testing.T) {
		t.Parallel()

		roles := (&Session{seams: Seams{Roles: Roles{Owner: "account_admin"}}}).Roles()

		test.EqOp(t, "account_admin", roles.Owner)
		test.EqOp(t, "service_admin", roles.Service)
		test.EqOp(t, [2]string{"support", "billing"}, roles.Membership)
	})
}

func TestRoles_problem(t *testing.T) {
	t.Parallel()

	t.Run("the zero value is accepted", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", Roles{}.problem())
	})

	t.Run("two distinct membership roles are accepted", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", Roles{Membership: [2]string{"account_admin", "account_member"}}.problem())
	})

	t.Run("half a pair is refused rather than spliced onto a literal", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, Roles{Membership: [2]string{"account_member", ""}}.problem(), "names one role and not the other")
		test.StrContains(t, Roles{Membership: [2]string{"", "account_member"}}.problem(), "names one role and not the other")
	})

	t.Run("the same role twice is refused, since nothing could be replaced", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, Roles{Membership: [2]string{"account_member", "account_member"}}.problem(), "twice")
	})
}
