package billing

import (
	"testing"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test/must"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "billing"

// The calls this suite makes, as the names a caller is minted to make them by.
const (
	archiveProduct              = billingpb.BillingService_ArchiveProduct_FullMethodName
	archiveSubscription         = billingpb.BillingService_ArchiveSubscription_FullMethodName
	createProduct               = billingpb.BillingService_CreateProduct_FullMethodName
	getProduct                  = billingpb.BillingService_GetProduct_FullMethodName
	getSubscription             = billingpb.BillingService_GetSubscription_FullMethodName
	listCurrentSubscriptions    = billingpb.BillingService_ListCurrentSubscriptions_FullMethodName
	listProducts                = billingpb.BillingService_ListProducts_FullMethodName
	listSubscriptions           = billingpb.BillingService_ListSubscriptions_FullMethodName
	listSubscriptionsForAccount = billingpb.BillingService_ListSubscriptionsForAccount_FullMethodName
	updateProduct               = billingpb.BillingService_UpdateProduct_FullMethodName
)

// method is the full name of the billing RPC called name.
func method(name string) string {
	return "/" + billingpb.BillingService_ServiceDesc.ServiceName + "/" + name
}

// currencyUSD is the currency every product here is priced in. Which one is
// immaterial; that it is one the surface accepts is what matters.
const currencyUSD = "USD"

// Suite is the billing surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
		Mounted: func(s conformance.Surfaces) bool { return s.Billing != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("products", func(t *testing.T) {
		t.Parallel()
		products(t, s)
	})
	t.Run("accounts", func(t *testing.T) {
		t.Parallel()
		accounts(t, s)
	})
	t.Run("subscriptions", func(t *testing.T) {
		t.Parallel()
		subscriptions(t, s)
	})
}

// twoTenants mints two callers and refuses to proceed if the subject put them
// in one tenant.
//
// A subject whose NewSubject ignored its request and handed back one caller
// twice would make every confinement assertion here compare a catalog with
// itself, and all of them would pass.
//
// opts are applied to both, and name the calls each makes.
func twoTenants(t *testing.T, s *conformance.Session, opts ...conformance.SubjectOption) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.TwoTenants(t, surface, opts...)

	return mine, theirs
}

// twoOperators mints a caller making methods in each of two tenants, for the
// confinement assertions about the catalog.
func twoOperators(t *testing.T, s *conformance.Session, methods ...string) (mine, theirs *conformance.Subject) {
	t.Helper()

	return twoTenants(t, s, conformance.Making(methods...))
}

// colleague mints a second caller in of's tenant, with an account of their own
// that of holds no membership in, making methods. A subject that cannot put two
// callers in one tenant declines, and the assertion that asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject, methods ...string) *conformance.Subject {
	t.Helper()

	other := s.Subject(t, conformance.Making(methods...), conformance.InTenant(surface, of.ScopeFor(surface)))
	needsAccount(t, other)

	must.StrNotEqFold(t, of.AccountID, other.AccountID,
		must.Sprint("the subject minted a colleague on the same account; the account rule cannot be observed"))

	return other
}

// needsAccount skips unless the subject surfaced the caller's account, which
// every account-keyed assertion here names.
func needsAccount(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.AccountID == "" {
		t.Skip("conformance: this subject does not surface the caller's account identifier")
	}
}

// subscribed makes a paid subscription exist for sub's account, or skips where
// the subject cannot say how its deployment would.
func subscribed(t *testing.T, s *conformance.Session, sub *conformance.Subject) string {
	t.Helper()

	subscribe := s.Seams().Actions.Subscribed
	s.NeedsAction(t, subscribe != nil, "subscribed")
	needsAccount(t, sub)

	subscription, err := subscribe(t.Context(), sub.ScopeFor(surface), sub.AccountID)
	must.NoError(t, err, must.Sprint("making a paid subscription exist"))
	must.NotNil(t, subscription, must.Sprint("the subscribed action reported no subscription"))
	must.StrNotEqFold(t, "", subscription.ID, must.Sprint("the subscribed action reported a subscription with no identifier"))

	return subscription.ID
}

// productInput is the smallest product a catalog accepts, with a provider
// identifier nobody else has claimed.
func productInput() *billingpb.ProductCreationInput {
	return &billingpb.ProductCreationInput{
		Name:              "a thing",
		Description:       "a thing that is sold",
		Kind:              billingpb.ProductKind_PRODUCT_KIND_ONE_TIME,
		Currency:          currencyUSD,
		AmountCents:       500,
		ExternalProductId: "prod_" + identifiers.New(),
	}
}

// stock puts a product in sub's catalog through the surface, so sub names
// CreateProduct among the calls it was minted to make.
func stock(t *testing.T, sub *conformance.Subject) *billingpb.Product {
	t.Helper()

	created, err := sub.Surfaces.Billing.CreateProduct(sub.Context(t.Context()),
		&billingpb.CreateProductRequest{Input: productInput()})
	must.NoError(t, err, must.Sprint("stocking a product"))
	must.NotNil(t, created.GetResult())

	return created.GetResult()
}

// catalog is the identifiers on the first page of sub's catalog.
func catalog(t *testing.T, sub *conformance.Subject) []string {
	t.Helper()

	page, err := sub.Surfaces.Billing.ListProducts(sub.Context(t.Context()), &billingpb.ListProductsRequest{})
	must.NoError(t, err)
	must.NotNil(t, page.GetPagination(), must.Sprint("a paged read answered with no pagination"))

	ids := make([]string, 0, len(page.GetResults()))
	for _, p := range page.GetResults() {
		ids = append(ids, p.GetId())
	}

	return ids
}

func subscriptionIDs(subscriptions []*billingpb.Subscription) []string {
	ids := make([]string, 0, len(subscriptions))
	for _, s := range subscriptions {
		ids = append(ids, s.GetId())
	}

	return ids
}

// badFilter is a page request no converter can read. The sort direction is
// the field with a closed set of spellings, so an unrecognized one is refused
// by the filter's own conversion rather than by anything this surface decides.
func badFilter() *filteringpb.QueryFilter {
	sideways := "sideways"

	return &filteringpb.QueryFilter{SortBy: &sideways}
}
