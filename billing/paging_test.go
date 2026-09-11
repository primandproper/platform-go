package billing

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestPageFilter(T *testing.T) {
	T.Parallel()

	T.Run("a missing filter becomes the default one", func(t *testing.T) {
		t.Parallel()

		// The page size is what the generated statements bind, and MySQL's LIMIT
		// takes a value rather than an expression — so an unset one has to
		// become a number here rather than in a COALESCE the SQL cannot spell.
		bounded := pageFilter(nil)

		must.NotNil(t, bounded)
		must.NotNil(t, bounded.MaxResponseSize)
		test.EqOp(t, uint16(filtering.DefaultQueryFilterLimit), *bounded.MaxResponseSize)
	})

	T.Run("a filter with no page size gets the default one", func(t *testing.T) {
		t.Parallel()

		bounded := pageFilter(&filtering.QueryFilter{})

		must.NotNil(t, bounded.MaxResponseSize)
		test.EqOp(t, uint16(filtering.DefaultQueryFilterLimit), *bounded.MaxResponseSize)
	})

	T.Run("a page size within the ceiling is kept", func(t *testing.T) {
		t.Parallel()

		size := uint16(7)

		bounded := pageFilter(&filtering.QueryFilter{MaxResponseSize: &size})

		must.NotNil(t, bounded.MaxResponseSize)
		test.EqOp(t, uint16(7), *bounded.MaxResponseSize)
	})

	T.Run("a page size over the ceiling is clamped to it", func(t *testing.T) {
		t.Parallel()

		// Clamping is filtering's, at filtering's ceiling — the same treatment
		// the URL parameter gets, rather than a second ceiling written here.
		size := filtering.MaxQueryFilterLimit

		bounded := pageFilter(&filtering.QueryFilter{MaxResponseSize: &size})

		must.NotNil(t, bounded.MaxResponseSize)
		test.EqOp(t, filtering.MaxQueryFilterLimit, *bounded.MaxResponseSize)
	})

	T.Run("the caller's filter is not written through", func(t *testing.T) {
		t.Parallel()

		// It bounds a copy: a caller who reuses a filter across two reads must
		// not find the first read's defaulting applied to the second.
		original := &filtering.QueryFilter{}

		bounded := pageFilter(original)

		test.Nil(t, original.MaxResponseSize)
		test.NotNil(t, bounded.MaxResponseSize)
	})
}

// runPagingSuite is what a descending filter means to the seven paged reads.
//
// A direction is statement text on all three engines rather than a bound value,
// so querygen emits each paged read twice and sortedRows picks between them. A
// read that reached for the ascending statement while holding a descending
// filter would answer in the order the client did not ask for, and nothing about
// the rows that came back would say so — which is why this asks each of them.
func runPagingSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	descending := func() *filtering.QueryFilter {
		return &filtering.QueryFilter{SortBy: filtering.SortDescending}
	}

	// ids reads the page's identifiers in the order the store returned them.
	idsOf := func(page []*Product) []string {
		out := make([]string, 0, len(page))
		for _, p := range page {
			out = append(out, p.ID)
		}

		return out
	}

	t.Run("a descending catalog page reverses the ascending one", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreateProduct(t, env, store, testScope, oneTimeProduct("first"))
		mustCreateProduct(t, env, store, testScope, oneTimeProduct("second"))
		mustCreateProduct(t, env, store, testScope, oneTimeProduct("third"))

		up, err := store.ListProducts(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		must.SliceLen(t, 3, up.Data)

		down, err := store.ListProducts(t.Context(), env.reader(), testScope, descending())
		must.NoError(t, err)
		must.SliceLen(t, 3, down.Data)

		ascending := idsOf(up.Data)
		reversed := idsOf(down.Data)

		test.Eq(t, []string{ascending[2], ascending[1], ascending[0]}, reversed)
	})

	t.Run("every paged read answers in the direction it was asked for", func(t *testing.T) {
		t.Parallel()

		// Each of these is a second statement that only a descending filter
		// reaches, so a read wired to the wrong one is invisible until asked.
		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, recurringProduct("pro"))
		oneTime := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))

		mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, testAccount))
		mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, testAccount))
		mustCreatePurchase(t, env, store, testScope, outstandingPurchase(oneTime.ID, testAccount))
		mustCreatePurchase(t, env, store, testScope, outstandingPurchase(oneTime.ID, testAccount))
		mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))
		mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))

		subs, err := store.ListSubscriptions(t.Context(), env.reader(), testScope, descending())
		must.NoError(t, err)
		test.SliceLen(t, 2, subs.Data)

		subsForAccount, err := store.ListSubscriptionsForAccount(t.Context(), env.reader(), testScope, testAccount, descending())
		must.NoError(t, err)
		test.SliceLen(t, 2, subsForAccount.Data)

		current, err := store.ListCurrentSubscriptions(t.Context(), env.reader(), testScope, testAccount, descending())
		must.NoError(t, err)
		test.SliceLen(t, 2, current.Data)

		purchases, err := store.ListPurchases(t.Context(), env.reader(), testScope, descending())
		must.NoError(t, err)
		test.SliceLen(t, 2, purchases.Data)

		purchasesForAccount, err := store.ListPurchasesForAccount(t.Context(), env.reader(), testScope, testAccount, descending())
		must.NoError(t, err)
		test.SliceLen(t, 2, purchasesForAccount.Data)

		transactions, err := store.ListTransactions(t.Context(), env.reader(), testScope, descending())
		must.NoError(t, err)
		test.SliceLen(t, 2, transactions.Data)

		transactionsForAccount, err := store.ListTransactionsForAccount(
			t.Context(), env.reader(), testScope, testAccount, descending())
		must.NoError(t, err)
		test.SliceLen(t, 2, transactionsForAccount.Data)
	})

	t.Run("a bounded page size is honored in both directions", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreateProduct(t, env, store, testScope, oneTimeProduct("first"))
		mustCreateProduct(t, env, store, testScope, oneTimeProduct("second"))
		mustCreateProduct(t, env, store, testScope, oneTimeProduct("third"))

		size := uint16(2)

		up, err := store.ListProducts(t.Context(), env.reader(), testScope,
			&filtering.QueryFilter{MaxResponseSize: &size})
		must.NoError(t, err)
		test.SliceLen(t, 2, up.Data)

		down, err := store.ListProducts(t.Context(), env.reader(), testScope,
			&filtering.QueryFilter{MaxResponseSize: &size, SortBy: filtering.SortDescending})
		must.NoError(t, err)
		test.SliceLen(t, 2, down.Data)

		// The page is short of the table, and the counts beside it say so.
		test.EqOp(t, uint64(3), up.TotalCount)
		test.EqOp(t, uint64(3), down.TotalCount)
	})
}
