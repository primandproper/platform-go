package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"

	"github.com/primandproper/primitives-go/database"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The catalog: what the deployment sells.
//
// Two reads and three writes, and the split in who they are for is the whole of
// this file. The catalog is scope-wide — it carries no account, because a
// product is a thing on offer and who bought it is a subscription or a purchase
// — so the reads answer to the scope alone and there is no account for
// [AccountAuthorizer] to be asked about. The three writes are administrative,
// behind three grants that a customer-facing role does not hold.
//
// Each method is one call plus its conversion, and a write is that call inside
// one transaction the handler opens. The error goes through
// grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*: the
// encoding interceptor re-runs the registered mappers over the preserved chain,
// so billing.GRPCMapper wins over the guess made here and no handler on this
// surface switches on a sentinel.

// CreateProduct stocks the caller's catalog.
func (s *Server) CreateProduct(
	ctx context.Context,
	request *billingpb.CreateProductRequest,
) (*billingpb.CreateProductResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_CreateProduct_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	product := productFromCreationInput(request.GetInput())
	if product == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "creating a product")

		return nil, err
	}

	var created *billing.Product

	// The transaction is opened here because billing ships no service to open
	// one. It holds a single write, which is exactly the case Client.
	// WithTransaction's documentation describes as a caller with nothing to
	// join — a consumer writing an audit entry beside this reaches the store
	// directly and passes their own Tx.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var writeErr error
		created, writeErr = s.store.CreateProduct(ctx, tx, req.scope, product)

		return writeErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "creating a product")

		return nil, err
	}

	req.op.Set(productKey, created.ID)

	return &billingpb.CreateProductResponse{Result: ProductToProto(created)}, nil
}

// GetProduct reads one live product from the caller's catalog.
func (s *Server) GetProduct(
	ctx context.Context,
	request *billingpb.GetProductRequest,
) (*billingpb.GetProductResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_GetProduct_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetProductId()
	req.op.Set(productKey, id)

	product, err := s.store.GetProduct(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading product %q", id)

		return nil, err
	}

	return &billingpb.GetProductResponse{Result: ProductToProto(product)}, nil
}

// ListProducts pages the caller's catalog.
func (s *Server) ListProducts(
	ctx context.Context,
	request *billingpb.ListProductsRequest,
) (*billingpb.ListProductsResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ListProducts_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a product page")

		return nil, err
	}

	page, err := s.store.ListProducts(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing products")

		return nil, err
	}

	return &billingpb.ListProductsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ProductsToProto(page.Data),
	}, nil
}

// UpdateProduct revises a product, repricing included.
//
// What it will not do is revive an archived one, and what repricing changes is
// what the next sale costs and nothing about what anybody already paid — the
// amount on a purchase and on a ledger row is that sale's own.
//
// The write and the read-back run in one transaction, so the response is the row
// as stored rather than the request echoed with a timestamp guessed at. A second
// caller revising the same product between them would otherwise be the version
// this caller is told they wrote.
func (s *Server) UpdateProduct(
	ctx context.Context,
	request *billingpb.UpdateProductRequest,
) (*billingpb.UpdateProductResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_UpdateProduct_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetProductId()
	req.op.Set(productKey, id)

	product := productFromUpdateInput(id, request.GetInput())
	if product == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "updating product %q", id)

		return nil, err
	}

	var updated *billing.Product

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if writeErr := s.store.UpdateProduct(ctx, tx, req.scope, product); writeErr != nil {
			return writeErr
		}

		var readErr error
		updated, readErr = s.store.GetProduct(ctx, tx, req.scope, id)

		return readErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "updating product %q", id)

		return nil, err
	}

	return &billingpb.UpdateProductResponse{Result: ProductToProto(updated)}, nil
}

// ArchiveProduct withdraws a product from sale.
//
// The subscriptions already on it keep renewing and the purchases already made
// stay readable: archiving takes something off the shelf, it does not cancel
// what has been sold. A deployment that means to end the agreements ends them
// through the payment provider, and the statuses arrive here as events.
func (s *Server) ArchiveProduct(
	ctx context.Context,
	request *billingpb.ArchiveProductRequest,
) (*billingpb.ArchiveProductResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ArchiveProduct_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetProductId()
	req.op.Set(productKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.store.ArchiveProduct(ctx, tx, req.scope, id)
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving product %q", id)

		return nil, err
	}

	return &billingpb.ArchiveProductResponse{}, nil
}
