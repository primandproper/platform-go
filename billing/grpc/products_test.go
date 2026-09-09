package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The catalog is the one part of this surface that answers to the scope alone.
// These tests are about that: what a caller in the scope may do to it, and that
// the scope is the caller's own rather than anything they sent.

func TestCreateProduct(T *testing.T) {
	T.Parallel()

	T.Run("stocks the caller's catalog", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateProduct(h.ctx(t, testUser, testAccount),
			&billingpb.CreateProductRequest{Input: creationInput()})
		must.NoError(t, err)
		must.NotNil(t, res.GetResult())

		test.NotEq(t, "", res.GetResult().GetId())
		test.EqOp(t, "a thing", res.GetResult().GetName())
		test.EqOp(t, billingpb.ProductKind_PRODUCT_KIND_ONE_TIME, res.GetResult().GetKind())
		test.EqOp(t, int64(500), res.GetResult().GetAmountCents())

		// The created time is the database's and is on the wire; the two
		// nullable ones are unset, because nothing has revised or archived it.
		must.NotNil(t, res.GetResult().GetCreatedAt())
		test.Nil(t, res.GetResult().GetLastUpdatedAt())
		test.Nil(t, res.GetResult().GetArchivedAt())

		// It is in the caller's scope and nobody else's, which is the property
		// no request field could have expressed.
		stored, readErr := h.store.GetProduct(t.Context(), h.db.Reader(), testScope, res.GetResult().GetId())
		must.NoError(t, readErr)
		test.EqOp(t, testScope, stored.Scope)
	})

	T.Run("refuses a request that named no input", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.CreateProduct(h.ctx(t, testUser, testAccount),
			&billingpb.CreateProductRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("refuses a product that named no kind", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		input := creationInput()
		input.Kind = billingpb.ProductKind_PRODUCT_KIND_UNSPECIFIED

		// The unspecified kind becomes the empty Kind rather than a default, so
		// the store refuses it by name. A converter that had picked one would
		// have decided what this deployment sells.
		_, err := h.server.CreateProduct(h.ctx(t, testUser, testAccount),
			&billingpb.CreateProductRequest{Input: input})
		must.Error(t, err)
		test.ErrorIs(t, err, billing.ErrInvalidKind)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("refuses a currency that is not three characters", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		input := creationInput()
		input.Currency = "dollars"

		_, err := h.server.CreateProduct(h.ctx(t, testUser, testAccount),
			&billingpb.CreateProductRequest{Input: input})
		must.Error(t, err)
		test.ErrorIs(t, err, billing.ErrInvalidCurrency)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		// The sentinel is client-safe, so the sentence a caller reads names the
		// field rather than the call. Seven refusals here share this code, which
		// is why that matters.
		test.StrContains(t, status.Convert(err).Message(), "three-character")
	})

	T.Run("reports a provider id already claimed as a conflict", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := h.ctx(t, testUser, testAccount)

		input := creationInput()
		input.ExternalProductId = "prod_stripe_1"

		_, err := h.server.CreateProduct(ctx, &billingpb.CreateProductRequest{Input: input})
		must.NoError(t, err)

		_, err = h.server.CreateProduct(ctx, &billingpb.CreateProductRequest{Input: input})
		must.Error(t, err)
		test.ErrorIs(t, err, billing.ErrProductExists)
		test.EqOp(t, codes.AlreadyExists, status.Code(err))
	})
}

func TestGetProduct(T *testing.T) {
	T.Parallel()

	T.Run("reads a product in the caller's catalog", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		product := h.seedProduct(t, testScope)

		res, err := h.server.GetProduct(h.ctx(t, testUser, testAccount),
			&billingpb.GetProductRequest{ProductId: product.ID})
		must.NoError(t, err)
		test.EqOp(t, product.ID, res.GetResult().GetId())
	})

	T.Run("does not read another tenant's catalog", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		product := h.seedProduct(t, testScope)

		// The same id, asked for by a caller the extractor put in another
		// tenant. There is no field on the request that could have said which
		// tenant, which is the whole reason the answer is an absence.
		_, err := h.server.GetProduct(h.otherScopeCtx(t),
			&billingpb.GetProductRequest{ProductId: product.ID})
		must.Error(t, err)
		test.ErrorIs(t, err, billing.ErrProductNotFound)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("answers an unknown id as an absence", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.GetProduct(h.ctx(t, testUser, testAccount),
			&billingpb.GetProductRequest{ProductId: "nope"})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}

func TestListProducts(T *testing.T) {
	T.Parallel()

	T.Run("pages the caller's catalog and nobody else's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seedProduct(t, testScope)
		h.seedProduct(t, otherScope)

		res, err := h.server.ListProducts(h.ctx(t, testUser, testAccount),
			&billingpb.ListProductsRequest{})
		must.NoError(t, err)
		must.NotNil(t, res.GetPagination())
		test.SliceLen(t, 1, res.GetResults())
	})

	T.Run("refuses a malformed filter", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.ListProducts(h.ctx(t, testUser, testAccount),
			&billingpb.ListProductsRequest{Filter: badFilter()})
		test.Nil(t, res)
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestUpdateProduct(T *testing.T) {
	T.Parallel()

	T.Run("returns the row as stored rather than the request echoed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		product := h.seedProduct(t, testScope)

		res, err := h.server.UpdateProduct(h.ctx(t, testUser, testAccount),
			&billingpb.UpdateProductRequest{ProductId: product.ID, Input: updateInput()})
		must.NoError(t, err)
		must.NotNil(t, res.GetResult())

		test.EqOp(t, "a renamed thing", res.GetResult().GetName())
		test.EqOp(t, int64(750), res.GetResult().GetAmountCents())

		// The read-back is what makes this the row: the update statement assigns
		// no timestamp the handler could have known.
		must.NotNil(t, res.GetResult().GetLastUpdatedAt())
		test.EqOp(t, product.CreatedAt.UTC(), res.GetResult().GetCreatedAt().AsTime().UTC())
	})

	T.Run("refuses a request that named no input", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		product := h.seedProduct(t, testScope)

		_, err := h.server.UpdateProduct(h.ctx(t, testUser, testAccount),
			&billingpb.UpdateProductRequest{ProductId: product.ID})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("does not revise another tenant's product", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		product := h.seedProduct(t, testScope)

		_, err := h.server.UpdateProduct(h.otherScopeCtx(t),
			&billingpb.UpdateProductRequest{ProductId: product.ID, Input: updateInput()})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))

		unchanged, readErr := h.store.GetProduct(t.Context(), h.db.Reader(), testScope, product.ID)
		must.NoError(t, readErr)
		test.EqOp(t, product.Name, unchanged.Name)
	})
}

func TestArchiveProduct(T *testing.T) {
	T.Parallel()

	T.Run("takes a product off the shelf", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		product := h.seedProduct(t, testScope)
		ctx := h.ctx(t, testUser, testAccount)

		_, err := h.server.ArchiveProduct(ctx, &billingpb.ArchiveProductRequest{ProductId: product.ID})
		must.NoError(t, err)

		_, err = h.server.GetProduct(ctx, &billingpb.GetProductRequest{ProductId: product.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("does not archive another tenant's product", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		product := h.seedProduct(t, testScope)

		_, err := h.server.ArchiveProduct(h.otherScopeCtx(t),
			&billingpb.ArchiveProductRequest{ProductId: product.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}
