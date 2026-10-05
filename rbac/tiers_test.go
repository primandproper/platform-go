package rbac

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/authorization"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	readAny  authorization.Permission = "things.read_any"
	readOwn  authorization.Permission = "things.read"
	moderate authorization.Permission = "things.moderate"
	override authorization.Permission = "things.override"

	methodGet       = "/things.Things/Get"
	methodGetByMail = "/things.Things/GetByMail"
	methodList      = "/things.Things/List"
	methodListAll   = "/things.Things/ListAll"
)

func testRequired() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		methodGet:       {readOwn},
		methodList:      {moderate},
		methodListAll:   {readAny},
		methodGetByMail: {readAny},
	}
}

func testTiers() Tiers {
	return Tiers{
		Operator:    []authorization.Permission{readAny, override},
		TenantAdmin: []authorization.Permission{moderate},
		Member:      []authorization.Permission{readOwn},
		Narrowings: []Narrowing{
			{Method: methodListAll, Tier: TierMember, Authorizer: "ThingAuthorizer.AuthorizeSubject"},
		},
	}
}

func TestTier_String(T *testing.T) {
	T.Parallel()

	test.EqOp(T, "Member", TierMember.String())
	test.EqOp(T, "TenantAdmin", TierTenantAdmin.String())
	test.EqOp(T, "Operator", TierOperator.String())
	test.EqOp(T, "unknown tier", Tier(0).String())
}

func TestTiers_Validate(T *testing.T) {
	T.Parallel()

	T.Run("a permission in one tier", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, testTiers().Validate())
	})

	T.Run("a permission repeated within one tier grants nothing twice", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, Tiers{Member: []authorization.Permission{readOwn, readOwn}}.Validate())
	})

	T.Run("a permission in two tiers", func(t *testing.T) {
		t.Parallel()

		err := Tiers{
			Operator: []authorization.Permission{readOwn},
			Member:   []authorization.Permission{readOwn},
		}.Validate()

		test.ErrorIs(t, err, ErrPermissionInTwoTiers)
	})
}

func TestTiers_Partitions(T *testing.T) {
	T.Parallel()

	T.Run("an exact partition", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, testTiers().Partitions(testRequired(), override))
	})

	T.Run("a permission required and placed nowhere", func(t *testing.T) {
		t.Parallel()

		tiers := testTiers()
		tiers.TenantAdmin = nil

		err := tiers.Partitions(testRequired(), override)
		test.ErrorIs(t, err, ErrUntieredPermission)
		test.StrContains(t, err.Error(), string(moderate))
	})

	T.Run("a permission consulted and placed nowhere", func(t *testing.T) {
		t.Parallel()

		err := testTiers().Partitions(testRequired(), override, "things.unplaced")
		test.ErrorIs(t, err, ErrUntieredPermission)
	})

	T.Run("a permission placed and checked by nothing", func(t *testing.T) {
		t.Parallel()

		// Without override consulted, the operator tier grants a permission
		// no method and no handler asks about.
		err := testTiers().Partitions(testRequired())
		test.ErrorIs(t, err, ErrUncheckedPermission)
		test.StrContains(t, err.Error(), string(override))
	})

	T.Run("a permission placed twice", func(t *testing.T) {
		t.Parallel()

		for name, tiers := range map[string]Tiers{
			"in two tiers": func() Tiers {
				tiers := testTiers()
				tiers.Member = append(tiers.Member, moderate)

				return tiers
			}(),
			"within one tier": func() Tiers {
				tiers := testTiers()
				tiers.Member = append(tiers.Member, readOwn)

				return tiers
			}(),
		} {
			test.ErrorIs(t, tiers.Partitions(testRequired(), override), ErrPermissionInTwoTiers, test.Sprint(name))
		}
	})

	T.Run("every problem at once", func(t *testing.T) {
		t.Parallel()

		err := Tiers{Member: []authorization.Permission{"things.stale"}}.Partitions(testRequired())
		test.ErrorIs(t, err, ErrUntieredPermission)
		test.ErrorIs(t, err, ErrUncheckedPermission)
	})

	T.Run("a narrowing of a method that requires nothing", func(t *testing.T) {
		t.Parallel()

		tiers := testTiers()
		tiers.Narrowings = append(tiers.Narrowings, Narrowing{
			Method:     "/things.Things/Public",
			Tier:       TierMember,
			Authorizer: "ThingAuthorizer.AuthorizeSubject",
		})

		test.ErrorIs(t, tiers.Partitions(testRequired(), override), ErrInvalidNarrowing)
	})

	T.Run("a narrowing that names no authorizer", func(t *testing.T) {
		t.Parallel()

		tiers := testTiers()
		tiers.Narrowings[0].Authorizer = ""

		test.ErrorIs(t, tiers.Partitions(testRequired(), override), ErrInvalidNarrowing)
	})

	T.Run("a narrowing to its permission's own tier", func(t *testing.T) {
		t.Parallel()

		tiers := testTiers()
		tiers.Narrowings[0].Tier = TierOperator

		test.ErrorIs(t, tiers.Partitions(testRequired(), override), ErrInvalidNarrowing)
	})

	T.Run("a method narrowed twice", func(t *testing.T) {
		t.Parallel()

		tiers := testTiers()
		tiers.Narrowings = append(tiers.Narrowings, tiers.Narrowings[0])

		test.ErrorIs(t, tiers.Partitions(testRequired(), override), ErrInvalidNarrowing)
	})
}

func TestMergeTiers(T *testing.T) {
	T.Parallel()

	T.Run("disjoint surfaces", func(t *testing.T) {
		t.Parallel()

		other := Tiers{
			Operator: []authorization.Permission{"other.read_any"},
			Member:   []authorization.Permission{"other.read"},
		}

		merged, err := MergeTiers(testTiers(), other)
		must.NoError(t, err)

		test.Eq(t, []authorization.Permission{readAny, override, "other.read_any"}, merged.Operator)
		test.Eq(t, []authorization.Permission{moderate}, merged.TenantAdmin)
		test.Eq(t, []authorization.Permission{readOwn, "other.read"}, merged.Member)
		test.Eq(t, testTiers().Narrowings, merged.Narrowings)
	})

	T.Run("one permission in one tier from two surfaces is kept once", func(t *testing.T) {
		t.Parallel()

		merged, err := MergeTiers(testTiers(), testTiers())
		must.NoError(t, err)

		test.Eq(t, testTiers(), merged)
	})

	T.Run("nothing to merge", func(t *testing.T) {
		t.Parallel()

		merged, err := MergeTiers()
		must.NoError(t, err)
		test.Eq(t, Tiers{}, merged)
	})

	T.Run("one permission in two tiers from two surfaces", func(t *testing.T) {
		t.Parallel()

		merged, err := MergeTiers(testTiers(), Tiers{Member: []authorization.Permission{readAny}})
		test.ErrorIs(t, err, ErrPermissionInTwoTiers)
		test.Eq(t, Tiers{}, merged)
	})

	T.Run("one method narrowed two ways", func(t *testing.T) {
		t.Parallel()

		other := Tiers{Narrowings: []Narrowing{{Method: methodListAll, Tier: TierTenantAdmin, Authorizer: "Other"}}}

		_, err := MergeTiers(testTiers(), other)
		test.ErrorIs(t, err, ErrInvalidNarrowing)
	})
}

func TestPolicyFromTiers(T *testing.T) {
	T.Parallel()

	T.Run("each tier inherits the one below it", func(t *testing.T) {
		t.Parallel()

		policy := PolicyFromTiers(testTiers(), RoleNames{Operator: "staff", TenantAdmin: "owner", Member: "member"})
		must.NoError(t, policy.Validate())

		expanded, err := authorization.ExpandInheritance(policy.Roles...)
		must.NoError(t, err)

		test.True(t, expanded["member"].Equal(authorization.NewPermissionSet(readOwn)))
		test.True(t, expanded["owner"].Equal(authorization.NewPermissionSet(readOwn, moderate)))
		test.True(t, expanded["staff"].Equal(authorization.NewPermissionSet(readOwn, moderate, readAny, override)))
	})

	T.Run("a narrowing is granted to nobody", func(t *testing.T) {
		t.Parallel()

		policy := PolicyFromTiers(testTiers(), RoleNames{Operator: "staff", TenantAdmin: "owner", Member: "member"})

		expanded, err := authorization.ExpandInheritance(policy.Roles...)
		must.NoError(t, err)

		// The narrowed method's permission is the operator's, and the member
		// role is not handed it on the narrowing's account.
		test.False(t, expanded["member"].Has(readAny))
	})

	T.Run("the policy does not alias the tiers", func(t *testing.T) {
		t.Parallel()

		tiers := testTiers()
		policy := PolicyFromTiers(tiers, RoleNames{Operator: "staff", TenantAdmin: "owner", Member: "member"})
		policy.Roles[0].Permissions[0] = "rewritten"

		test.EqOp(t, readOwn, tiers.Member[0])
	})

	T.Run("an unnamed tier fails validation rather than being folded", func(t *testing.T) {
		t.Parallel()

		policy := PolicyFromTiers(testTiers(), RoleNames{Operator: "staff", Member: "member"})
		test.ErrorIs(t, policy.Validate(), authorization.ErrEmptyRoleName)
	})
}
