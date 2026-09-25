package billing

import (
	"testing"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// targetNotPermitted is the refusal's own sentence, which must never reach a
// client from a read that answers a refusal as an absence. It is spelled out
// rather than imported so that a deployment's suite need not link callers, and
// because what is asserted is the words on the wire.
const targetNotPermitted = "the caller may not act on the named target"

func subscriptions(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an account's subscriptions are its own, with the status the provider reported", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)
		own, theirs := subscribed(t, s, mine), subscribed(t, s, other)

		page, err := mine.Surfaces.Billing.ListSubscriptionsForAccount(mine.Context(t.Context()),
			&billingpb.ListSubscriptionsForAccountRequest{AccountId: mine.AccountID})
		must.NoError(t, err)
		test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		ids := subscriptionIDs(page.GetResults())
		test.SliceContains(t, ids, own, test.Sprint("the account's own subscription was missing from its page"))
		test.SliceNotContains(t, ids, theirs,
			test.Sprint("another account's subscription reached this account's page"))

		// The wire value is the provider's own spelling rather than a
		// generated constant, and it is carried rather than interpreted.
		for _, subscription := range page.GetResults() {
			if subscription.GetId() == own {
				test.EqOp(t, capitalism.SubscriptionStatusActive.String(), subscription.GetStatus())
			}
		}
	})

	t.Run("a subscription whose paid period covers now is current", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)
		own, theirs := subscribed(t, s, mine), subscribed(t, s, other)

		page, err := mine.Surfaces.Billing.ListCurrentSubscriptions(mine.Context(t.Context()),
			&billingpb.ListCurrentSubscriptionsRequest{AccountId: mine.AccountID})
		must.NoError(t, err)

		ids := subscriptionIDs(page.GetResults())
		test.SliceContains(t, ids, own, test.Sprint("a subscription paid for through today was not current"))
		test.SliceNotContains(t, ids, theirs,
			test.Sprint("another account's subscription reached this account's current page"))
	})

	// The divergence from a read that names its account: here the row has
	// already been read before its account is asked about, and answering
	// PermissionDenied would tell a caller walking identifiers exactly which of
	// them are real. So another account's row and a row nobody holds are one
	// answer.
	t.Run("another account's subscription is absent, exactly as an unknown one is", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)
		own, theirs := subscribed(t, s, mine), subscribed(t, s, other)
		ctx := mine.Context(t.Context())

		read, err := mine.Surfaces.Billing.GetSubscription(ctx, &billingpb.GetSubscriptionRequest{SubscriptionId: own})
		must.NoError(t, err, must.Sprint("the caller cannot read its own subscription; the absence below proves nothing"))
		test.EqOp(t, own, read.GetResult().GetId())
		test.EqOp(t, mine.AccountID, read.GetResult().GetBelongsToAccount())

		_, refused := mine.Surfaces.Billing.GetSubscription(ctx, &billingpb.GetSubscriptionRequest{SubscriptionId: theirs})
		must.Error(t, refused, must.Sprint("another account's subscription was readable"))
		test.EqOp(t, codes.NotFound, status.Code(refused))

		_, missing := mine.Surfaces.Billing.GetSubscription(ctx,
			&billingpb.GetSubscriptionRequest{SubscriptionId: identifiers.New()})
		must.Error(t, missing)
		test.EqOp(t, status.Code(missing), status.Code(refused),
			test.Sprint("a subscription somebody else holds was answered differently from one nobody holds"))

		// And the words do not undo it. A status whose message named the
		// refusal would be the oracle the code was chosen to close.
		test.StrNotContains(t, status.Convert(refused).Message(), targetNotPermitted)
	})

	t.Run("a neighboring tenant's subscription is absent", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		own, foreign := subscribed(t, s, mine), subscribed(t, s, theirs)

		read, err := mine.Surfaces.Billing.GetSubscription(mine.Context(t.Context()),
			&billingpb.GetSubscriptionRequest{SubscriptionId: own})
		must.NoError(t, err, must.Sprint("the caller cannot read its own subscription; the absence below proves nothing"))
		test.EqOp(t, own, read.GetResult().GetId())

		_, err = mine.Surfaces.Billing.GetSubscription(mine.Context(t.Context()),
			&billingpb.GetSubscriptionRequest{SubscriptionId: foreign})
		must.Error(t, err, must.Sprint("a neighboring tenant's subscription was readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	// The operator's read names no account, so there is nothing to ask a rule
	// about and the grant is the whole answer: a colleague's subscription is on
	// it, and a neighboring tenant's is not.
	t.Run("the scope-wide listing is the tenant's, and asks about no account", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		other := colleague(t, s, mine)
		own, shared, foreign := subscribed(t, s, mine), subscribed(t, s, other), subscribed(t, s, theirs)

		page, err := mine.Surfaces.Billing.ListSubscriptions(mine.Context(t.Context()),
			&billingpb.ListSubscriptionsRequest{})
		must.NoError(t, err)

		ids := subscriptionIDs(page.GetResults())
		test.SliceContains(t, ids, own)
		test.SliceContains(t, ids, shared,
			test.Sprint("the scope-wide listing consulted an account rule it has no account to ask about"))
		test.SliceNotContains(t, ids, foreign,
			test.Sprint("a neighboring tenant's subscription reached the scope-wide listing"))
	})

	// Archiving is administrative and asks about no account, which is the
	// deliberate consequence of what it is: a cancellation is a fact the
	// provider reports and arrives as a status, so withdrawing a row is an
	// operator's correction rather than something an account does to its own.
	t.Run("archiving a subscription asks about no account, and withdraws it", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		holder := colleague(t, s, operator)
		theirs := subscribed(t, s, holder)
		holderCtx := holder.Context(t.Context())

		_, err := holder.Surfaces.Billing.GetSubscription(holderCtx, &billingpb.GetSubscriptionRequest{SubscriptionId: theirs})
		must.NoError(t, err, must.Sprint("the holder cannot read its own subscription before archival"))

		_, err = operator.Surfaces.Billing.ArchiveSubscription(operator.Context(t.Context()),
			&billingpb.ArchiveSubscriptionRequest{SubscriptionId: theirs})
		must.NoError(t, err)

		_, err = holder.Surfaces.Billing.GetSubscription(holderCtx, &billingpb.GetSubscriptionRequest{SubscriptionId: theirs})
		must.Error(t, err, must.Sprint("an archived subscription was still readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("archiving a neighboring tenant's subscription is absent and changes nothing", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		own := subscribed(t, s, mine)

		_, err := theirs.Surfaces.Billing.ArchiveSubscription(theirs.Context(t.Context()),
			&billingpb.ArchiveSubscriptionRequest{SubscriptionId: own})
		must.Error(t, err, must.Sprint("a neighboring tenant archived this caller's subscription"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		read, err := mine.Surfaces.Billing.GetSubscription(mine.Context(t.Context()),
			&billingpb.GetSubscriptionRequest{SubscriptionId: own})
		must.NoError(t, err, must.Sprint("a refused archival withdrew the subscription anyway"))
		test.EqOp(t, own, read.GetResult().GetId())
	})
}
