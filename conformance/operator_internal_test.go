package conformance

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// minting is a session whose factory answers every request with a caller that
// says what it was asked for, reserving reserved and minting an administrator
// only where admins says it can.
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
				sub.Scope = *req.Scope
			}

			if req.Admin {
				sub.UserID = "admin"
			}

			return sub, nil
		},
	}}
}

func TestSession_Operator(T *testing.T) {
	T.Parallel()

	create, list := billingpb.BillingService_CreateProduct_FullMethodName, billingpb.BillingService_ListProducts_FullMethodName

	T.Run("a subject that reserves none of the methods answers an ordinary caller", func(t *testing.T) {
		t.Parallel()

		op := minting(true).Operator(t, create, list)

		test.EqOp(t, "", op.UserID)
	})

	T.Run("a subject that reserves one of the methods answers an administrator", func(t *testing.T) {
		t.Parallel()

		op := minting(true, list).Operator(t, create, list)

		test.EqOp(t, "admin", op.UserID)
	})

	T.Run("OperatorIn puts either caller in the tenant named", func(t *testing.T) {
		t.Parallel()

		account := tenancy.Of("account")

		test.EqOp(t, account, minting(true).OperatorIn(t, account, create).Scope)
		test.EqOp(t, account, minting(true, create).OperatorIn(t, account, create).Scope)
	})

	T.Run("a subject that reserves a method and mints no administrator skips", func(t *testing.T) {
		t.Parallel()

		// A parallel subtest finishes before its parent's cleanups run, which is
		// what lets the parent read how it ended.
		var inner *testing.T
		t.Cleanup(func() { test.True(t, inner.Skipped()) })

		t.Run("reserved", func(t *testing.T) {
			inner = t
			t.Parallel()

			minting(false, create).Operator(t, create)
			t.Error("Operator returned for a reserved method with no administrator to make it")
		})
	})
}

func TestSession_Reserves(t *testing.T) {
	t.Parallel()

	s := minting(true, billingpb.BillingService_ListProducts_FullMethodName)

	test.True(t, s.Reserves(billingpb.BillingService_ListProducts_FullMethodName))
	test.False(t, s.Reserves(billingpb.BillingService_GetProduct_FullMethodName))
}

func TestUnreservable(t *testing.T) {
	t.Parallel()

	test.SliceEmpty(t, unreservable(ReservableMethods()))
	test.SliceEmpty(t, unreservable(nil))
	test.SliceEmpty(t, unreservable([]string{"/consumer.recipes.v1.RecipesService/DeleteRecipe"}),
		test.Sprint("a method on a service no suite covers is the deployment's own business"))

	ordinary := notificationspb.NotificationsService_ListNotifications_FullMethodName
	must.Eq(t, []string{ordinary}, unreservable([]string{billingpb.BillingService_ListProducts_FullMethodName, ordinary}))
}
