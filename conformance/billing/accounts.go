package billing

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// accountRead is one of the four RPCs whose request names the account, as a
// call a caller makes.
type accountRead func(ctx context.Context, sub *conformance.Subject, accountID string, filter *filteringpb.QueryFilter) error

// accountReads are all four, so every property below is asserted about each
// of them rather than about whichever one somebody wrote a test for.
func accountReads() map[string]accountRead {
	return map[string]accountRead{
		"ListSubscriptionsForAccount": func(ctx context.Context, sub *conformance.Subject, accountID string, filter *filteringpb.QueryFilter) error {
			_, err := sub.Surfaces.Billing.ListSubscriptionsForAccount(ctx,
				&billingpb.ListSubscriptionsForAccountRequest{AccountId: accountID, Filter: filter})

			return err
		},
		"ListCurrentSubscriptions": func(ctx context.Context, sub *conformance.Subject, accountID string, filter *filteringpb.QueryFilter) error {
			_, err := sub.Surfaces.Billing.ListCurrentSubscriptions(ctx,
				&billingpb.ListCurrentSubscriptionsRequest{AccountId: accountID, Filter: filter})

			return err
		},
		"ListPurchasesForAccount": func(ctx context.Context, sub *conformance.Subject, accountID string, filter *filteringpb.QueryFilter) error {
			_, err := sub.Surfaces.Billing.ListPurchasesForAccount(ctx,
				&billingpb.ListPurchasesForAccountRequest{AccountId: accountID, Filter: filter})

			return err
		},
		"ListTransactionsForAccount": func(ctx context.Context, sub *conformance.Subject, accountID string, filter *filteringpb.QueryFilter) error {
			_, err := sub.Surfaces.Billing.ListTransactionsForAccount(ctx,
				&billingpb.ListTransactionsForAccountRequest{AccountId: accountID, Filter: filter})

			return err
		},
	}
}

func accounts(t *testing.T, s *conformance.Session) {
	t.Helper()

	for name, read := range accountReads() {
		// PermissionDenied rather than NotFound, and the same for an account
		// that exists and one that does not: the check runs before anything is
		// read, so the code says nothing about which account identifiers are
		// real.
		t.Run(name+" answers the caller's own account and refuses every other", func(t *testing.T) {
			t.Parallel()

			mine := s.Subject(t)
			needsAccount(t, mine)
			other := colleague(t, s, mine)
			ctx := mine.Context(t.Context())

			// The positive control, which is what keeps the refusals below from
			// being a surface that refuses everybody.
			must.NoError(t, read(ctx, mine, mine.AccountID, nil),
				must.Sprint("the caller cannot read its own account; the refusals below prove nothing"))

			err := read(ctx, mine, other.AccountID, nil)
			must.Error(t, err, must.Sprint("a colleague's account was readable to a caller with no membership in it"))
			test.EqOp(t, codes.PermissionDenied, status.Code(err))

			err = read(ctx, mine, identifiers.New(), nil)
			must.Error(t, err)
			test.EqOp(t, codes.PermissionDenied, status.Code(err),
				test.Sprint("an account nobody holds was answered differently from one somebody else holds"))
		})

		// Malformed rather than refused, and answered before any rule is
		// asked: an empty account is not an account somebody may or may not
		// reach, and a rule that happened to permit the empty string would
		// otherwise have gone on to page everybody's.
		t.Run(name+" refuses a request that names no account as malformed", func(t *testing.T) {
			t.Parallel()

			mine := s.Subject(t)

			err := read(mine.Context(t.Context()), mine, "", nil)
			must.Error(t, err)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))
		})

		// The same ordering from the other side. A filter nothing can read is
		// malformed whoever sent it, and saying so discloses nothing about any
		// account — so it is InvalidArgument even for an account the caller
		// would have been refused, which is only possible if the conversion
		// ran first.
		t.Run(name+" answers a malformed page as malformed before it gates the account", func(t *testing.T) {
			t.Parallel()

			mine := s.Subject(t)
			needsAccount(t, mine)
			other := colleague(t, s, mine)

			err := read(mine.Context(t.Context()), mine, other.AccountID, badFilter())
			must.Error(t, err)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))
		})
	}
}
