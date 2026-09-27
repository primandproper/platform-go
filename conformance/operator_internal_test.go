package conformance

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
)

// minting is a session whose factory answers every request with a caller that
// says what it was asked for, reserving reserved and minting an administrator
// only where admins says it can. A tenant named for a surface is that surface's
// alone, so a caller asked into one reads it back only through ScopeFor.
func minting(admins bool, reserved ...string) *Session {
	return &Session{seams: Seams{
		OperatorMethods: reserved,
		NewSubject: func(_ context.Context, opts ...SubjectOption) (*Subject, error) {
			req := NewSubjectRequest(opts...)
			if req.Admin && !admins {
				return nil, ErrSubjectUnsupported
			}

			sub := &Subject{Scope: tenancy.Of("fresh")}
			if req.Scope != nil {
				sub.Scopes = map[string]tenancy.Scope{req.Surface: *req.Scope}
			}

			if req.Admin {
				sub.UserID = "admin"
			}

			return sub, nil
		},
	}}
}

// skipped runs mint in a subtest and reports whether it skipped. Reaching the
// end of mint is an error, since every case asking this expects the mint to
// skip.
func skipped(t *testing.T, mint func(t *testing.T)) bool {
	t.Helper()

	var inner *testing.T

	t.Run("mint", func(t *testing.T) {
		inner = t

		mint(t)
		t.Error("the mint returned where it should have skipped")
	})

	return inner.Skipped()
}

func TestSession_Subject_routesByReservation(T *testing.T) {
	T.Parallel()

	create, list := billingpb.BillingService_CreateProduct_FullMethodName, billingpb.BillingService_ListProducts_FullMethodName

	T.Run("a subject that reserves none of the methods answers an ordinary caller", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", minting(true).Subject(t, Making(create, list)).UserID)
	})

	T.Run("a subject that reserves one of the methods answers an administrator", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "admin", minting(true, list).Subject(t, Making(create, list)).UserID)
	})

	T.Run("any method may be reserved", func(t *testing.T) {
		t.Parallel()

		mark := notificationspb.NotificationsService_ListNotifications_FullMethodName

		test.EqOp(t, "admin", minting(true, mark).Subject(t, Making(mark)).UserID)
	})

	T.Run("either caller is put in the tenant named on the surface named", func(t *testing.T) {
		t.Parallel()

		account := tenancy.Of("account")

		test.EqOp(t, account, minting(true).Subject(t, Making(create), InTenant("billing", account)).ScopeFor("billing"))
		test.EqOp(t, account, minting(true, create).Subject(t, Making(create), InTenant("billing", account)).ScopeFor("billing"))
	})

	T.Run("a member making nothing reserved is an ordinary caller", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", minting(true, list).Subject(t, Making(create), AsMember()).UserID)
	})

	T.Run("a subject that reserves a method and mints no administrator skips", func(t *testing.T) {
		t.Parallel()

		test.True(t, skipped(t, func(t *testing.T) {
			t.Helper()

			minting(false, create).Subject(t, Making(create))
		}))
	})

	T.Run("a member asked to make a reserved method skips", func(t *testing.T) {
		t.Parallel()

		test.True(t, skipped(t, func(t *testing.T) {
			t.Helper()

			minting(true, create).Subject(t, Making(create), AsMember())
		}))
	})
}

func TestSession_Reserves(t *testing.T) {
	t.Parallel()

	s := minting(true, billingpb.BillingService_ListProducts_FullMethodName)

	test.True(t, s.Reserves(billingpb.BillingService_ListProducts_FullMethodName))
	test.False(t, s.Reserves(billingpb.BillingService_GetProduct_FullMethodName))
}

func TestIsFullMethodName(t *testing.T) {
	t.Parallel()

	test.True(t, isFullMethodName(billingpb.BillingService_ListProducts_FullMethodName))
	test.True(t, isFullMethodName("/consumer.recipes.v1.RecipesService/DeleteRecipe"),
		test.Sprint("a method on a service no suite covers is the deployment's own to reserve"))

	for _, spelled := range []string{
		"",
		"ListProducts",
		"billing.v1.BillingService/ListProducts",
		"/BillingService/ListProducts",
		"/billing.v1.BillingService/",
		"/billing.v1.BillingService/List/Products",
	} {
		test.False(t, isFullMethodName(spelled), test.Sprintf("%q", spelled))
	}
}
