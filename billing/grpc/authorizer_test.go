package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file is the half of authorization a grant on the method cannot reach, and
// it is load-bearing here more than anywhere else the pattern has been applied.
// Seven RPCs are somebody's own money: four name the account in the request and
// three name a row that belongs to one.
//
// Three properties are pinned, and each has a different failure if it is wrong.
// That the seam is consulted at all, or a grant on the method is directory-wide
// over the ledger. That a refusal takes the two different shapes this surface
// gives it, or a caller walking ids learns which of them are real. And that an
// authorizer which could not decide is not read as one that refused, or an
// outage tells a consumer to widen their policy.

// TestErrTargetNotPermittedIsTheDirectorysSentinel pins the aliasing rather than
// assuming it. It is what lets a consumer pass identity/grpc's
// MembershipAuthorizer in unchanged: the method set matches, the Principal is
// the same alias, and a refusal it returns is a refusal this package recognizes.
func TestErrTargetNotPermittedIsTheDirectorysSentinel(T *testing.T) {
	T.Parallel()

	test.ErrorIs(T, billinggrpc.ErrTargetNotPermitted, identitygrpc.ErrTargetNotPermitted)
	test.ErrorIs(T, identitygrpc.ErrTargetNotPermitted, billinggrpc.ErrTargetNotPermitted)
}

// TestAMembershipAuthorizerSatisfiesTheSeam is the claim the documentation makes
// about the common case, made at compile time.
func TestAMembershipAuthorizerSatisfiesTheSeam(T *testing.T) {
	T.Parallel()

	var _ billinggrpc.AccountAuthorizer = (*identitygrpc.MembershipAuthorizer)(nil)
}

// TestANamedAccountRefusalIsPermissionDenied covers the three RPCs whose request
// carries the account.
//
// PermissionDenied rather than NotFound, because the check runs before any read
// and an account the caller has no standing in is answered the same way as an
// account nobody has — so the code discloses nothing about which account ids are
// real.
func TestANamedAccountRefusalIsPermissionDenied(T *testing.T) {
	T.Parallel()

	for name, call := range accountKeyedReads() {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			// A caller active on their own account, asking about somebody
			// else's.
			_, err := call(h, h.ctx(t, testUser, otherAccount), testAccount)
			must.Error(t, err)
			test.EqOp(t, codes.PermissionDenied, status.Code(err))
		})
	}
}

// TestANamedAccountIsReadableToItsOwner is the other side of the same rule: the
// seam is asked and it says yes, which is what keeps the refusal above from
// being a surface that refuses everybody.
func TestANamedAccountIsReadableToItsOwner(T *testing.T) {
	T.Parallel()

	for name, call := range accountKeyedReads() {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := call(h, h.ctx(t, testUser, testAccount), testAccount)
			must.NoError(t, err)
		})
	}
}

// TestAnUndecidableAuthorizerIsInternal is the third answer, and the one a
// consumer is most likely to get wrong in the direction that hurts.
//
// A refusal is a sentence about the caller and an outage is not. Reporting the
// second as the first tells a consumer to widen their policy while their
// database is down, and leaves a dashboard counting server faults reading zero
// through it.
func TestAnUndecidableAuthorizerIsInternal(T *testing.T) {
	T.Parallel()

	undecidable := platformerrors.New("the membership store is unreachable")

	for name, call := range accountKeyedReads() {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarnessWithAuthorizer(t, brokenAuthorizer(undecidable))

			_, err := call(h, h.ctx(t, testUser, testAccount), testAccount)
			must.Error(t, err)
			test.ErrorIs(t, err, undecidable)
			test.EqOp(t, codes.Internal, status.Code(err))
		})
	}
}

// TestAWrappedRefusalIsStillARefusal is what the seam's contract promises an
// implementation: it may add context of its own and still be refusing, because
// the three answers are told apart by errors.Is.
func TestAWrappedRefusalIsStillARefusal(T *testing.T) {
	T.Parallel()

	h := newHarnessWithAuthorizer(T,
		brokenAuthorizer(platformerrors.Wrap(billinggrpc.ErrTargetNotPermitted, "no membership in that account")))

	_, err := h.server.ListTransactionsForAccount(h.ctx(T, testUser, testAccount),
		&billingpb.ListTransactionsForAccountRequest{AccountId: testAccount})
	must.Error(T, err)
	test.EqOp(T, codes.PermissionDenied, status.Code(err))
}

// TestARowOwnerRefusalIsNotFound covers the three keyed reads, and is where this
// surface diverges from identity/grpc on purpose.
//
// There the question is asked ahead of the read, so a refusal is all the server
// can say. Here the row has already been read, and answering PermissionDenied
// would tell a caller walking ids exactly which of them are real — the absent
// row says NotFound and the forbidden one would say something else. So both say
// NotFound.
func TestARowOwnerRefusalIsNotFound(T *testing.T) {
	T.Parallel()

	T.Run("a subscription belonging to another account", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		subscription := h.seedSubscription(t, testScope, otherAccount)

		_, err := h.server.GetSubscription(h.ctx(t, testUser, testAccount),
			&billingpb.GetSubscriptionRequest{SubscriptionId: subscription.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))

		// And the same status an id nobody holds gets, which is the property
		// rather than the code on its own.
		_, missing := h.server.GetSubscription(h.ctx(t, testUser, testAccount),
			&billingpb.GetSubscriptionRequest{SubscriptionId: "nope"})
		must.Error(t, missing)
		test.EqOp(t, status.Code(missing), status.Code(err))
	})

	T.Run("a purchase belonging to another account", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		purchase := h.seedPurchase(t, testScope, otherAccount)

		_, err := h.server.GetPurchase(h.ctx(t, testUser, testAccount),
			&billingpb.GetPurchaseRequest{PurchaseId: purchase.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("a ledger row belonging to another account", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		transaction := h.seedTransaction(t, testScope, otherAccount)

		_, err := h.server.GetTransaction(h.ctx(t, testUser, testAccount),
			&billingpb.GetTransactionRequest{TransactionId: transaction.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}

// TestARefusalDoesNotCarryTheRefusalsWording is why ErrTargetNotPermitted is
// registered as client-safe nowhere.
//
// Half of what this package does with it is answer as though the row were
// absent, and a status whose message said "the caller may not act on the named
// target" would undo that in the one place it matters.
func TestARefusalDoesNotCarryTheRefusalsWording(T *testing.T) {
	T.Parallel()

	h := newHarness(T)
	transaction := h.seedTransaction(T, testScope, otherAccount)

	_, err := h.server.GetTransaction(h.ctx(T, testUser, testAccount),
		&billingpb.GetTransactionRequest{TransactionId: transaction.ID})
	must.Error(T, err)

	message := status.Convert(err).Message()
	test.StrNotContains(T, message, billinggrpc.ErrTargetNotPermitted.Error())
	test.StrContains(T, message, transaction.ID)
}

// TestARowOwnerIsReadableToItsOwner keeps the three reads above from being
// closed to everybody.
func TestARowOwnerIsReadableToItsOwner(T *testing.T) {
	T.Parallel()

	h := newHarness(T)
	ctx := h.ctx(T, testUser, testAccount)

	subscription := h.seedSubscription(T, testScope, testAccount)
	purchase := h.seedPurchase(T, testScope, testAccount)
	transaction := h.seedTransaction(T, testScope, testAccount)

	gotSubscription, err := h.server.GetSubscription(ctx,
		&billingpb.GetSubscriptionRequest{SubscriptionId: subscription.ID})
	must.NoError(T, err)
	test.EqOp(T, subscription.ID, gotSubscription.GetResult().GetId())

	gotPurchase, err := h.server.GetPurchase(ctx, &billingpb.GetPurchaseRequest{PurchaseId: purchase.ID})
	must.NoError(T, err)
	test.EqOp(T, purchase.ID, gotPurchase.GetResult().GetId())

	gotTransaction, err := h.server.GetTransaction(ctx,
		&billingpb.GetTransactionRequest{TransactionId: transaction.ID})
	must.NoError(T, err)
	test.EqOp(T, transaction.ID, gotTransaction.GetResult().GetId())
}

// TestAMalformedRequestIsAnsweredBeforeItIsGated is the ordering identity/grpc
// states and this surface keeps: a malformed request is answered as malformed
// whoever sent it, since saying so discloses nothing about any account.
//
// The evidence is the code. A filter no converter can read is InvalidArgument
// even from a caller the authorizer would have refused, which is only possible
// if the conversion ran first.
func TestAMalformedRequestIsAnsweredBeforeItIsGated(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	_, err := h.server.ListPurchasesForAccount(h.ctx(T, testUser, otherAccount),
		&billingpb.ListPurchasesForAccountRequest{AccountId: testAccount, Filter: badFilter()})
	must.Error(T, err)
	test.EqOp(T, codes.InvalidArgument, status.Code(err))
}

// TestTheOperatorReadsAskNoAuthorizer is the deliberate absence on the other
// side: ListSubscriptions, ListPurchases and ListTransactions page every row in
// the scope and name no account, so there is nothing to ask about and the grant
// is the whole answer.
//
// The harness's authorizer refuses every account but the caller's own, and these
// three answer anyway.
func TestTheOperatorReadsAskNoAuthorizer(T *testing.T) {
	T.Parallel()

	h := newHarnessWithAuthorizer(T, brokenAuthorizer(billinggrpc.ErrTargetNotPermitted))
	ctx := h.ctx(T, testUser, testAccount)

	h.seedSubscription(T, testScope, otherAccount)
	h.seedPurchase(T, testScope, otherAccount)
	h.seedTransaction(T, testScope, otherAccount)

	subscriptions, err := h.server.ListSubscriptions(ctx, &billingpb.ListSubscriptionsRequest{})
	must.NoError(T, err)
	test.SliceLen(T, 1, subscriptions.GetResults())

	purchases, err := h.server.ListPurchases(ctx, &billingpb.ListPurchasesRequest{})
	must.NoError(T, err)
	test.SliceLen(T, 1, purchases.GetResults())

	transactions, err := h.server.ListTransactions(ctx, &billingpb.ListTransactionsRequest{})
	must.NoError(T, err)
	test.SliceLen(T, 1, transactions.GetResults())
}

// accountKeyedReads is the four RPCs whose request names the account, as one
// call each, so the properties above are asserted about all four rather than
// about whichever one somebody wrote a test for.
func accountKeyedReads() map[string]func(*harness, context.Context, string) (any, error) {
	return map[string]func(*harness, context.Context, string) (any, error){
		"ListSubscriptionsForAccount": func(h *harness, ctx context.Context, accountID string) (any, error) {
			return h.server.ListSubscriptionsForAccount(ctx,
				&billingpb.ListSubscriptionsForAccountRequest{AccountId: accountID})
		},
		"ListCurrentSubscriptions": func(h *harness, ctx context.Context, accountID string) (any, error) {
			return h.server.ListCurrentSubscriptions(ctx,
				&billingpb.ListCurrentSubscriptionsRequest{AccountId: accountID})
		},
		"ListPurchasesForAccount": func(h *harness, ctx context.Context, accountID string) (any, error) {
			return h.server.ListPurchasesForAccount(ctx,
				&billingpb.ListPurchasesForAccountRequest{AccountId: accountID})
		},
		"ListTransactionsForAccount": func(h *harness, ctx context.Context, accountID string) (any, error) {
			return h.server.ListTransactionsForAccount(ctx,
				&billingpb.ListTransactionsForAccountRequest{AccountId: accountID})
		},
	}
}
