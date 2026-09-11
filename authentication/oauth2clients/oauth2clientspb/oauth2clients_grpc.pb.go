// Package primandproper.platform.oauth2clients.v1 is the wire schema for an
// administered registry of OAuth2 clients: the registrations an operator
// creates on purpose, listed, read and withdrawn.
//
// It is not RFC 7591 dynamic client registration. That is an anonymous POST to
// /authorize's sibling endpoint, it is served by
// github.com/primandproper/primitives-go/v2/authentication/oauth2server, and a
// deployment using this registry turns it off. What is here is the API a
// console and a CLI drive.
//
// This file is shipped inside the published Go module, and it is the file
// itself that is shipped -- not a copy for you to keep in sync. A consumer puts
// the module's proto directories on protoc's path and imports this file by its
// canonical name, exactly as identity.proto and filtering.proto already work:
//
//	PLATFORM_PROTO := $(shell go list -m -f '{{.Dir}}' github.com/primandproper/platform-go/v14)
//
//	protoc --proto_path proto/ \
//	    --proto_path $(PLATFORM_PROTO)/authentication/oauth2clients/proto \
//	    --proto_path $(PLATFORM_PROTO)/filtering/proto \
//	    --go_opt=Mprimandproper/platform/oauth2clients/v1/oauth2clients.proto=github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb \
//	    $(CONSUMER_PROTO_FILES)   # the platform files deliberately absent from that list
//
// Field numbers are the compatibility promise, across every language a consumer
// generates into. Numbers are never reused and never repurposed: a field that
// goes away is reserved.
//
// # Why there are four RPCs
//
// Because four is what a consumer asked for. This service was drawn from
// dinnerdonebetter's proto/oauth, which declares create, get, list and archive
// and nothing else; the file was diffed against it rather than remembered, and
// six further methods that had shipped here -- an update, and a self-service
// mirror of all five operations -- turned out to answer no caller in that
// repository or any other. They were removed before anything consumed this
// package.
//
// The self-service half is the one worth recording, because it was not merely
// unused. Its five methods were reachable behind no permission at all, on the
// theory that owning the row is the authorization, which made them the surface
// in this package with the most ways to be wrong and the fewest readers checking.
// Surface nobody asked for is not free, and permissionless surface nobody asked
// for is the expensive kind.
//
// # If a self-service half is ever wanted
//
// The Go API still models the arrangement -- a registration carries an owner,
// and oauth2clients.Store lists by one -- so what is missing is a transport, and
// there is a right and a wrong shape for it. The right one is mirrored methods:
// CreateOAuth2Client and a separate CreateOwnOAuth2Client, so the decision is on
// the method name.
//
// A consumer's interceptor gates an RPC by its full method name, and it runs
// before the request body is parsed -- see
// github.com/primandproper/primitives-go/v2/authorization/grpc. A single
// CreateOAuth2Client whose required permission depended on an "ownership" field
// would be one the enforcer could not gate: it would have to be declared public
// and gate itself, which is the arrangement that puts an authorization decision
// somewhere nobody auditing the permission map can see it. The other shape, one
// set of RPCs that silently narrows to the caller's own rows when they hold no
// grant, is the antipattern oauth2server names about scopes: silently narrowing
// hands back an answer that looks like the one that was asked for and is not.
//
// # What is not here, and why
//
// No scope field, anywhere, and the name is reserved so there cannot be one. A
// scope a client could name is a cross-tenant read hiding behind a request
// field; it comes off the principal the consumer's interceptor put on the
// context. See identity.proto, which says this at greater length.
//
// Reserving the name rather than only saying so is audit.proto's pattern:
// `reserved "scope";` is a schema protoc refuses to accept a scope field into,
// in this repository and in a consumer's fork of the file alike, whereas a
// comment is a request to the next author. It is reserved on all four request
// messages, on [OAuth2ClientCreationInput], which one of them is built from,
// and on [OAuth2Client] and [IssuedOAuth2Client], which the responses are built
// from.
//
// The reservation is of the singular name only, and this is the one file on the
// lane where that has to be said out loud: OAuth2Client.scopes and
// OAuth2ClientCreationInput.scopes are OAuth2 authorization scopes, which are
// what a client may ask for at /authorize and have nothing to do with a tenant.
// Two different words that happen to be spelled the same; protoc reserves
// "scope" and leaves "scopes" alone, which is the outcome wanted here.
//
// No belongs_to_user in any request, for the same reason one level in. An owner
// a client could name is a credential minted in somebody else's name. It is
// output-only: a response carries it so a console can show who owns a row.
//
// No client_secret on OAuth2Client. The plaintext exists on exactly one message
// -- [IssuedOAuth2Client], returned by the creation RPC -- because a field that
// is populated once and empty on every other read is the field that ends up in a
// log. It is not recoverable: what the row holds is a digest, and losing a
// secret means archiving the registration and minting another.
//
// No update of any kind. A registration's descriptive fields are revisable
// through the Go API and no consumer has asked to revise them over the wire; the
// owner, the client_id and the secret are not revisable at all, the first two
// because they are immutable facts about a row and the third because rotating it
// is a call that hands back a new credential rather than an UPDATE nobody sees.

// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.5.1
// - protoc             v6.33.1
// source: primandproper/platform/oauth2clients/v1/oauth2clients.proto

package oauth2clientspb

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
	OAuth2ClientsService_CreateOAuth2Client_FullMethodName  = "/primandproper.platform.oauth2clients.v1.OAuth2ClientsService/CreateOAuth2Client"
	OAuth2ClientsService_GetOAuth2Client_FullMethodName     = "/primandproper.platform.oauth2clients.v1.OAuth2ClientsService/GetOAuth2Client"
	OAuth2ClientsService_ListOAuth2Clients_FullMethodName   = "/primandproper.platform.oauth2clients.v1.OAuth2ClientsService/ListOAuth2Clients"
	OAuth2ClientsService_ArchiveOAuth2Client_FullMethodName = "/primandproper.platform.oauth2clients.v1.OAuth2ClientsService/ArchiveOAuth2Client"
)

// OAuth2ClientsServiceClient is the client API for OAuth2ClientsService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
//
// OAuth2ClientsService administers the registry.
//
// Every method acts on any registration in the caller's registry and every one
// of them requires a grant. The registry comes off the caller's principal; the
// registration belongs to no person. See the file comment for why there are four
// of them and what a self-service half would have to look like.
type OAuth2ClientsServiceClient interface {
	CreateOAuth2Client(ctx context.Context, in *CreateOAuth2ClientRequest, opts ...grpc.CallOption) (*CreateOAuth2ClientResponse, error)
	GetOAuth2Client(ctx context.Context, in *GetOAuth2ClientRequest, opts ...grpc.CallOption) (*GetOAuth2ClientResponse, error)
	ListOAuth2Clients(ctx context.Context, in *ListOAuth2ClientsRequest, opts ...grpc.CallOption) (*ListOAuth2ClientsResponse, error)
	ArchiveOAuth2Client(ctx context.Context, in *ArchiveOAuth2ClientRequest, opts ...grpc.CallOption) (*ArchiveOAuth2ClientResponse, error)
}

type oAuth2ClientsServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewOAuth2ClientsServiceClient(cc grpc.ClientConnInterface) OAuth2ClientsServiceClient {
	return &oAuth2ClientsServiceClient{cc}
}

func (c *oAuth2ClientsServiceClient) CreateOAuth2Client(ctx context.Context, in *CreateOAuth2ClientRequest, opts ...grpc.CallOption) (*CreateOAuth2ClientResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(CreateOAuth2ClientResponse)
	err := c.cc.Invoke(ctx, OAuth2ClientsService_CreateOAuth2Client_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *oAuth2ClientsServiceClient) GetOAuth2Client(ctx context.Context, in *GetOAuth2ClientRequest, opts ...grpc.CallOption) (*GetOAuth2ClientResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetOAuth2ClientResponse)
	err := c.cc.Invoke(ctx, OAuth2ClientsService_GetOAuth2Client_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *oAuth2ClientsServiceClient) ListOAuth2Clients(ctx context.Context, in *ListOAuth2ClientsRequest, opts ...grpc.CallOption) (*ListOAuth2ClientsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListOAuth2ClientsResponse)
	err := c.cc.Invoke(ctx, OAuth2ClientsService_ListOAuth2Clients_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *oAuth2ClientsServiceClient) ArchiveOAuth2Client(ctx context.Context, in *ArchiveOAuth2ClientRequest, opts ...grpc.CallOption) (*ArchiveOAuth2ClientResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ArchiveOAuth2ClientResponse)
	err := c.cc.Invoke(ctx, OAuth2ClientsService_ArchiveOAuth2Client_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OAuth2ClientsServiceServer is the server API for OAuth2ClientsService service.
// All implementations must embed UnimplementedOAuth2ClientsServiceServer
// for forward compatibility.
//
// OAuth2ClientsService administers the registry.
//
// Every method acts on any registration in the caller's registry and every one
// of them requires a grant. The registry comes off the caller's principal; the
// registration belongs to no person. See the file comment for why there are four
// of them and what a self-service half would have to look like.
type OAuth2ClientsServiceServer interface {
	CreateOAuth2Client(context.Context, *CreateOAuth2ClientRequest) (*CreateOAuth2ClientResponse, error)
	GetOAuth2Client(context.Context, *GetOAuth2ClientRequest) (*GetOAuth2ClientResponse, error)
	ListOAuth2Clients(context.Context, *ListOAuth2ClientsRequest) (*ListOAuth2ClientsResponse, error)
	ArchiveOAuth2Client(context.Context, *ArchiveOAuth2ClientRequest) (*ArchiveOAuth2ClientResponse, error)
	mustEmbedUnimplementedOAuth2ClientsServiceServer()
}

// UnimplementedOAuth2ClientsServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedOAuth2ClientsServiceServer struct{}

func (UnimplementedOAuth2ClientsServiceServer) CreateOAuth2Client(context.Context, *CreateOAuth2ClientRequest) (*CreateOAuth2ClientResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateOAuth2Client not implemented")
}
func (UnimplementedOAuth2ClientsServiceServer) GetOAuth2Client(context.Context, *GetOAuth2ClientRequest) (*GetOAuth2ClientResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetOAuth2Client not implemented")
}
func (UnimplementedOAuth2ClientsServiceServer) ListOAuth2Clients(context.Context, *ListOAuth2ClientsRequest) (*ListOAuth2ClientsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListOAuth2Clients not implemented")
}
func (UnimplementedOAuth2ClientsServiceServer) ArchiveOAuth2Client(context.Context, *ArchiveOAuth2ClientRequest) (*ArchiveOAuth2ClientResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ArchiveOAuth2Client not implemented")
}
func (UnimplementedOAuth2ClientsServiceServer) mustEmbedUnimplementedOAuth2ClientsServiceServer() {}
func (UnimplementedOAuth2ClientsServiceServer) testEmbeddedByValue()                              {}

// UnsafeOAuth2ClientsServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to OAuth2ClientsServiceServer will
// result in compilation errors.
type UnsafeOAuth2ClientsServiceServer interface {
	mustEmbedUnimplementedOAuth2ClientsServiceServer()
}

func RegisterOAuth2ClientsServiceServer(s grpc.ServiceRegistrar, srv OAuth2ClientsServiceServer) {
	// If the following call pancis, it indicates UnimplementedOAuth2ClientsServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&OAuth2ClientsService_ServiceDesc, srv)
}

func _OAuth2ClientsService_CreateOAuth2Client_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateOAuth2ClientRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(OAuth2ClientsServiceServer).CreateOAuth2Client(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: OAuth2ClientsService_CreateOAuth2Client_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(OAuth2ClientsServiceServer).CreateOAuth2Client(ctx, req.(*CreateOAuth2ClientRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _OAuth2ClientsService_GetOAuth2Client_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetOAuth2ClientRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(OAuth2ClientsServiceServer).GetOAuth2Client(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: OAuth2ClientsService_GetOAuth2Client_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(OAuth2ClientsServiceServer).GetOAuth2Client(ctx, req.(*GetOAuth2ClientRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _OAuth2ClientsService_ListOAuth2Clients_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListOAuth2ClientsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(OAuth2ClientsServiceServer).ListOAuth2Clients(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: OAuth2ClientsService_ListOAuth2Clients_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(OAuth2ClientsServiceServer).ListOAuth2Clients(ctx, req.(*ListOAuth2ClientsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _OAuth2ClientsService_ArchiveOAuth2Client_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ArchiveOAuth2ClientRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(OAuth2ClientsServiceServer).ArchiveOAuth2Client(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: OAuth2ClientsService_ArchiveOAuth2Client_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(OAuth2ClientsServiceServer).ArchiveOAuth2Client(ctx, req.(*ArchiveOAuth2ClientRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// OAuth2ClientsService_ServiceDesc is the grpc.ServiceDesc for OAuth2ClientsService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var OAuth2ClientsService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "primandproper.platform.oauth2clients.v1.OAuth2ClientsService",
	HandlerType: (*OAuth2ClientsServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "CreateOAuth2Client",
			Handler:    _OAuth2ClientsService_CreateOAuth2Client_Handler,
		},
		{
			MethodName: "GetOAuth2Client",
			Handler:    _OAuth2ClientsService_GetOAuth2Client_Handler,
		},
		{
			MethodName: "ListOAuth2Clients",
			Handler:    _OAuth2ClientsService_ListOAuth2Clients_Handler,
		},
		{
			MethodName: "ArchiveOAuth2Client",
			Handler:    _OAuth2ClientsService_ArchiveOAuth2Client_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "primandproper/platform/oauth2clients/v1/oauth2clients.proto",
}
