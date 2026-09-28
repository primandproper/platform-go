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
		test.EqOp(t, "operator", roles.Service)
		test.EqOp(t, [2]string{"support", "billing"}, roles.Membership)
	})

	t.Run("a named vocabulary is used as named", func(t *testing.T) {
		t.Parallel()

		named := Roles{
			Owner:      "proprietor",
			Service:    "platform_admin",
			Membership: [2]string{"proprietor", "patron"},
		}

		test.EqOp(t, named, (&Session{seams: Seams{Roles: named}}).Roles())
	})

	t.Run("each empty field falls back on its own", func(t *testing.T) {
		t.Parallel()

		roles := (&Session{seams: Seams{Roles: Roles{Owner: "proprietor"}}}).Roles()

		test.EqOp(t, "proprietor", roles.Owner)
		test.EqOp(t, "operator", roles.Service)
		test.EqOp(t, [2]string{"support", "billing"}, roles.Membership)
	})
}

func TestRoles_problem(t *testing.T) {
	t.Parallel()

	t.Run("the zero value is accepted", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", (&Roles{}).problem())
	})

	t.Run("two distinct membership roles are accepted", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", (&Roles{Membership: [2]string{"proprietor", "patron"}}).problem())
	})

	t.Run("half a pair is refused rather than spliced onto a literal", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, (&Roles{Membership: [2]string{"patron", ""}}).problem(), "names one role and not the other")
		test.StrContains(t, (&Roles{Membership: [2]string{"", "patron"}}).problem(), "names one role and not the other")
	})

	t.Run("the same role twice is refused, since nothing could be replaced", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, (&Roles{Membership: [2]string{"patron", "patron"}}).problem(), "twice")
	})
}
