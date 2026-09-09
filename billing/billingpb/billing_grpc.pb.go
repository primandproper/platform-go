// Package primandproper.platform.billing.v1 is the wire schema for what a
// deployment sells and what its customers paid: the catalog, the recurring
// agreements, the one-time sales, and the ledger of attempts.
//
// This file is shipped inside the published Go module, and it is the file
// itself that is shipped -- not a copy for you to keep in sync. A consumer puts
// the module's proto directories on protoc's path and imports this file by its
// canonical name, exactly as identity.proto and filtering.proto already work:
//
//	PLATFORM_PROTO := $(shell go list -m -f '{{.Dir}}' github.com/primandproper/platform-go/v14)
//
//	protoc --proto_path proto/ \
//	    --proto_path $(PLATFORM_PROTO)/billing/proto \
//	    --proto_path $(PLATFORM_PROTO)/filtering/proto \
//	    --go_opt=Mprimandproper/platform/billing/v1/billing.proto=github.com/primandproper/platform-go/v14/billing/billingpb \
//	    $(CONSUMER_PROTO_FILES)   # the platform files deliberately absent from that list
//
// Field numbers are the compatibility promise, across every language a consumer
// generates into. Numbers are never reused and never repurposed: a field that
// goes away is reserved.
//
// # The one thing this schema will not carry
//
// There is no is_active, entitled, in_good_standing or account standing of any
// kind, on any message here, and there never will be. billing stores facts a
// payment provider owns and interprets none of them; a computed standing is the
// interpretation, it differs between two deployments selling the same thing, and
// putting it on the wire would put every consumer's policy on this module's
// release cadence.
//
// What ships instead is [Subscription.status], which is the fact. A consumer
// reads it and decides. The seam that decision belongs in already exists --
// billing/plans fills entitlements' PlanSource with this store's
// current-subscription read plus a function the consumer writes -- so a
// GetAccountStanding RPC here would be a second home for a policy that already
// has one.
//
// # Why status is a string and the other two are enums
//
// [Product.kind] and [Transaction.status] are enums. Both are closed sets
// billing defines itself and validates every write against, so a value outside
// the enum is a row the store would have refused; the generated constant is
// exactly as complete as the column.
//
// [Subscription.status] is a string, and it is the one borrowed vocabulary in
// this file. The set is capitalism.SubscriptionStatus -- the closed, documented
// set every payment adapter maps its provider's words onto -- and it is defined
// in a different module. An enum here would put that vocabulary on this module's
// release cadence, which is the objection identity/grpc's pattern section raises
// against a generated enum for a catalog somebody else owns; worse, a status
// primitives-go added and billing stored would render as UNSPECIFIED until this
// file caught up, which is a real row reaching a client as "we don't know".
// The wire value is the documented string -- "active", "past_due",
// "incomplete_expired" -- and it is never empty on a stored row, because the
// empty string is capitalism's unknown and billing refuses to store it.
//
// # What is not here, and why
//
// No scope field, anywhere. A scope a client could name is a cross-tenant read
// hiding behind a request field; it comes off the principal the consumer's
// interceptor put on the context. See identity.proto, which says this at
// greater length.
//
// No write that a payment processor's callback makes. Seven of billing's thirty
// store methods are absent from this service: CreateSubscription,
// UpdateSubscription, SetSubscriptionStatus, CreatePurchase, CompletePurchase,
// RecordTransaction and SetTransactionStatus. Their caller is not a client. It
// is a Stripe or RevenueCat receiver the consumer owns, or the checkout handler
// that created the payment intent, and both are already inside a transaction
// that is writing an audit entry and an outbox event beside the billing row. An
// RPC moves that write out of the transaction that was the entire point of it.
// billing/grpc/doc.go carries the ruling and billing.Store documents it on each
// method.
//
// No lookup by a payment provider's identifier. billing.Store has four --
// GetProductByExternalID and its three siblings -- and none is an RPC. The
// caller holding a provider's identifier is the callback that was handed one,
// and it already holds the store; a client that could ask "whose subscription is
// sub_1234" would be reading the provider's namespace rather than its own rows,
// against an id space it did not mint. A response about a row you may already
// read still carries the provider's id for it, which is the opposite direction
// and is how a console links out to the processor.
//
// No amount on any write. The only thing that legitimately changes about a
// ledger row is its status and the only thing that changes about a purchase is
// whether the money arrived, so there is no statement able to assign either --
// see billing's own documentation on why a price is a fact about a moment.

// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.5.1
// - protoc             v6.33.1
// source: primandproper/platform/billing/v1/billing.proto

package billingpb

import (
	context "context"

	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.64.0 or later.
const _ = grpc.SupportPackageIsVersion9

const (
	BillingService_CreateProduct_FullMethodName               = "/primandproper.platform.billing.v1.BillingService/CreateProduct"
	BillingService_GetProduct_FullMethodName                  = "/primandproper.platform.billing.v1.BillingService/GetProduct"
	BillingService_ListProducts_FullMethodName                = "/primandproper.platform.billing.v1.BillingService/ListProducts"
	BillingService_UpdateProduct_FullMethodName               = "/primandproper.platform.billing.v1.BillingService/UpdateProduct"
	BillingService_ArchiveProduct_FullMethodName              = "/primandproper.platform.billing.v1.BillingService/ArchiveProduct"
	BillingService_GetSubscription_FullMethodName             = "/primandproper.platform.billing.v1.BillingService/GetSubscription"
	BillingService_ListSubscriptions_FullMethodName           = "/primandproper.platform.billing.v1.BillingService/ListSubscriptions"
	BillingService_ListSubscriptionsForAccount_FullMethodName = "/primandproper.platform.billing.v1.BillingService/ListSubscriptionsForAccount"
	BillingService_ListCurrentSubscriptions_FullMethodName    = "/primandproper.platform.billing.v1.BillingService/ListCurrentSubscriptions"
	BillingService_ArchiveSubscription_FullMethodName         = "/primandproper.platform.billing.v1.BillingService/ArchiveSubscription"
	BillingService_GetPurchase_FullMethodName                 = "/primandproper.platform.billing.v1.BillingService/GetPurchase"
	BillingService_ListPurchases_FullMethodName               = "/primandproper.platform.billing.v1.BillingService/ListPurchases"
	BillingService_ListPurchasesForAccount_FullMethodName     = "/primandproper.platform.billing.v1.BillingService/ListPurchasesForAccount"
	BillingService_ArchivePurchase_FullMethodName             = "/primandproper.platform.billing.v1.BillingService/ArchivePurchase"
	BillingService_GetTransaction_FullMethodName              = "/primandproper.platform.billing.v1.BillingService/GetTransaction"
	BillingService_ListTransactions_FullMethodName            = "/primandproper.platform.billing.v1.BillingService/ListTransactions"
	BillingService_ListTransactionsForAccount_FullMethodName  = "/primandproper.platform.billing.v1.BillingService/ListTransactionsForAccount"
	BillingService_ArchiveTransaction_FullMethodName          = "/primandproper.platform.billing.v1.BillingService/ArchiveTransaction"
)

// BillingServiceClient is the client API for BillingService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
//
// BillingService serves the record of what a deployment sells and what its
// customers paid.
//
// Eighteen RPCs over thirty store methods, and the shape of the subset is the
// decision worth reading before the list. Twelve of the eighteen are reads,
// because the writes on this table have a caller who is not a client: seven of
// them are made by a processor callback or a checkout handler already inside the
// consumer's own transaction, and they are named in this file's opening comment
// along with why an RPC would break them. What is left on the write side is
// administrative -- stocking and revising the catalog, and withdrawing a row
// from each of the four tables.
//
// Reads come in three kinds and each is a different grant. The catalog is
// scope-wide and belongs to everybody in the scope. The account-keyed reads are
// somebody's own, and they name an account the caller has to be permitted
// against -- see billing/grpc's AccountAuthorizer, which is the half of
// authorization a grant on the method cannot reach. The three unqualified list
// reads are an operator's: they page every row in the scope and carry a
// permission of their own, so that a consumer can hand out "read my invoices"
// without handing out the customer ledger.
type BillingServiceClient interface {
	// The catalog: two reads any member of the scope may make, and three
	// administrative writes.
	CreateProduct(ctx context.Context, in *CreateProductRequest, opts ...grpc.CallOption) (*CreateProductResponse, error)
	GetProduct(ctx context.Context, in *GetProductRequest, opts ...grpc.CallOption) (*GetProductResponse, error)
	ListProducts(ctx context.Context, in *ListProductsRequest, opts ...grpc.CallOption) (*ListProductsResponse, error)
	UpdateProduct(ctx context.Context, in *UpdateProductRequest, opts ...grpc.CallOption) (*UpdateProductResponse, error)
	ArchiveProduct(ctx context.Context, in *ArchiveProductRequest, opts ...grpc.CallOption) (*ArchiveProductResponse, error)
	// The recurring half. ListCurrentSubscriptions is the one an entitlement
	// screen reads: it pages the agreements whose paid period covers now, and
	// deliberately does not filter on status, because which status leaves an
	// account entitled is the consumer's ruling.
	GetSubscription(ctx context.Context, in *GetSubscriptionRequest, opts ...grpc.CallOption) (*GetSubscriptionResponse, error)
	ListSubscriptions(ctx context.Context, in *ListSubscriptionsRequest, opts ...grpc.CallOption) (*ListSubscriptionsResponse, error)
	ListSubscriptionsForAccount(ctx context.Context, in *ListSubscriptionsForAccountRequest, opts ...grpc.CallOption) (*ListSubscriptionsForAccountResponse, error)
	ListCurrentSubscriptions(ctx context.Context, in *ListCurrentSubscriptionsRequest, opts ...grpc.CallOption) (*ListCurrentSubscriptionsResponse, error)
	ArchiveSubscription(ctx context.Context, in *ArchiveSubscriptionRequest, opts ...grpc.CallOption) (*ArchiveSubscriptionResponse, error)
	// The one-time half.
	GetPurchase(ctx context.Context, in *GetPurchaseRequest, opts ...grpc.CallOption) (*GetPurchaseResponse, error)
	ListPurchases(ctx context.Context, in *ListPurchasesRequest, opts ...grpc.CallOption) (*ListPurchasesResponse, error)
	ListPurchasesForAccount(ctx context.Context, in *ListPurchasesForAccountRequest, opts ...grpc.CallOption) (*ListPurchasesForAccountResponse, error)
	ArchivePurchase(ctx context.Context, in *ArchivePurchaseRequest, opts ...grpc.CallOption) (*ArchivePurchaseResponse, error)
	// The ledger.
	GetTransaction(ctx context.Context, in *GetTransactionRequest, opts ...grpc.CallOption) (*GetTransactionResponse, error)
	ListTransactions(ctx context.Context, in *ListTransactionsRequest, opts ...grpc.CallOption) (*ListTransactionsResponse, error)
	ListTransactionsForAccount(ctx context.Context, in *ListTransactionsForAccountRequest, opts ...grpc.CallOption) (*ListTransactionsForAccountResponse, error)
	ArchiveTransaction(ctx context.Context, in *ArchiveTransactionRequest, opts ...grpc.CallOption) (*ArchiveTransactionResponse, error)
}

type billingServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewBillingServiceClient(cc grpc.ClientConnInterface) BillingServiceClient {
	return &billingServiceClient{cc}
}

func (c *billingServiceClient) CreateProduct(ctx context.Context, in *CreateProductRequest, opts ...grpc.CallOption) (*CreateProductResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(CreateProductResponse)
	err := c.cc.Invoke(ctx, BillingService_CreateProduct_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) GetProduct(ctx context.Context, in *GetProductRequest, opts ...grpc.CallOption) (*GetProductResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetProductResponse)
	err := c.cc.Invoke(ctx, BillingService_GetProduct_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ListProducts(ctx context.Context, in *ListProductsRequest, opts ...grpc.CallOption) (*ListProductsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListProductsResponse)
	err := c.cc.Invoke(ctx, BillingService_ListProducts_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) UpdateProduct(ctx context.Context, in *UpdateProductRequest, opts ...grpc.CallOption) (*UpdateProductResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(UpdateProductResponse)
	err := c.cc.Invoke(ctx, BillingService_UpdateProduct_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ArchiveProduct(ctx context.Context, in *ArchiveProductRequest, opts ...grpc.CallOption) (*ArchiveProductResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ArchiveProductResponse)
	err := c.cc.Invoke(ctx, BillingService_ArchiveProduct_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) GetSubscription(ctx context.Context, in *GetSubscriptionRequest, opts ...grpc.CallOption) (*GetSubscriptionResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetSubscriptionResponse)
	err := c.cc.Invoke(ctx, BillingService_GetSubscription_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ListSubscriptions(ctx context.Context, in *ListSubscriptionsRequest, opts ...grpc.CallOption) (*ListSubscriptionsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListSubscriptionsResponse)
	err := c.cc.Invoke(ctx, BillingService_ListSubscriptions_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ListSubscriptionsForAccount(ctx context.Context, in *ListSubscriptionsForAccountRequest, opts ...grpc.CallOption) (*ListSubscriptionsForAccountResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListSubscriptionsForAccountResponse)
	err := c.cc.Invoke(ctx, BillingService_ListSubscriptionsForAccount_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ListCurrentSubscriptions(ctx context.Context, in *ListCurrentSubscriptionsRequest, opts ...grpc.CallOption) (*ListCurrentSubscriptionsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListCurrentSubscriptionsResponse)
	err := c.cc.Invoke(ctx, BillingService_ListCurrentSubscriptions_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ArchiveSubscription(ctx context.Context, in *ArchiveSubscriptionRequest, opts ...grpc.CallOption) (*ArchiveSubscriptionResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ArchiveSubscriptionResponse)
	err := c.cc.Invoke(ctx, BillingService_ArchiveSubscription_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) GetPurchase(ctx context.Context, in *GetPurchaseRequest, opts ...grpc.CallOption) (*GetPurchaseResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetPurchaseResponse)
	err := c.cc.Invoke(ctx, BillingService_GetPurchase_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ListPurchases(ctx context.Context, in *ListPurchasesRequest, opts ...grpc.CallOption) (*ListPurchasesResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListPurchasesResponse)
	err := c.cc.Invoke(ctx, BillingService_ListPurchases_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ListPurchasesForAccount(ctx context.Context, in *ListPurchasesForAccountRequest, opts ...grpc.CallOption) (*ListPurchasesForAccountResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListPurchasesForAccountResponse)
	err := c.cc.Invoke(ctx, BillingService_ListPurchasesForAccount_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ArchivePurchase(ctx context.Context, in *ArchivePurchaseRequest, opts ...grpc.CallOption) (*ArchivePurchaseResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ArchivePurchaseResponse)
	err := c.cc.Invoke(ctx, BillingService_ArchivePurchase_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) GetTransaction(ctx context.Context, in *GetTransactionRequest, opts ...grpc.CallOption) (*GetTransactionResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetTransactionResponse)
	err := c.cc.Invoke(ctx, BillingService_GetTransaction_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ListTransactions(ctx context.Context, in *ListTransactionsRequest, opts ...grpc.CallOption) (*ListTransactionsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListTransactionsResponse)
	err := c.cc.Invoke(ctx, BillingService_ListTransactions_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ListTransactionsForAccount(ctx context.Context, in *ListTransactionsForAccountRequest, opts ...grpc.CallOption) (*ListTransactionsForAccountResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListTransactionsForAccountResponse)
	err := c.cc.Invoke(ctx, BillingService_ListTransactionsForAccount_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *billingServiceClient) ArchiveTransaction(ctx context.Context, in *ArchiveTransactionRequest, opts ...grpc.CallOption) (*ArchiveTransactionResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ArchiveTransactionResponse)
	err := c.cc.Invoke(ctx, BillingService_ArchiveTransaction_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// BillingServiceServer is the server API for BillingService service.
// All implementations must embed UnimplementedBillingServiceServer
// for forward compatibility.
//
// BillingService serves the record of what a deployment sells and what its
// customers paid.
//
// Eighteen RPCs over thirty store methods, and the shape of the subset is the
// decision worth reading before the list. Twelve of the eighteen are reads,
// because the writes on this table have a caller who is not a client: seven of
// them are made by a processor callback or a checkout handler already inside the
// consumer's own transaction, and they are named in this file's opening comment
// along with why an RPC would break them. What is left on the write side is
// administrative -- stocking and revising the catalog, and withdrawing a row
// from each of the four tables.
//
// Reads come in three kinds and each is a different grant. The catalog is
// scope-wide and belongs to everybody in the scope. The account-keyed reads are
// somebody's own, and they name an account the caller has to be permitted
// against -- see billing/grpc's AccountAuthorizer, which is the half of
// authorization a grant on the method cannot reach. The three unqualified list
// reads are an operator's: they page every row in the scope and carry a
// permission of their own, so that a consumer can hand out "read my invoices"
// without handing out the customer ledger.
type BillingServiceServer interface {
	// The catalog: two reads any member of the scope may make, and three
	// administrative writes.
	CreateProduct(context.Context, *CreateProductRequest) (*CreateProductResponse, error)
	GetProduct(context.Context, *GetProductRequest) (*GetProductResponse, error)
	ListProducts(context.Context, *ListProductsRequest) (*ListProductsResponse, error)
	UpdateProduct(context.Context, *UpdateProductRequest) (*UpdateProductResponse, error)
	ArchiveProduct(context.Context, *ArchiveProductRequest) (*ArchiveProductResponse, error)
	// The recurring half. ListCurrentSubscriptions is the one an entitlement
	// screen reads: it pages the agreements whose paid period covers now, and
	// deliberately does not filter on status, because which status leaves an
	// account entitled is the consumer's ruling.
	GetSubscription(context.Context, *GetSubscriptionRequest) (*GetSubscriptionResponse, error)
	ListSubscriptions(context.Context, *ListSubscriptionsRequest) (*ListSubscriptionsResponse, error)
	ListSubscriptionsForAccount(context.Context, *ListSubscriptionsForAccountRequest) (*ListSubscriptionsForAccountResponse, error)
	ListCurrentSubscriptions(context.Context, *ListCurrentSubscriptionsRequest) (*ListCurrentSubscriptionsResponse, error)
	ArchiveSubscription(context.Context, *ArchiveSubscriptionRequest) (*ArchiveSubscriptionResponse, error)
	// The one-time half.
	GetPurchase(context.Context, *GetPurchaseRequest) (*GetPurchaseResponse, error)
	ListPurchases(context.Context, *ListPurchasesRequest) (*ListPurchasesResponse, error)
	ListPurchasesForAccount(context.Context, *ListPurchasesForAccountRequest) (*ListPurchasesForAccountResponse, error)
	ArchivePurchase(context.Context, *ArchivePurchaseRequest) (*ArchivePurchaseResponse, error)
	// The ledger.
	GetTransaction(context.Context, *GetTransactionRequest) (*GetTransactionResponse, error)
	ListTransactions(context.Context, *ListTransactionsRequest) (*ListTransactionsResponse, error)
	ListTransactionsForAccount(context.Context, *ListTransactionsForAccountRequest) (*ListTransactionsForAccountResponse, error)
	ArchiveTransaction(context.Context, *ArchiveTransactionRequest) (*ArchiveTransactionResponse, error)
	mustEmbedUnimplementedBillingServiceServer()
}

// UnimplementedBillingServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedBillingServiceServer struct{}

func (UnimplementedBillingServiceServer) CreateProduct(context.Context, *CreateProductRequest) (*CreateProductResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateProduct not implemented")
}
func (UnimplementedBillingServiceServer) GetProduct(context.Context, *GetProductRequest) (*GetProductResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetProduct not implemented")
}
func (UnimplementedBillingServiceServer) ListProducts(context.Context, *ListProductsRequest) (*ListProductsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListProducts not implemented")
}
func (UnimplementedBillingServiceServer) UpdateProduct(context.Context, *UpdateProductRequest) (*UpdateProductResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateProduct not implemented")
}
func (UnimplementedBillingServiceServer) ArchiveProduct(context.Context, *ArchiveProductRequest) (*ArchiveProductResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ArchiveProduct not implemented")
}
func (UnimplementedBillingServiceServer) GetSubscription(context.Context, *GetSubscriptionRequest) (*GetSubscriptionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetSubscription not implemented")
}
func (UnimplementedBillingServiceServer) ListSubscriptions(context.Context, *ListSubscriptionsRequest) (*ListSubscriptionsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListSubscriptions not implemented")
}
func (UnimplementedBillingServiceServer) ListSubscriptionsForAccount(context.Context, *ListSubscriptionsForAccountRequest) (*ListSubscriptionsForAccountResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListSubscriptionsForAccount not implemented")
}
func (UnimplementedBillingServiceServer) ListCurrentSubscriptions(context.Context, *ListCurrentSubscriptionsRequest) (*ListCurrentSubscriptionsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListCurrentSubscriptions not implemented")
}
func (UnimplementedBillingServiceServer) ArchiveSubscription(context.Context, *ArchiveSubscriptionRequest) (*ArchiveSubscriptionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ArchiveSubscription not implemented")
}
func (UnimplementedBillingServiceServer) GetPurchase(context.Context, *GetPurchaseRequest) (*GetPurchaseResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetPurchase not implemented")
}
func (UnimplementedBillingServiceServer) ListPurchases(context.Context, *ListPurchasesRequest) (*ListPurchasesResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListPurchases not implemented")
}
func (UnimplementedBillingServiceServer) ListPurchasesForAccount(context.Context, *ListPurchasesForAccountRequest) (*ListPurchasesForAccountResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListPurchasesForAccount not implemented")
}
func (UnimplementedBillingServiceServer) ArchivePurchase(context.Context, *ArchivePurchaseRequest) (*ArchivePurchaseResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ArchivePurchase not implemented")
}
func (UnimplementedBillingServiceServer) GetTransaction(context.Context, *GetTransactionRequest) (*GetTransactionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetTransaction not implemented")
}
func (UnimplementedBillingServiceServer) ListTransactions(context.Context, *ListTransactionsRequest) (*ListTransactionsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListTransactions not implemented")
}
func (UnimplementedBillingServiceServer) ListTransactionsForAccount(context.Context, *ListTransactionsForAccountRequest) (*ListTransactionsForAccountResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListTransactionsForAccount not implemented")
}
func (UnimplementedBillingServiceServer) ArchiveTransaction(context.Context, *ArchiveTransactionRequest) (*ArchiveTransactionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ArchiveTransaction not implemented")
}
func (UnimplementedBillingServiceServer) mustEmbedUnimplementedBillingServiceServer() {}
func (UnimplementedBillingServiceServer) testEmbeddedByValue()                        {}

// UnsafeBillingServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to BillingServiceServer will
// result in compilation errors.
type UnsafeBillingServiceServer interface {
	mustEmbedUnimplementedBillingServiceServer()
}

func RegisterBillingServiceServer(s grpc.ServiceRegistrar, srv BillingServiceServer) {
	// If the following call pancis, it indicates UnimplementedBillingServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&BillingService_ServiceDesc, srv)
}

func _BillingService_CreateProduct_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateProductRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).CreateProduct(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_CreateProduct_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).CreateProduct(ctx, req.(*CreateProductRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_GetProduct_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetProductRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).GetProduct(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_GetProduct_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).GetProduct(ctx, req.(*GetProductRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ListProducts_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListProductsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ListProducts(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ListProducts_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ListProducts(ctx, req.(*ListProductsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_UpdateProduct_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(UpdateProductRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).UpdateProduct(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_UpdateProduct_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).UpdateProduct(ctx, req.(*UpdateProductRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ArchiveProduct_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ArchiveProductRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ArchiveProduct(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ArchiveProduct_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ArchiveProduct(ctx, req.(*ArchiveProductRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_GetSubscription_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetSubscriptionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).GetSubscription(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_GetSubscription_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).GetSubscription(ctx, req.(*GetSubscriptionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ListSubscriptions_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListSubscriptionsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ListSubscriptions(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ListSubscriptions_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ListSubscriptions(ctx, req.(*ListSubscriptionsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ListSubscriptionsForAccount_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListSubscriptionsForAccountRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ListSubscriptionsForAccount(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ListSubscriptionsForAccount_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ListSubscriptionsForAccount(ctx, req.(*ListSubscriptionsForAccountRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ListCurrentSubscriptions_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListCurrentSubscriptionsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ListCurrentSubscriptions(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ListCurrentSubscriptions_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ListCurrentSubscriptions(ctx, req.(*ListCurrentSubscriptionsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ArchiveSubscription_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ArchiveSubscriptionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ArchiveSubscription(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ArchiveSubscription_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ArchiveSubscription(ctx, req.(*ArchiveSubscriptionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_GetPurchase_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetPurchaseRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).GetPurchase(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_GetPurchase_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).GetPurchase(ctx, req.(*GetPurchaseRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ListPurchases_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListPurchasesRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ListPurchases(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ListPurchases_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ListPurchases(ctx, req.(*ListPurchasesRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ListPurchasesForAccount_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListPurchasesForAccountRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ListPurchasesForAccount(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ListPurchasesForAccount_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ListPurchasesForAccount(ctx, req.(*ListPurchasesForAccountRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ArchivePurchase_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ArchivePurchaseRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ArchivePurchase(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ArchivePurchase_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ArchivePurchase(ctx, req.(*ArchivePurchaseRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_GetTransaction_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetTransactionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).GetTransaction(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_GetTransaction_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).GetTransaction(ctx, req.(*GetTransactionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ListTransactions_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListTransactionsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ListTransactions(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ListTransactions_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ListTransactions(ctx, req.(*ListTransactionsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ListTransactionsForAccount_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListTransactionsForAccountRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ListTransactionsForAccount(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ListTransactionsForAccount_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ListTransactionsForAccount(ctx, req.(*ListTransactionsForAccountRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BillingService_ArchiveTransaction_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ArchiveTransactionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BillingServiceServer).ArchiveTransaction(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: BillingService_ArchiveTransaction_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BillingServiceServer).ArchiveTransaction(ctx, req.(*ArchiveTransactionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// BillingService_ServiceDesc is the grpc.ServiceDesc for BillingService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var BillingService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "primandproper.platform.billing.v1.BillingService",
	HandlerType: (*BillingServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "CreateProduct",
			Handler:    _BillingService_CreateProduct_Handler,
		},
		{
			MethodName: "GetProduct",
			Handler:    _BillingService_GetProduct_Handler,
		},
		{
			MethodName: "ListProducts",
			Handler:    _BillingService_ListProducts_Handler,
		},
		{
			MethodName: "UpdateProduct",
			Handler:    _BillingService_UpdateProduct_Handler,
		},
		{
			MethodName: "ArchiveProduct",
			Handler:    _BillingService_ArchiveProduct_Handler,
		},
		{
			MethodName: "GetSubscription",
			Handler:    _BillingService_GetSubscription_Handler,
		},
		{
			MethodName: "ListSubscriptions",
			Handler:    _BillingService_ListSubscriptions_Handler,
		},
		{
			MethodName: "ListSubscriptionsForAccount",
			Handler:    _BillingService_ListSubscriptionsForAccount_Handler,
		},
		{
			MethodName: "ListCurrentSubscriptions",
			Handler:    _BillingService_ListCurrentSubscriptions_Handler,
		},
		{
			MethodName: "ArchiveSubscription",
			Handler:    _BillingService_ArchiveSubscription_Handler,
		},
		{
			MethodName: "GetPurchase",
			Handler:    _BillingService_GetPurchase_Handler,
		},
		{
			MethodName: "ListPurchases",
			Handler:    _BillingService_ListPurchases_Handler,
		},
		{
			MethodName: "ListPurchasesForAccount",
			Handler:    _BillingService_ListPurchasesForAccount_Handler,
		},
		{
			MethodName: "ArchivePurchase",
			Handler:    _BillingService_ArchivePurchase_Handler,
		},
		{
			MethodName: "GetTransaction",
			Handler:    _BillingService_GetTransaction_Handler,
		},
		{
			MethodName: "ListTransactions",
			Handler:    _BillingService_ListTransactions_Handler,
		},
		{
			MethodName: "ListTransactionsForAccount",
			Handler:    _BillingService_ListTransactionsForAccount_Handler,
		},
		{
			MethodName: "ArchiveTransaction",
			Handler:    _BillingService_ArchiveTransaction_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "primandproper/platform/billing/v1/billing.proto",
}
