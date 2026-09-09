// Package primandproper.platform.waitlists.v1 is the wire schema for the queue
// people join before the thing they are queueing for exists: the lists an
// operator opens, and the signups against them with a lifecycle of their own.
//
// It is one service with two audiences, which is what makes it different from
// the three domain surfaces that came before it. Three RPCs are the signup page
// — the open lists, the form, and the unsubscribe — and are reachable by
// somebody who has not signed in and frequently does not have an account to
// sign in to. The other fourteen are whoever is running the launch, and every
// one of them is behind a grant. See the service comment at the bottom.
//
// This file is shipped inside the published Go module, and it is the file
// itself that is shipped -- not a copy for you to keep in sync. A consumer puts
// the module's proto directories on protoc's path and imports this file by its
// canonical name, exactly as identity.proto and filtering.proto already work:
//
//	PLATFORM_PROTO := $(shell go list -m -f '{{.Dir}}' github.com/primandproper/platform-go/v14)
//
//	protoc --proto_path proto/ \
//	    --proto_path $(PLATFORM_PROTO)/waitlists/proto \
//	    --proto_path $(PLATFORM_PROTO)/filtering/proto \
//	    --go_opt=Mprimandproper/platform/waitlists/v1/waitlists.proto=github.com/primandproper/platform-go/v14/waitlists/waitlistspb \
//	    $(CONSUMER_PROTO_FILES)   # the platform files deliberately absent from that list
//
// Field numbers are the compatibility promise, across every language a consumer
// generates into. Numbers are never reused and never repurposed: a field that
// goes away is reserved.
//
// # status is an enum, and that is not the usual answer
//
// [SignupStatus] is a generated enum, where webhooks' event type, comments'
// target type and issuereports' kind are all opaque strings. The rule those
// three are under is that a consumer's catalog stays a string, because a
// generated enum puts the application's vocabulary on this module's release
// cadence. This is the opposite case and waitlists.Status says so in its own
// documentation: the four statuses decide which transitions the store will
// make and what a withdrawal means, so a fifth is not a word an application
// adds -- it is a row nothing can move. settings.Kind is the other one of these.
//
// SubjectType is the string on this surface, and it is the one that is genuinely
// the consumer's: "user" and "account" are suggestions, and an application whose
// signups hang off a device or a workspace should say so rather than misfile it.
//
// # What is not here, and why
//
// No scope field, anywhere, and the name is reserved so there cannot be one. A
// scope a client could name is a cross-tenant read hiding behind a request
// field -- on this surface, one tenant reading another's signup list, or joining
// somebody to it. It is resolved off the caller where there is one and off the
// connection where there is not; see waitlists/grpc.
//
// Reserving the name rather than only saying so is audit.proto's pattern:
// `reserved "scope";` is a schema protoc refuses to accept a scope field into,
// in this repository and in a consumer's fork of the file alike, whereas a
// comment is a request to the next author. It is reserved on every request
// message and on the three messages a response is built from.
//
// No subject on [JoinRequest], and the name is reserved there too. A signup's
// subject is provenance -- who this row belongs to -- and provenance a caller
// could name is provenance that says whatever the caller wanted it to. It is
// filled from the principal the consumer's interceptor resolved, so a signed-in
// person's signup is theirs and an anonymous one belongs to nobody, which is the
// ordinary case for a pre-launch list. It is the same reading webhooks.proto
// takes of created_by. A deployment queueing whole organizations writes that
// signup through waitlists.SignupStore.Join in the transaction that carries the
// rest of what it knows about the organization, which is where a subject that is
// not the caller can actually be vouched for.
//
// No notes on [JoinRequest], reserved for the same reason by a shorter argument:
// Signup.notes is what whoever administers the list wrote about somebody, and
// UpdateSignupNotes is behind a grant. A form that could write it would be a
// form that writes the operator's column.
//
// No contact_digest on [Signup], and the name is reserved. The digest is what
// the row is found by and what survives a withdrawal, and it is deliberately
// unsalted over a fast hash -- so a client holding one can test any address it
// likes against it offline. waitlists.SQLStore.Digest still exports it in
// process, for the deployment migrating off a hand-written table, which is a
// caller with the addresses already in hand.
//
// No queue position. "You are 4,102nd in line" is a count that changes under
// whoever is reading it, and the paged reads here carry the counts a caller
// needs to render a position it is willing to stand behind.

// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.5.1
// - protoc             v6.33.1
// source: primandproper/platform/waitlists/v1/waitlists.proto

package waitlistspb

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
	WaitlistsService_CreateList_FullMethodName                = "/primandproper.platform.waitlists.v1.WaitlistsService/CreateList"
	WaitlistsService_GetList_FullMethodName                   = "/primandproper.platform.waitlists.v1.WaitlistsService/GetList"
	WaitlistsService_ListLists_FullMethodName                 = "/primandproper.platform.waitlists.v1.WaitlistsService/ListLists"
	WaitlistsService_ListOpenLists_FullMethodName             = "/primandproper.platform.waitlists.v1.WaitlistsService/ListOpenLists"
	WaitlistsService_UpdateList_FullMethodName                = "/primandproper.platform.waitlists.v1.WaitlistsService/UpdateList"
	WaitlistsService_ArchiveList_FullMethodName               = "/primandproper.platform.waitlists.v1.WaitlistsService/ArchiveList"
	WaitlistsService_Join_FullMethodName                      = "/primandproper.platform.waitlists.v1.WaitlistsService/Join"
	WaitlistsService_GetSignup_FullMethodName                 = "/primandproper.platform.waitlists.v1.WaitlistsService/GetSignup"
	WaitlistsService_GetSignupByContact_FullMethodName        = "/primandproper.platform.waitlists.v1.WaitlistsService/GetSignupByContact"
	WaitlistsService_ListSignups_FullMethodName               = "/primandproper.platform.waitlists.v1.WaitlistsService/ListSignups"
	WaitlistsService_ListSignupsForSubject_FullMethodName     = "/primandproper.platform.waitlists.v1.WaitlistsService/ListSignupsForSubject"
	WaitlistsService_UpdateSignupNotes_FullMethodName         = "/primandproper.platform.waitlists.v1.WaitlistsService/UpdateSignupNotes"
	WaitlistsService_Invite_FullMethodName                    = "/primandproper.platform.waitlists.v1.WaitlistsService/Invite"
	WaitlistsService_Convert_FullMethodName                   = "/primandproper.platform.waitlists.v1.WaitlistsService/Convert"
	WaitlistsService_Withdraw_FullMethodName                  = "/primandproper.platform.waitlists.v1.WaitlistsService/Withdraw"
	WaitlistsService_WithdrawSignupsForSubject_FullMethodName = "/primandproper.platform.waitlists.v1.WaitlistsService/WithdrawSignupsForSubject"
	WaitlistsService_ArchiveSignup_FullMethodName             = "/primandproper.platform.waitlists.v1.WaitlistsService/ArchiveSignup"
)

// WaitlistsServiceClient is the client API for WaitlistsService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
//
// WaitlistsService is the whole of waitlists on the wire: all seventeen methods
// of waitlists.Store, split by who calls them.
//
// There are no absences, which is unusual on this lane and is the reason this
// was the first of the ten domains to cross. Every other surface in the module
// carves something out because its realistic caller is a worker on a timer, a
// processor callback, or the consumer's own code inside its own transaction.
// Nothing here has that shape: a waitlist has no queue protocol, no fan-out and
// no provider callback, and every one of the seventeen is either a form
// somebody submitted or a console somebody is looking at.
//
// # The public three
//
// ListOpenLists, Join and Withdraw are reachable without a grant, because the
// caller is a person on a signup page who has not signed in and frequently has
// no account to sign in to. That is the whole of what "public" means here: the
// consumer's authentication interceptor still runs, and a caller who does arrive
// with a principal has their signup attributed to them.
//
// Public is not unguarded. Join is refused for a closed list, for an address
// already on the list, and for one that has withdrawn from it. Withdraw names a
// row, so the standing to move it is asked of a seam the consumer implements --
// see WithdrawRequest. And the read a public caller gets is the catalog of open
// lists, which is what a signup page publishes anyway.
//
// # The administrative fourteen
//
// List CRUD, the signup reads, the two lifecycle transitions, the note, the
// archive and the erasure. Each is behind a grant, and waitlists/grpc's
// Permissions is the default map a consumer composes into their policy.
//
// GetSignupByContact is the one to look at twice. It is a read, it looks
// harmless beside Join, and it is the difference between a service and an oracle
// over which addresses are on which list.
type WaitlistsServiceClient interface {
	// The catalog: what lists exist, what they are for, and when each stops
	// taking signups. ListOpenLists is the public one.
	CreateList(ctx context.Context, in *CreateListRequest, opts ...grpc.CallOption) (*CreateListResponse, error)
	GetList(ctx context.Context, in *GetListRequest, opts ...grpc.CallOption) (*GetListResponse, error)
	ListLists(ctx context.Context, in *ListListsRequest, opts ...grpc.CallOption) (*ListListsResponse, error)
	ListOpenLists(ctx context.Context, in *ListOpenListsRequest, opts ...grpc.CallOption) (*ListOpenListsResponse, error)
	UpdateList(ctx context.Context, in *UpdateListRequest, opts ...grpc.CallOption) (*UpdateListResponse, error)
	ArchiveList(ctx context.Context, in *ArchiveListRequest, opts ...grpc.CallOption) (*ArchiveListResponse, error)
	// The queue. Join and Withdraw are the person's own; the rest are the
	// operator's.
	Join(ctx context.Context, in *JoinRequest, opts ...grpc.CallOption) (*JoinResponse, error)
	GetSignup(ctx context.Context, in *GetSignupRequest, opts ...grpc.CallOption) (*GetSignupResponse, error)
	GetSignupByContact(ctx context.Context, in *GetSignupByContactRequest, opts ...grpc.CallOption) (*GetSignupByContactResponse, error)
	ListSignups(ctx context.Context, in *ListSignupsRequest, opts ...grpc.CallOption) (*ListSignupsResponse, error)
	ListSignupsForSubject(ctx context.Context, in *ListSignupsForSubjectRequest, opts ...grpc.CallOption) (*ListSignupsForSubjectResponse, error)
	UpdateSignupNotes(ctx context.Context, in *UpdateSignupNotesRequest, opts ...grpc.CallOption) (*UpdateSignupNotesResponse, error)
	Invite(ctx context.Context, in *InviteRequest, opts ...grpc.CallOption) (*InviteResponse, error)
	Convert(ctx context.Context, in *ConvertRequest, opts ...grpc.CallOption) (*ConvertResponse, error)
	Withdraw(ctx context.Context, in *WithdrawRequest, opts ...grpc.CallOption) (*WithdrawResponse, error)
	WithdrawSignupsForSubject(ctx context.Context, in *WithdrawSignupsForSubjectRequest, opts ...grpc.CallOption) (*WithdrawSignupsForSubjectResponse, error)
	ArchiveSignup(ctx context.Context, in *ArchiveSignupRequest, opts ...grpc.CallOption) (*ArchiveSignupResponse, error)
}

type waitlistsServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewWaitlistsServiceClient(cc grpc.ClientConnInterface) WaitlistsServiceClient {
	return &waitlistsServiceClient{cc}
}

func (c *waitlistsServiceClient) CreateList(ctx context.Context, in *CreateListRequest, opts ...grpc.CallOption) (*CreateListResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(CreateListResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_CreateList_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) GetList(ctx context.Context, in *GetListRequest, opts ...grpc.CallOption) (*GetListResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetListResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_GetList_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) ListLists(ctx context.Context, in *ListListsRequest, opts ...grpc.CallOption) (*ListListsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListListsResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_ListLists_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) ListOpenLists(ctx context.Context, in *ListOpenListsRequest, opts ...grpc.CallOption) (*ListOpenListsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListOpenListsResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_ListOpenLists_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) UpdateList(ctx context.Context, in *UpdateListRequest, opts ...grpc.CallOption) (*UpdateListResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(UpdateListResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_UpdateList_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) ArchiveList(ctx context.Context, in *ArchiveListRequest, opts ...grpc.CallOption) (*ArchiveListResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ArchiveListResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_ArchiveList_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) Join(ctx context.Context, in *JoinRequest, opts ...grpc.CallOption) (*JoinResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(JoinResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_Join_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) GetSignup(ctx context.Context, in *GetSignupRequest, opts ...grpc.CallOption) (*GetSignupResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetSignupResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_GetSignup_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) GetSignupByContact(ctx context.Context, in *GetSignupByContactRequest, opts ...grpc.CallOption) (*GetSignupByContactResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetSignupByContactResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_GetSignupByContact_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) ListSignups(ctx context.Context, in *ListSignupsRequest, opts ...grpc.CallOption) (*ListSignupsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListSignupsResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_ListSignups_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) ListSignupsForSubject(ctx context.Context, in *ListSignupsForSubjectRequest, opts ...grpc.CallOption) (*ListSignupsForSubjectResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListSignupsForSubjectResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_ListSignupsForSubject_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) UpdateSignupNotes(ctx context.Context, in *UpdateSignupNotesRequest, opts ...grpc.CallOption) (*UpdateSignupNotesResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(UpdateSignupNotesResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_UpdateSignupNotes_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) Invite(ctx context.Context, in *InviteRequest, opts ...grpc.CallOption) (*InviteResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(InviteResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_Invite_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) Convert(ctx context.Context, in *ConvertRequest, opts ...grpc.CallOption) (*ConvertResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ConvertResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_Convert_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) Withdraw(ctx context.Context, in *WithdrawRequest, opts ...grpc.CallOption) (*WithdrawResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(WithdrawResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_Withdraw_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) WithdrawSignupsForSubject(ctx context.Context, in *WithdrawSignupsForSubjectRequest, opts ...grpc.CallOption) (*WithdrawSignupsForSubjectResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(WithdrawSignupsForSubjectResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_WithdrawSignupsForSubject_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *waitlistsServiceClient) ArchiveSignup(ctx context.Context, in *ArchiveSignupRequest, opts ...grpc.CallOption) (*ArchiveSignupResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ArchiveSignupResponse)
	err := c.cc.Invoke(ctx, WaitlistsService_ArchiveSignup_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// WaitlistsServiceServer is the server API for WaitlistsService service.
// All implementations must embed UnimplementedWaitlistsServiceServer
// for forward compatibility.
//
// WaitlistsService is the whole of waitlists on the wire: all seventeen methods
// of waitlists.Store, split by who calls them.
//
// There are no absences, which is unusual on this lane and is the reason this
// was the first of the ten domains to cross. Every other surface in the module
// carves something out because its realistic caller is a worker on a timer, a
// processor callback, or the consumer's own code inside its own transaction.
// Nothing here has that shape: a waitlist has no queue protocol, no fan-out and
// no provider callback, and every one of the seventeen is either a form
// somebody submitted or a console somebody is looking at.
//
// # The public three
//
// ListOpenLists, Join and Withdraw are reachable without a grant, because the
// caller is a person on a signup page who has not signed in and frequently has
// no account to sign in to. That is the whole of what "public" means here: the
// consumer's authentication interceptor still runs, and a caller who does arrive
// with a principal has their signup attributed to them.
//
// Public is not unguarded. Join is refused for a closed list, for an address
// already on the list, and for one that has withdrawn from it. Withdraw names a
// row, so the standing to move it is asked of a seam the consumer implements --
// see WithdrawRequest. And the read a public caller gets is the catalog of open
// lists, which is what a signup page publishes anyway.
//
// # The administrative fourteen
//
// List CRUD, the signup reads, the two lifecycle transitions, the note, the
// archive and the erasure. Each is behind a grant, and waitlists/grpc's
// Permissions is the default map a consumer composes into their policy.
//
// GetSignupByContact is the one to look at twice. It is a read, it looks
// harmless beside Join, and it is the difference between a service and an oracle
// over which addresses are on which list.
type WaitlistsServiceServer interface {
	// The catalog: what lists exist, what they are for, and when each stops
	// taking signups. ListOpenLists is the public one.
	CreateList(context.Context, *CreateListRequest) (*CreateListResponse, error)
	GetList(context.Context, *GetListRequest) (*GetListResponse, error)
	ListLists(context.Context, *ListListsRequest) (*ListListsResponse, error)
	ListOpenLists(context.Context, *ListOpenListsRequest) (*ListOpenListsResponse, error)
	UpdateList(context.Context, *UpdateListRequest) (*UpdateListResponse, error)
	ArchiveList(context.Context, *ArchiveListRequest) (*ArchiveListResponse, error)
	// The queue. Join and Withdraw are the person's own; the rest are the
	// operator's.
	Join(context.Context, *JoinRequest) (*JoinResponse, error)
	GetSignup(context.Context, *GetSignupRequest) (*GetSignupResponse, error)
	GetSignupByContact(context.Context, *GetSignupByContactRequest) (*GetSignupByContactResponse, error)
	ListSignups(context.Context, *ListSignupsRequest) (*ListSignupsResponse, error)
	ListSignupsForSubject(context.Context, *ListSignupsForSubjectRequest) (*ListSignupsForSubjectResponse, error)
	UpdateSignupNotes(context.Context, *UpdateSignupNotesRequest) (*UpdateSignupNotesResponse, error)
	Invite(context.Context, *InviteRequest) (*InviteResponse, error)
	Convert(context.Context, *ConvertRequest) (*ConvertResponse, error)
	Withdraw(context.Context, *WithdrawRequest) (*WithdrawResponse, error)
	WithdrawSignupsForSubject(context.Context, *WithdrawSignupsForSubjectRequest) (*WithdrawSignupsForSubjectResponse, error)
	ArchiveSignup(context.Context, *ArchiveSignupRequest) (*ArchiveSignupResponse, error)
	mustEmbedUnimplementedWaitlistsServiceServer()
}

// UnimplementedWaitlistsServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedWaitlistsServiceServer struct{}

func (UnimplementedWaitlistsServiceServer) CreateList(context.Context, *CreateListRequest) (*CreateListResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateList not implemented")
}
func (UnimplementedWaitlistsServiceServer) GetList(context.Context, *GetListRequest) (*GetListResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetList not implemented")
}
func (UnimplementedWaitlistsServiceServer) ListLists(context.Context, *ListListsRequest) (*ListListsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListLists not implemented")
}
func (UnimplementedWaitlistsServiceServer) ListOpenLists(context.Context, *ListOpenListsRequest) (*ListOpenListsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListOpenLists not implemented")
}
func (UnimplementedWaitlistsServiceServer) UpdateList(context.Context, *UpdateListRequest) (*UpdateListResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateList not implemented")
}
func (UnimplementedWaitlistsServiceServer) ArchiveList(context.Context, *ArchiveListRequest) (*ArchiveListResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ArchiveList not implemented")
}
func (UnimplementedWaitlistsServiceServer) Join(context.Context, *JoinRequest) (*JoinResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Join not implemented")
}
func (UnimplementedWaitlistsServiceServer) GetSignup(context.Context, *GetSignupRequest) (*GetSignupResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetSignup not implemented")
}
func (UnimplementedWaitlistsServiceServer) GetSignupByContact(context.Context, *GetSignupByContactRequest) (*GetSignupByContactResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetSignupByContact not implemented")
}
func (UnimplementedWaitlistsServiceServer) ListSignups(context.Context, *ListSignupsRequest) (*ListSignupsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListSignups not implemented")
}
func (UnimplementedWaitlistsServiceServer) ListSignupsForSubject(context.Context, *ListSignupsForSubjectRequest) (*ListSignupsForSubjectResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListSignupsForSubject not implemented")
}
func (UnimplementedWaitlistsServiceServer) UpdateSignupNotes(context.Context, *UpdateSignupNotesRequest) (*UpdateSignupNotesResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateSignupNotes not implemented")
}
func (UnimplementedWaitlistsServiceServer) Invite(context.Context, *InviteRequest) (*InviteResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Invite not implemented")
}
func (UnimplementedWaitlistsServiceServer) Convert(context.Context, *ConvertRequest) (*ConvertResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Convert not implemented")
}
func (UnimplementedWaitlistsServiceServer) Withdraw(context.Context, *WithdrawRequest) (*WithdrawResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Withdraw not implemented")
}
func (UnimplementedWaitlistsServiceServer) WithdrawSignupsForSubject(context.Context, *WithdrawSignupsForSubjectRequest) (*WithdrawSignupsForSubjectResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method WithdrawSignupsForSubject not implemented")
}
func (UnimplementedWaitlistsServiceServer) ArchiveSignup(context.Context, *ArchiveSignupRequest) (*ArchiveSignupResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ArchiveSignup not implemented")
}
func (UnimplementedWaitlistsServiceServer) mustEmbedUnimplementedWaitlistsServiceServer() {}
func (UnimplementedWaitlistsServiceServer) testEmbeddedByValue()                          {}

// UnsafeWaitlistsServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to WaitlistsServiceServer will
// result in compilation errors.
type UnsafeWaitlistsServiceServer interface {
	mustEmbedUnimplementedWaitlistsServiceServer()
}

func RegisterWaitlistsServiceServer(s grpc.ServiceRegistrar, srv WaitlistsServiceServer) {
	// If the following call pancis, it indicates UnimplementedWaitlistsServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&WaitlistsService_ServiceDesc, srv)
}

func _WaitlistsService_CreateList_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateListRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).CreateList(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_CreateList_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).CreateList(ctx, req.(*CreateListRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_GetList_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetListRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).GetList(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_GetList_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).GetList(ctx, req.(*GetListRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_ListLists_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListListsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).ListLists(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_ListLists_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).ListLists(ctx, req.(*ListListsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_ListOpenLists_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListOpenListsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).ListOpenLists(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_ListOpenLists_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).ListOpenLists(ctx, req.(*ListOpenListsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_UpdateList_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(UpdateListRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).UpdateList(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_UpdateList_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).UpdateList(ctx, req.(*UpdateListRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_ArchiveList_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ArchiveListRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).ArchiveList(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_ArchiveList_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).ArchiveList(ctx, req.(*ArchiveListRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_Join_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(JoinRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).Join(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_Join_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).Join(ctx, req.(*JoinRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_GetSignup_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetSignupRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).GetSignup(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_GetSignup_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).GetSignup(ctx, req.(*GetSignupRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_GetSignupByContact_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetSignupByContactRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).GetSignupByContact(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_GetSignupByContact_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).GetSignupByContact(ctx, req.(*GetSignupByContactRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_ListSignups_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListSignupsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).ListSignups(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_ListSignups_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).ListSignups(ctx, req.(*ListSignupsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_ListSignupsForSubject_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListSignupsForSubjectRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).ListSignupsForSubject(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_ListSignupsForSubject_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).ListSignupsForSubject(ctx, req.(*ListSignupsForSubjectRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_UpdateSignupNotes_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(UpdateSignupNotesRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).UpdateSignupNotes(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_UpdateSignupNotes_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).UpdateSignupNotes(ctx, req.(*UpdateSignupNotesRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_Invite_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(InviteRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).Invite(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_Invite_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).Invite(ctx, req.(*InviteRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_Convert_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ConvertRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).Convert(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_Convert_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).Convert(ctx, req.(*ConvertRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_Withdraw_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(WithdrawRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).Withdraw(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_Withdraw_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).Withdraw(ctx, req.(*WithdrawRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_WithdrawSignupsForSubject_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(WithdrawSignupsForSubjectRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).WithdrawSignupsForSubject(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_WithdrawSignupsForSubject_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).WithdrawSignupsForSubject(ctx, req.(*WithdrawSignupsForSubjectRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _WaitlistsService_ArchiveSignup_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ArchiveSignupRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(WaitlistsServiceServer).ArchiveSignup(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: WaitlistsService_ArchiveSignup_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(WaitlistsServiceServer).ArchiveSignup(ctx, req.(*ArchiveSignupRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// WaitlistsService_ServiceDesc is the grpc.ServiceDesc for WaitlistsService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var WaitlistsService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "primandproper.platform.waitlists.v1.WaitlistsService",
	HandlerType: (*WaitlistsServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "CreateList",
			Handler:    _WaitlistsService_CreateList_Handler,
		},
		{
			MethodName: "GetList",
			Handler:    _WaitlistsService_GetList_Handler,
		},
		{
			MethodName: "ListLists",
			Handler:    _WaitlistsService_ListLists_Handler,
		},
		{
			MethodName: "ListOpenLists",
			Handler:    _WaitlistsService_ListOpenLists_Handler,
		},
		{
			MethodName: "UpdateList",
			Handler:    _WaitlistsService_UpdateList_Handler,
		},
		{
			MethodName: "ArchiveList",
			Handler:    _WaitlistsService_ArchiveList_Handler,
		},
		{
			MethodName: "Join",
			Handler:    _WaitlistsService_Join_Handler,
		},
		{
			MethodName: "GetSignup",
			Handler:    _WaitlistsService_GetSignup_Handler,
		},
		{
			MethodName: "GetSignupByContact",
			Handler:    _WaitlistsService_GetSignupByContact_Handler,
		},
		{
			MethodName: "ListSignups",
			Handler:    _WaitlistsService_ListSignups_Handler,
		},
		{
			MethodName: "ListSignupsForSubject",
			Handler:    _WaitlistsService_ListSignupsForSubject_Handler,
		},
		{
			MethodName: "UpdateSignupNotes",
			Handler:    _WaitlistsService_UpdateSignupNotes_Handler,
		},
		{
			MethodName: "Invite",
			Handler:    _WaitlistsService_Invite_Handler,
		},
		{
			MethodName: "Convert",
			Handler:    _WaitlistsService_Convert_Handler,
		},
		{
			MethodName: "Withdraw",
			Handler:    _WaitlistsService_Withdraw_Handler,
		},
		{
			MethodName: "WithdrawSignupsForSubject",
			Handler:    _WaitlistsService_WithdrawSignupsForSubject_Handler,
		},
		{
			MethodName: "ArchiveSignup",
			Handler:    _WaitlistsService_ArchiveSignup_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "primandproper/platform/waitlists/v1/waitlists.proto",
}
