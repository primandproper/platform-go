package conformance

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/billing/billingpb"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

// signingIn is a session whose SignedIn seam answers every token with inner,
// reserving reserved, over a subject that mounts billing.
func signingIn(inner grpc.ClientConnInterface, handed *[]string, reserved ...string) *Session {
	return &Session{
		mounted: Surfaces{Billing: billingpb.NewBillingServiceClient(inner)},
		seams: Seams{
			OperatorMethods: reserved,
			SignedIn: func(_ context.Context, issued *signinpb.IssuedToken) (grpc.ClientConnInterface, error) {
				*handed = append(*handed, issued.GetToken())

				return inner, nil
			},
		},
	}
}

func TestSession_SignedIn(T *testing.T) {
	T.Parallel()

	list := billingpb.BillingService_ListProducts_FullMethodName
	issued := &signinpb.IssuedToken{Token: "the token", ActiveAccountId: "account"}

	T.Run("the token is handed to the seam and its caller reaches every mounted surface", func(t *testing.T) {
		t.Parallel()

		inner, handed := &answering{}, []string{}
		sub := signingIn(inner, &handed).SignedIn(t, issued, Making(list), InTenant("billing", tenancy.Global()))

		test.Eq(t, []string{"the token"}, handed)
		test.EqOp(t, "account", sub.AccountID)
		test.EqOp(t, "", sub.UserID, test.Sprint("a token names nobody a client can read"))
		test.EqOp(t, tenancy.Global(), sub.Scope)
		test.Nil(t, sub.Surfaces.Audit, test.Sprint("a surface the subject did not mount was mounted"))

		must.NotNil(t, sub.Surfaces.Billing)
		_, err := sub.Surfaces.Billing.ListProducts(t.Context(), &billingpb.ListProductsRequest{})
		test.NoError(t, err)
		test.Eq(t, []string{list}, inner.calls)
	})

	T.Run("the caller is held to what it declared", func(t *testing.T) {
		t.Parallel()

		inner, handed := &answering{}, []string{}
		sub := signingIn(inner, &handed).SignedIn(t, issued, Making(list))

		conn, ok := sub.Conn.(*declaredConn)
		must.True(t, ok, must.Sprintf("the caller's connection is a %T rather than a checked one", sub.Conn))
		test.Eq(t, []string{list}, conn.declared)
	})

	T.Run("a subject with no seam skips", func(t *testing.T) {
		t.Parallel()

		test.True(t, skipped(t, func(t *testing.T) {
			t.Helper()

			(&Session{}).SignedIn(t, issued, Making(list))
		}))
	})

	T.Run("an ordinary token declaring a reserved call skips", func(t *testing.T) {
		t.Parallel()

		test.True(t, skipped(t, func(t *testing.T) {
			t.Helper()

			handed := []string{}
			signingIn(&answering{}, &handed, list).SignedIn(t, issued, Making(list))
		}))
	})

	T.Run("an administrative token may declare a reserved call", func(t *testing.T) {
		t.Parallel()

		inner, handed := &answering{}, []string{}
		admin := &signinpb.IssuedToken{Token: "the token", Administrative: true}

		sub := signingIn(inner, &handed, list).SignedIn(t, admin, Making(list))

		_, err := sub.Surfaces.Billing.ListProducts(t.Context(), &billingpb.ListProductsRequest{})
		test.NoError(t, err)
	})
}
