package billing

import (
	"time"

	"github.com/primandproper/platform-go/v14/billing/internal/billingdb"

	"github.com/primandproper/primitives-go/capitalism"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// The typed seam between the generated package and the domain types.
//
// billing/internal/billingdb is sqlc-gen-unison's output: one params and one row
// struct per statement, the same on all three dialects. These functions are the
// whole of what this package does with them — a row becomes the domain type, a
// domain value becomes the params — and every one is a struct literal on
// purpose. A renamed or retyped column changes the generated struct, and every
// conversion here stops compiling; a scan-by-position pairing would report the
// same mistake as a runtime scan error, or worse, as two same-typed columns
// silently transposed.
//
// The row structs are nominal per statement, so there is one conversion per
// shape rather than one per statement, and the statements that share a shape are
// converted into it. That is a narrower claim than it looks: every read here
// projects its table's own column list, so two statements over one table are one
// projection rendered twice with different predicates, exactly as an ascending
// page and its descending twin are. Go makes the claim the compiler's — the day
// two of them stop being identical in field name, type or order, this stops
// building rather than filling the wrong fields.
//
// What is deliberately not converted that way is a row from one table into
// another's. Those are different projections that would only ever agree by
// accident, and each has its own conversion below.

// utcPtr normalizes an optional timestamp to UTC, preserving absence. It is the
// one home for the rule, and every conversion below goes through it.
//
// Every timestamp this package returns is normalized here — Postgres hands back
// a time in the session's zone, MySQL in the server's, and SQLite whatever the
// string parsed as, so a caller comparing two of those, or rendering one into
// JSON, would otherwise get an answer that depends on where the row was read.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// nullable renders an optional string column: the empty string as absent.
//
// It is the one home for the rule this package's four external identifier fields
// follow. The empty string is not a legal provider identifier, so mapping it onto
// NULL loses nothing — and NULL is what the unique indexes need in order to admit
// more than one row that has no provider behind it. See billing/migrations.
func nullable(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// text reads an optional string column back, rendering absence as the empty
// string. It is nullable's inverse and the reason the domain types carry no
// string pointers.
func text(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// months renders an optional interval column: zero as absent.
//
// Zero months is not a billing interval, so the empty value cannot be confused
// with a real one — which is what lets [Product.BillingIntervalMonths] be an
// int64 rather than a pointer.
func months(n int64) *int64 {
	if n == 0 {
		return nil
	}

	return &n
}

// count reads an optional integer column back, rendering absence as zero. It is
// months' inverse.
func count(n *int64) int64 {
	if n == nil {
		return 0
	}

	return *n
}

// listWindow is the filter window every generated list statement binds, in the
// shape the generated params carry it. One reading of the filter, restated into
// each nominal params type by the constructors below.
type listWindow struct {
	createdAfter    *time.Time
	createdBefore   *time.Time
	updatedAfter    *time.Time
	updatedBefore   *time.Time
	pageCursor      *string
	resultLimit     int64
	includeArchived bool
}

// windowFrom reads the window off a page filter. The filter has been through
// pageFilter, so MaxResponseSize is set; only IncludeArchived defaults here, and
// it defaults to excluding, which is what the statement's COALESCE would have
// done with a NULL anyway — bound explicitly so the parameter is a bool rather
// than a pointer whose nil means the same thing.
//
// The UTC normalization on the four times is load-bearing on SQLite, not
// cosmetic. That column compares as text, the stored shape is UTC
// `YYYY-MM-DD HH:MM:SS`, and the driver renders a bound time.Time with its own
// zone's clock in exactly that prefix position — so a UTC value compares
// correctly to the second and a zoned one is off by its offset, silently.
func windowFrom(filter *filtering.QueryFilter) listWindow {
	w := listWindow{
		createdAfter:  utcPtr(filter.CreatedAfter),
		createdBefore: utcPtr(filter.CreatedBefore),
		updatedAfter:  utcPtr(filter.UpdatedAfter),
		updatedBefore: utcPtr(filter.UpdatedBefore),
		pageCursor:    filter.Cursor,
		resultLimit:   int64(*filter.MaxResponseSize),
	}

	if filter.IncludeArchived != nil {
		w.includeArchived = *filter.IncludeArchived
	}

	return w
}

// sortedRows runs whichever of a paged read's two statements the filter's sort
// direction names, and hands back the ascending statement's rows either way.
//
// A paged list is two statements here, because a direction is which way the
// ORDER BY runs and which way the cursor comparison points — statement text, not
// a bound value, on all three engines. database/querygen emits the pair and
// filtering.QueryFilter.SortsDescending picks between them; this is where the
// pick is made, once, rather than at each of the seven paged reads. A read that
// reached for the ascending statement while holding a descending filter would
// answer in the order the client did not ask for, and nothing about the rows that
// came back would say so.
func sortedRows[Ascending, Descending any](
	filter *filtering.QueryFilter,
	ascending func() ([]Ascending, error),
	descending func() ([]Descending, error),
	same func(Descending) Ascending,
) ([]Ascending, error) {
	if !filter.SortsDescending() {
		return ascending()
	}

	rows, err := descending()
	if err != nil {
		return nil, err
	}

	page := make([]Ascending, 0, len(rows))
	for i := range rows {
		page = append(page, same(rows[i]))
	}

	return page, nil
}

// pageRow is one row of a rendered list query: the value, and the two counts the
// statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which is
// what makes a page and the number describing it come from one snapshot of the
// table. It also means a page with no rows carries no counts — see
// filtering.Drain, which is what reports that as unknown rather than as zero.
type pageRow[T any] struct {
	value    *T
	filtered int64
	total    int64
}

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts[T any](row pageRow[T]) (filtered, total int64) {
	return row.filtered, row.total
}

// pageValue reads the value off a row, for filtering.Drain.
func pageValue[T any](row pageRow[T]) *T { return row.value }

// drainPage assembles a paged result from the rows one list statement returned.
//
// The cursor is the id, because every statement here orders by it. A cursor
// naming a position in an order the query does not use is a page that skips rows
// and repeats others, with nothing reporting an error.
func drainPage[T any](
	rows []pageRow[T],
	id func(*T) string,
	filter *filtering.QueryFilter,
) *filtering.QueryFilteredResult[T] {
	return filtering.Drain(rows, pageValue, pageCounts, id, filter)
}

// Products.

func productFromRow(r *billingdb.GetProductRow) *Product {
	return &Product{
		ID:                    r.ID,
		Scope:                 r.Scope,
		Name:                  r.Name,
		Description:           r.Description,
		Kind:                  Kind(r.Kind),
		AmountCents:           r.AmountCents,
		Currency:              r.Currency,
		BillingIntervalMonths: count(r.BillingIntervalMonths),
		ExternalProductID:     text(r.ExternalProductID),
		CreatedAt:             r.CreatedAt.UTC(),
		LastUpdatedAt:         utcPtr(r.LastUpdatedAt),
		ArchivedAt:            utcPtr(r.ArchivedAt),
	}
}

func productPageRow(r *billingdb.ListProductsRow) pageRow[Product] {
	return pageRow[Product]{
		value: &Product{
			ID:                    r.ID,
			Scope:                 r.Scope,
			Name:                  r.Name,
			Description:           r.Description,
			Kind:                  Kind(r.Kind),
			AmountCents:           r.AmountCents,
			Currency:              r.Currency,
			BillingIntervalMonths: count(r.BillingIntervalMonths),
			ExternalProductID:     text(r.ExternalProductID),
			CreatedAt:             r.CreatedAt.UTC(),
			LastUpdatedAt:         utcPtr(r.LastUpdatedAt),
			ArchivedAt:            utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createProductParams(p *Product, scope tenancy.Scope) billingdb.CreateProductParams {
	return billingdb.CreateProductParams{
		ID:                    p.ID,
		Scope:                 scope,
		Name:                  p.Name,
		Description:           p.Description,
		Kind:                  string(p.Kind),
		AmountCents:           p.AmountCents,
		Currency:              p.Currency,
		BillingIntervalMonths: months(p.BillingIntervalMonths),
		ExternalProductID:     nullable(p.ExternalProductID),
	}
}

func updateProductParams(p *Product, scope tenancy.Scope) billingdb.UpdateProductParams {
	return billingdb.UpdateProductParams{
		Name:                  p.Name,
		Description:           p.Description,
		Kind:                  string(p.Kind),
		AmountCents:           p.AmountCents,
		Currency:              p.Currency,
		BillingIntervalMonths: months(p.BillingIntervalMonths),
		ExternalProductID:     nullable(p.ExternalProductID),
		ID:                    p.ID,
		Scope:                 scope,
	}
}

func listProductsParams(scope tenancy.Scope, filter *filtering.QueryFilter) billingdb.ListProductsParams {
	w := windowFrom(filter)

	return billingdb.ListProductsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

// Subscriptions.

func subscriptionFromRow(r *billingdb.GetSubscriptionRow) *Subscription {
	return &Subscription{
		ID:                     r.ID,
		Scope:                  r.Scope,
		BelongsToAccount:       r.BelongsToAccount,
		ProductID:              r.ProductID,
		ExternalSubscriptionID: text(r.ExternalSubscriptionID),
		Status:                 capitalism.SubscriptionStatus(r.Status),
		CurrentPeriodStart:     r.CurrentPeriodStart.UTC(),
		CurrentPeriodEnd:       r.CurrentPeriodEnd.UTC(),
		CreatedAt:              r.CreatedAt.UTC(),
		LastUpdatedAt:          utcPtr(r.LastUpdatedAt),
		ArchivedAt:             utcPtr(r.ArchivedAt),
	}
}

func subscriptionPageRow(r *billingdb.ListSubscriptionsRow) pageRow[Subscription] {
	return pageRow[Subscription]{
		value: &Subscription{
			ID:                     r.ID,
			Scope:                  r.Scope,
			BelongsToAccount:       r.BelongsToAccount,
			ProductID:              r.ProductID,
			ExternalSubscriptionID: text(r.ExternalSubscriptionID),
			Status:                 capitalism.SubscriptionStatus(r.Status),
			CurrentPeriodStart:     r.CurrentPeriodStart.UTC(),
			CurrentPeriodEnd:       r.CurrentPeriodEnd.UTC(),
			CreatedAt:              r.CreatedAt.UTC(),
			LastUpdatedAt:          utcPtr(r.LastUpdatedAt),
			ArchivedAt:             utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createSubscriptionParams(s *Subscription, scope tenancy.Scope) billingdb.CreateSubscriptionParams {
	return billingdb.CreateSubscriptionParams{
		ID:                     s.ID,
		Scope:                  scope,
		BelongsToAccount:       s.BelongsToAccount,
		ProductID:              s.ProductID,
		ExternalSubscriptionID: nullable(s.ExternalSubscriptionID),
		Status:                 string(s.Status),
		CurrentPeriodStart:     s.CurrentPeriodStart.UTC(),
		CurrentPeriodEnd:       s.CurrentPeriodEnd.UTC(),
	}
}

func updateSubscriptionParams(s *Subscription, scope tenancy.Scope) billingdb.UpdateSubscriptionParams {
	return billingdb.UpdateSubscriptionParams{
		ProductID:              s.ProductID,
		ExternalSubscriptionID: nullable(s.ExternalSubscriptionID),
		Status:                 string(s.Status),
		CurrentPeriodStart:     s.CurrentPeriodStart.UTC(),
		CurrentPeriodEnd:       s.CurrentPeriodEnd.UTC(),
		ID:                     s.ID,
		Scope:                  scope,
	}
}

func listSubscriptionsParams(scope tenancy.Scope, filter *filtering.QueryFilter) billingdb.ListSubscriptionsParams {
	w := windowFrom(filter)

	return billingdb.ListSubscriptionsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listSubscriptionsForAccountParams(
	scope tenancy.Scope,
	accountID string,
	filter *filtering.QueryFilter,
) billingdb.ListSubscriptionsForAccountParams {
	w := windowFrom(filter)

	return billingdb.ListSubscriptionsForAccountParams{
		CreatedAfter:     w.createdAfter,
		CreatedBefore:    w.createdBefore,
		UpdatedAfter:     w.updatedAfter,
		UpdatedBefore:    w.updatedBefore,
		IncludeArchived:  w.includeArchived,
		Scope:            scope,
		BelongsToAccount: accountID,
		PageCursor:       w.pageCursor,
		ResultLimit:      w.resultLimit,
	}
}

func listCurrentSubscriptionsParams(
	scope tenancy.Scope,
	accountID string,
	asOf time.Time,
	filter *filtering.QueryFilter,
) billingdb.ListCurrentSubscriptionsParams {
	w := windowFrom(filter)

	return billingdb.ListCurrentSubscriptionsParams{
		CreatedAfter:     w.createdAfter,
		CreatedBefore:    w.createdBefore,
		UpdatedAfter:     w.updatedAfter,
		UpdatedBefore:    w.updatedBefore,
		IncludeArchived:  w.includeArchived,
		Scope:            scope,
		BelongsToAccount: accountID,
		CurrentAsOf:      asOf.UTC(),
		PageCursor:       w.pageCursor,
		ResultLimit:      w.resultLimit,
	}
}

// Purchases.

func purchaseFromRow(r *billingdb.GetPurchaseRow) *Purchase {
	return &Purchase{
		ID:                    r.ID,
		Scope:                 r.Scope,
		BelongsToAccount:      r.BelongsToAccount,
		ProductID:             r.ProductID,
		ExternalTransactionID: text(r.ExternalTransactionID),
		AmountCents:           r.AmountCents,
		Currency:              r.Currency,
		CompletedAt:           utcPtr(r.CompletedAt),
		CreatedAt:             r.CreatedAt.UTC(),
		LastUpdatedAt:         utcPtr(r.LastUpdatedAt),
		ArchivedAt:            utcPtr(r.ArchivedAt),
	}
}

func purchasePageRow(r *billingdb.ListPurchasesRow) pageRow[Purchase] {
	return pageRow[Purchase]{
		value: &Purchase{
			ID:                    r.ID,
			Scope:                 r.Scope,
			BelongsToAccount:      r.BelongsToAccount,
			ProductID:             r.ProductID,
			ExternalTransactionID: text(r.ExternalTransactionID),
			AmountCents:           r.AmountCents,
			Currency:              r.Currency,
			CompletedAt:           utcPtr(r.CompletedAt),
			CreatedAt:             r.CreatedAt.UTC(),
			LastUpdatedAt:         utcPtr(r.LastUpdatedAt),
			ArchivedAt:            utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createPurchaseParams(p *Purchase, scope tenancy.Scope) billingdb.CreatePurchaseParams {
	return billingdb.CreatePurchaseParams{
		ID:                    p.ID,
		Scope:                 scope,
		BelongsToAccount:      p.BelongsToAccount,
		ProductID:             p.ProductID,
		ExternalTransactionID: nullable(p.ExternalTransactionID),
		AmountCents:           p.AmountCents,
		Currency:              p.Currency,
		CompletedAt:           utcPtr(p.CompletedAt),
	}
}

func listPurchasesParams(scope tenancy.Scope, filter *filtering.QueryFilter) billingdb.ListPurchasesParams {
	w := windowFrom(filter)

	return billingdb.ListPurchasesParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listPurchasesForAccountParams(
	scope tenancy.Scope,
	accountID string,
	filter *filtering.QueryFilter,
) billingdb.ListPurchasesForAccountParams {
	w := windowFrom(filter)

	return billingdb.ListPurchasesForAccountParams{
		CreatedAfter:     w.createdAfter,
		CreatedBefore:    w.createdBefore,
		UpdatedAfter:     w.updatedAfter,
		UpdatedBefore:    w.updatedBefore,
		IncludeArchived:  w.includeArchived,
		Scope:            scope,
		BelongsToAccount: accountID,
		PageCursor:       w.pageCursor,
		ResultLimit:      w.resultLimit,
	}
}

// Transactions.

func transactionFromRow(r *billingdb.GetTransactionRow) *Transaction {
	return &Transaction{
		ID:                    r.ID,
		Scope:                 r.Scope,
		BelongsToAccount:      r.BelongsToAccount,
		SubscriptionID:        text(r.SubscriptionID),
		PurchaseID:            text(r.PurchaseID),
		ExternalTransactionID: text(r.ExternalTransactionID),
		Status:                TransactionStatus(r.Status),
		AmountCents:           r.AmountCents,
		Currency:              r.Currency,
		CreatedAt:             r.CreatedAt.UTC(),
		LastUpdatedAt:         utcPtr(r.LastUpdatedAt),
		ArchivedAt:            utcPtr(r.ArchivedAt),
	}
}

func transactionPageRow(r *billingdb.ListTransactionsRow) pageRow[Transaction] {
	return pageRow[Transaction]{
		value: &Transaction{
			ID:                    r.ID,
			Scope:                 r.Scope,
			BelongsToAccount:      r.BelongsToAccount,
			SubscriptionID:        text(r.SubscriptionID),
			PurchaseID:            text(r.PurchaseID),
			ExternalTransactionID: text(r.ExternalTransactionID),
			Status:                TransactionStatus(r.Status),
			AmountCents:           r.AmountCents,
			Currency:              r.Currency,
			CreatedAt:             r.CreatedAt.UTC(),
			LastUpdatedAt:         utcPtr(r.LastUpdatedAt),
			ArchivedAt:            utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createTransactionParams(t *Transaction, scope tenancy.Scope) billingdb.CreateTransactionParams {
	return billingdb.CreateTransactionParams{
		ID:                    t.ID,
		Scope:                 scope,
		BelongsToAccount:      t.BelongsToAccount,
		SubscriptionID:        nullable(t.SubscriptionID),
		PurchaseID:            nullable(t.PurchaseID),
		ExternalTransactionID: nullable(t.ExternalTransactionID),
		Status:                string(t.Status),
		AmountCents:           t.AmountCents,
		Currency:              t.Currency,
	}
}

func listTransactionsParams(scope tenancy.Scope, filter *filtering.QueryFilter) billingdb.ListTransactionsParams {
	w := windowFrom(filter)

	return billingdb.ListTransactionsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listTransactionsForAccountParams(
	scope tenancy.Scope,
	accountID string,
	filter *filtering.QueryFilter,
) billingdb.ListTransactionsForAccountParams {
	w := windowFrom(filter)

	return billingdb.ListTransactionsForAccountParams{
		CreatedAfter:     w.createdAfter,
		CreatedBefore:    w.createdBefore,
		UpdatedAfter:     w.updatedAfter,
		UpdatedBefore:    w.updatedBefore,
		IncludeArchived:  w.includeArchived,
		Scope:            scope,
		BelongsToAccount: accountID,
		PageCursor:       w.pageCursor,
		ResultLimit:      w.resultLimit,
	}
}
