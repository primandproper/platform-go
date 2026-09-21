package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// grantsKey carries the authority a test wants its caller to have.
type grantsKey struct{}

// withGrants narrows a request context to the named grants, which is how a test
// describes a caller who may read and may not archive.
func withGrants(ctx context.Context, perms ...authorization.Permission) context.Context {
	return context.WithValue(ctx, grantsKey{}, authorization.NewGrants(authorization.NewPermissionSet(perms...)))
}

// extractGrants is the authorization.GrantsExtractor every harness here is built
// with.
//
// A context nothing narrowed carries every grant, because the rest of this suite
// is about what a handler does and not about what a policy allows — a default of
// "nothing" would make every existing test a test of this file.
func extractGrants(ctx context.Context) (authorization.Grants, bool) {
	grants, ok := ctx.Value(grantsKey{}).(authorization.Grants)
	if !ok {
		return authorization.AllowAll(), true
	}

	return grants, true
}

// includeArchived is the filter a client sets to ask for the retired rows.
func includeArchived() *filteringpb.QueryFilter {
	include := true

	return &filteringpb.QueryFilter{IncludeArchived: &include}
}

// archiveProduct retires a seeded product through the store, so the row a test
// is about was withdrawn without going through the surface the test is about.
func (h *harness) archiveProduct(tb testing.TB, productID string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		_, err := h.store.ArchiveProduct(tb.Context(), tx, testScope, productID)

		return err
	}))
}

// TestListProducts_Archived is the finding this file closes: include_archived
// arrived on the wire and reached the store untouched, so the read grant was
// enough to see what only the archive grant retires.
func TestListProducts_Archived(T *testing.T) {
	T.Parallel()

	T.Run("a caller without the archive grant is confined to the live rows", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		live := h.seedProduct(t, testScope)
		retired := h.seedProduct(t, testScope)
		h.archiveProduct(t, retired.ID)

		ctx := withGrants(h.ctx(t, testUser, testAccount), billinggrpc.PermissionReadProducts)

		res, err := h.server.ListProducts(ctx, &billingpb.ListProductsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		ids := productIDs(res.GetResults())
		test.SliceContains(t, ids, live.ID)
		test.SliceNotContains(t, ids, retired.ID)
	})

	T.Run("a holder of the archive grant receives them", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		retired := h.seedProduct(t, testScope)
		h.archiveProduct(t, retired.ID)

		ctx := withGrants(h.ctx(t, testUser, testAccount),
			billinggrpc.PermissionReadProducts, billinggrpc.PermissionArchiveProducts)

		res, err := h.server.ListProducts(ctx, &billingpb.ListProductsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.SliceContains(t, productIDs(res.GetResults()), retired.ID)
	})

	// The fail-closed half of callerGrants' default: a surface that cannot see
	// what the caller may do cannot tell an archivist from anybody else.
	T.Run("a server with no grants extractor confines every read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, billinggrpc.WithGrantsExtractor(nil))
		retired := h.seedProduct(t, testScope)
		h.archiveProduct(t, retired.ID)

		res, err := h.server.ListProducts(h.ctx(t, testUser, testAccount),
			&billingpb.ListProductsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.SliceNotContains(t, productIDs(res.GetResults()), retired.ID)
	})
}

func productIDs(products []*billingpb.Product) []string {
	ids := make([]string, 0, len(products))
	for _, p := range products {
		ids = append(ids, p.GetId())
	}

	return ids
}
