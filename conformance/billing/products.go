package billing

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func products(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a product is stocked in the caller's catalog and nobody else's", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)

		created := stock(t, mine)
		test.NotEqOp(t, "", created.GetId())
		test.EqOp(t, "a thing", created.GetName())
		test.EqOp(t, billingpb.ProductKind_PRODUCT_KIND_ONE_TIME, created.GetKind())
		test.EqOp(t, int64(500), created.GetAmountCents())

		// The created time is the database's; the two nullable ones are unset,
		// because nothing has revised or archived it yet, and a zero timestamp
		// in their place would read as 1970.
		test.NotNil(t, created.GetCreatedAt())
		test.Nil(t, created.GetLastUpdatedAt())
		test.Nil(t, created.GetArchivedAt())

		// The positive control. The absences below are also what a deployment
		// that reaches no catalog at all would answer.
		read, err := mine.Surfaces.Billing.GetProduct(mine.Context(t.Context()),
			&billingpb.GetProductRequest{ProductId: created.GetId()})
		must.NoError(t, err, must.Sprint("the caller cannot read the product it just stocked; the absence below proves nothing"))
		test.EqOp(t, created.GetId(), read.GetResult().GetId())

		// Absent rather than forbidden. No field on the request could have
		// named a tenant, so the only thing that can have kept this out is the
		// scope the caller was resolved into.
		_, err = theirs.Surfaces.Billing.GetProduct(theirs.Context(t.Context()),
			&billingpb.GetProductRequest{ProductId: created.GetId()})
		must.Error(t, err, must.Sprint("a neighboring tenant's product was readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("a catalog listing pages the caller's tenant only", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		needsAccount(t, mine)
		other := colleague(t, s, mine)

		own, shared, foreign := stock(t, mine), stock(t, other), stock(t, theirs)

		// Presence and absence of three named products, never a count: the
		// database may be one the suite does not own.
		ids := catalog(t, mine)
		test.SliceContains(t, ids, own.GetId(),
			test.Sprint("the caller's own product was missing from its catalog"))
		test.SliceContains(t, ids, shared.GetId(),
			test.Sprint("a colleague's product was missing; a catalog is the tenant's, not the stocker's"))
		test.SliceNotContains(t, ids, foreign.GetId(),
			test.Sprint("a neighboring tenant's product reached this catalog"))
	})

	t.Run("a product with no input is refused", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		_, err := mine.Surfaces.Billing.CreateProduct(mine.Context(t.Context()), &billingpb.CreateProductRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("a product that names no kind is refused rather than given one", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		// A converter that picked a default kind would have decided what this
		// deployment sells.
		input := productInput()
		input.Kind = billingpb.ProductKind_PRODUCT_KIND_UNSPECIFIED

		_, err := mine.Surfaces.Billing.CreateProduct(mine.Context(t.Context()),
			&billingpb.CreateProductRequest{Input: input})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("a currency that is not three characters is refused in words the caller can act on", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		input := productInput()
		input.Currency = "dollars"

		_, err := mine.Surfaces.Billing.CreateProduct(mine.Context(t.Context()),
			&billingpb.CreateProductRequest{Input: input})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		// Several refusals on this surface share the code, so the sentence is
		// what tells the person filling in the form which field to fix.
		test.StrContains(t, status.Convert(err).Message(), "three-character")
	})

	t.Run("a provider identifier already claimed is a conflict", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		ctx := mine.Context(t.Context())
		input := productInput()

		_, err := mine.Surfaces.Billing.CreateProduct(ctx, &billingpb.CreateProductRequest{Input: input})
		must.NoError(t, err)

		_, err = mine.Surfaces.Billing.CreateProduct(ctx, &billingpb.CreateProductRequest{Input: input})
		must.Error(t, err)
		test.EqOp(t, codes.AlreadyExists, status.Code(err))
	})

	// The granted half of include_archived. An administrator holds the grant
	// that withdraws a product from sale, which is the grant a deployment
	// reads the archive off, so asking is answered with the withdrawn row.
	t.Run("an administrator asking for withdrawn products receives them", func(t *testing.T) {
		t.Parallel()

		admin := s.Subject(t, conformance.AsAdmin())
		live := stock(t, admin)
		withdrawn := stock(t, admin)

		ctx := admin.Context(t.Context())

		_, err := admin.Surfaces.Billing.ArchiveProduct(ctx,
			&billingpb.ArchiveProductRequest{ProductId: withdrawn.GetId()})
		must.NoError(t, err)

		// The control: without asking, the withdrawn product is not listed.
		test.SliceNotContains(t, catalog(t, admin), withdrawn.GetId())

		include := true

		page, err := admin.Surfaces.Billing.ListProducts(ctx,
			&billingpb.ListProductsRequest{Filter: &filteringpb.QueryFilter{IncludeArchived: &include}})
		must.NoError(t, err)

		ids := make([]string, 0, len(page.GetResults()))
		for _, p := range page.GetResults() {
			ids = append(ids, p.GetId())
		}

		test.SliceContains(t, ids, live.GetId())
		test.SliceContains(t, ids, withdrawn.GetId(),
			test.Sprint("an administrator asked for withdrawn products and was answered without them"))
	})

	t.Run("an absent product is reported as absent", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		_, err := mine.Surfaces.Billing.GetProduct(mine.Context(t.Context()),
			&billingpb.GetProductRequest{ProductId: identifiers.New()})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("a revision answers with the row as stored rather than the request echoed", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		ctx := mine.Context(t.Context())
		product := stock(t, mine)

		read, err := mine.Surfaces.Billing.GetProduct(ctx, &billingpb.GetProductRequest{ProductId: product.GetId()})
		must.NoError(t, err)

		revised, err := mine.Surfaces.Billing.UpdateProduct(ctx, &billingpb.UpdateProductRequest{
			ProductId: product.GetId(),
			Input: &billingpb.ProductUpdateInput{
				Name:        "a renamed thing",
				Description: "still sold",
				Kind:        billingpb.ProductKind_PRODUCT_KIND_ONE_TIME,
				Currency:    currencyUSD,
				AmountCents: 750,
			},
		})
		must.NoError(t, err)
		test.EqOp(t, "a renamed thing", revised.GetResult().GetName())
		test.EqOp(t, int64(750), revised.GetResult().GetAmountCents())

		// Neither stamp is something the request carried, so both being right
		// is what makes the answer the row. Whole seconds, because that is all
		// the weakest dialect keeps.
		test.NotNil(t, revised.GetResult().GetLastUpdatedAt(),
			test.Sprint("a revised product came back with no revision stamp"))
		test.EqOp(t,
			read.GetResult().GetCreatedAt().AsTime().Truncate(time.Second),
			revised.GetResult().GetCreatedAt().AsTime().Truncate(time.Second),
			test.Sprint("a revision moved the product's creation time"))
	})

	t.Run("a revision with no input is refused", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		product := stock(t, mine)

		_, err := mine.Surfaces.Billing.UpdateProduct(mine.Context(t.Context()),
			&billingpb.UpdateProductRequest{ProductId: product.GetId()})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("a revision of a neighboring tenant's product is absent and changes nothing", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		product := stock(t, mine)

		_, err := theirs.Surfaces.Billing.UpdateProduct(theirs.Context(t.Context()), &billingpb.UpdateProductRequest{
			ProductId: product.GetId(),
			Input: &billingpb.ProductUpdateInput{
				Name:        "somebody else's name for it",
				Kind:        billingpb.ProductKind_PRODUCT_KIND_ONE_TIME,
				Currency:    currencyUSD,
				AmountCents: 1,
			},
		})
		must.Error(t, err, must.Sprint("a neighboring tenant revised this caller's product"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		// The owner still reads it, as it was, which is both the control for
		// the absence above and the proof the refusal wrote nothing.
		read, err := mine.Surfaces.Billing.GetProduct(mine.Context(t.Context()),
			&billingpb.GetProductRequest{ProductId: product.GetId()})
		must.NoError(t, err)
		test.EqOp(t, product.GetName(), read.GetResult().GetName())
		test.EqOp(t, product.GetAmountCents(), read.GetResult().GetAmountCents())
	})

	t.Run("archiving a product takes it off the shelf", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		ctx := mine.Context(t.Context())
		product := stock(t, mine)

		must.SliceContains(t, catalog(t, mine), product.GetId())

		_, err := mine.Surfaces.Billing.ArchiveProduct(ctx, &billingpb.ArchiveProductRequest{ProductId: product.GetId()})
		must.NoError(t, err)

		_, err = mine.Surfaces.Billing.GetProduct(ctx, &billingpb.GetProductRequest{ProductId: product.GetId()})
		must.Error(t, err, must.Sprint("an archived product was still readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.SliceNotContains(t, catalog(t, mine), product.GetId(),
			test.Sprint("an archived product was still in the catalog"))
	})

	t.Run("archiving a neighboring tenant's product is absent and changes nothing", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		product := stock(t, mine)

		_, err := theirs.Surfaces.Billing.ArchiveProduct(theirs.Context(t.Context()),
			&billingpb.ArchiveProductRequest{ProductId: product.GetId()})
		must.Error(t, err, must.Sprint("a neighboring tenant archived this caller's product"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		read, err := mine.Surfaces.Billing.GetProduct(mine.Context(t.Context()),
			&billingpb.GetProductRequest{ProductId: product.GetId()})
		must.NoError(t, err, must.Sprint("a refused archival withdrew the product anyway"))
		test.Nil(t, read.GetResult().GetArchivedAt())
	})
}
