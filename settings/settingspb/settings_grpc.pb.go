// Package primandproper.platform.settings.v1 is the wire schema for runtime
// settings: the catalog an operator administers, the answers a person gives
// about themselves, and what a setting resolves to for somebody who has not
// answered it.
//
// Resolution is the point of it. A stored value falling back to the
// definition's default, and a third answer for a setting nobody has decided, is
// what a hand-written settings service gets subtly wrong -- a screen showing
// blanks where it should show defaults -- so [ResolvedSetting] is the message
// the rest of this file exists to make readable.
//
// This file is shipped inside the published Go module, and it is the file
// itself that is shipped -- not a copy for you to keep in sync. A consumer puts
// the module's proto directories on protoc's path and imports this file by its
// canonical name, exactly as identity.proto and filtering.proto already work:
//
//	PLATFORM_PROTO := $(shell go list -m -f '{{.Dir}}' github.com/primandproper/platform-go/v14)
//
//	protoc --proto_path proto/ \
//	    --proto_path $(PLATFORM_PROTO)/settings/proto \
//	    --proto_path $(PLATFORM_PROTO)/filtering/proto \
//	    --go_opt=Mprimandproper/platform/settings/v1/settings.proto=github.com/primandproper/platform-go/v14/settings/settingspb \
//	    $(CONSUMER_PROTO_FILES)   # the platform files deliberately absent from that list
//
// Field numbers are the compatibility promise, across every language a consumer
// generates into. Numbers are never reused and never repurposed: a field that
// goes away is reserved.
//
// # A value is typed where this package parses one, and a string where it compares bytes
//
// The one design decision in this file. settings.Kind is a closed set of four
// -- string, boolean, integer, float -- and every value is stored as text, so
// there are two honest ways to put one on a wire: a string plus the kind, which
// makes every client re-derive the parse this package exists to have made once,
// or a value that carries its own type. This schema does the second, in
// [TypedValue], and applies it in exactly two places.
//
// Typed: [ResolvedSetting.typed_value], which is what Resolve and ResolveAll
// answer with, and [SetValueRequest.value], which is what a person's choice
// arrives as. Both are the parse. A client rendering a checkbox reads
// bool_value rather than comparing a string against "true", and a client saving
// one sends bool_value rather than formatting one -- and a bool_value sent for
// an integer setting is refused with settings.ErrKindMismatch at the boundary
// rather than stored as a row nothing can read back.
//
// A string: [SettingValue.raw], which is the row as stored and which GetValue
// is documented to hand back unparsed; and [SettingDefinition.default_value]
// and [SettingDefinition.enumeration], which are what every write is checked
// against, byte for byte. That last one is the reason for the split rather than
// a taste for consistency. A definition read as typed values and written back
// unchanged would round-trip through a parse and a format -- "1.50" arriving
// back as "1.5" -- and the enumeration is compared to stored values by string
// equality, so an edit that changed nothing could refuse itself with
// settings.ErrStrandedValues. A default is held to its own enumeration by the
// same comparison.
//
// # Presence, and why the value is a oneof rather than a string
//
// "Absence is distinguishable from zero" is this package's doctrine and not a
// preference: a text setting defaulting to "" answers every subject who has not
// chosen, and one with no default answers none of them. A proto3 string cannot
// hold that difference. A oneof can -- a string_value of "" sets the case and
// is a value somebody chose, where naming no case at all is a request that
// named no value -- and default_value is `optional` for the same reason, which
// is the presence the Go *string carries.
//
// The third state has a field rather than an error. A setting the subject has
// not answered and that has no default resolves to
// [ValueSource.VALUE_SOURCE_UNSET] with no typed_value, because it is an answer
// -- "nobody has decided" -- and the caller's own policy applies.
// settings.ErrSettingUnset is what the Go accessors report for the same state
// and is mapped for a consumer's own handlers, but no RPC here raises it.
//
// # What is a generated enum here, and what is not
//
// [SettingKind] and [ValueSource] are enums because they are closed sets this
// package defines: a kind decides how a stored string is parsed, so a kind this
// module does not implement is a value nothing can read back. That is the
// opposite case from issuereports.Kind and comments.TargetType, which are the
// consumer's catalog and stay opaque strings.
//
// The two vocabularies that are the consumer's stay strings here too. A
// definition's name is one -- "notifications.digest" is the application's word,
// and a generated enum would put it on this module's release cadence -- and so
// is [SettingSubject.type], which settings.SubjectType documents as a bare
// string precisely so that an application whose settings hang off a device, a
// workspace or an API client can say so.
//
// # What is not here, and why
//
// No scope field, anywhere, and the name is reserved so there cannot be one. A
// scope a client could name is a cross-tenant read hiding behind a request
// field -- here, a read or a write of another tenant's catalog. It comes off
// the principal the consumer's interceptor put on the context, and every
// statement behind these RPCs binds it. Reserving the name rather than only
// saying so is audit.proto's pattern: `reserved "scope";` is a schema protoc
// refuses to accept a scope field into, in this repository and in a consumer's
// fork of the file alike, whereas a comment is a request to the next author. It
// is reserved on every request message and on the four messages a response is
// built from. See identity.proto, which says the underlying rule at greater
// length.
//
// No erasure. settings.Store.DeleteValuesForSubject destroys everything one
// subject answered, cleared answers included, and it is the one hard delete in
// that package -- called by a dataprivacy.Eraser from inside the transaction
// that removes the rest of the person. An RPC moves that write out of the
// transaction that was the entire point of it, which leaves a subject erased
// from one table and present in the others. The service comment below says so
// again where a reader counting RPCs will be standing.
//
// No subject in a response's own right. Every value message carries the
// [SettingSubject] it is about, because a page of one definition's values is a
// page across subjects, but nothing here lets a caller ask for a subject
// without the surface asking whether they may -- see settings/grpc's
// SubjectAuthorizer, which is the half of authorization a per-method grant
// cannot reach.

// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.5.1
// - protoc             v6.33.1
// source: primandproper/platform/settings/v1/settings.proto

package settingspb

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
	SettingsService_CreateDefinition_FullMethodName        = "/primandproper.platform.settings.v1.SettingsService/CreateDefinition"
	SettingsService_GetDefinition_FullMethodName           = "/primandproper.platform.settings.v1.SettingsService/GetDefinition"
	SettingsService_GetDefinitionByName_FullMethodName     = "/primandproper.platform.settings.v1.SettingsService/GetDefinitionByName"
	SettingsService_ListDefinitions_FullMethodName         = "/primandproper.platform.settings.v1.SettingsService/ListDefinitions"
	SettingsService_UpdateDefinition_FullMethodName        = "/primandproper.platform.settings.v1.SettingsService/UpdateDefinition"
	SettingsService_ArchiveDefinition_FullMethodName       = "/primandproper.platform.settings.v1.SettingsService/ArchiveDefinition"
	SettingsService_ListValuesForDefinition_FullMethodName = "/primandproper.platform.settings.v1.SettingsService/ListValuesForDefinition"
	SettingsService_SetValue_FullMethodName                = "/primandproper.platform.settings.v1.SettingsService/SetValue"
	SettingsService_GetValue_FullMethodName                = "/primandproper.platform.settings.v1.SettingsService/GetValue"
	SettingsService_ClearValue_FullMethodName              = "/primandproper.platform.settings.v1.SettingsService/ClearValue"
	SettingsService_ListValuesForSubject_FullMethodName    = "/primandproper.platform.settings.v1.SettingsService/ListValuesForSubject"
	SettingsService_Resolve_FullMethodName                 = "/primandproper.platform.settings.v1.SettingsService/Resolve"
	SettingsService_ResolveAll_FullMethodName              = "/primandproper.platform.settings.v1.SettingsService/ResolveAll"
)

// SettingsServiceClient is the client API for SettingsService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
//
// SettingsService is the catalog and the answers stored against it.
//
// Thirteen RPCs over the fourteen methods of settings.Store, and the split in
// them is two audiences rather than two nouns. The seven definition methods are
// an operator's: what settings exist, what each holds, and who has overridden
// one. The six value methods are a person acting on themselves, and they are
// the settings screen every consumer ships.
//
// The fourteenth is DeleteValuesForSubject and it is deliberately absent. It
// destroys everything one subject answered, cleared answers included, and it is
// erasure machinery -- a dataprivacy.Eraser or a retention sweep calling on a
// subject's behalf from inside the transaction that removes the rest of them.
// A write whose whole property is that it commits with its caller's other
// writes is not an RPC: over a wire it lands in a transaction of its own, at a
// moment the caller does not choose, and what you get is a person erased from
// one table and present in the others. settings.Store documents the same
// absence on the method itself.
//
// Every method takes its scope off the caller's principal, and six of them take
// a subject from the request -- which a grant on the method cannot check, so
// settings/grpc asks a SubjectAuthorizer before any of the six reads or writes
// a row.
type SettingsServiceClient interface {
	CreateDefinition(ctx context.Context, in *CreateDefinitionRequest, opts ...grpc.CallOption) (*CreateDefinitionResponse, error)
	GetDefinition(ctx context.Context, in *GetDefinitionRequest, opts ...grpc.CallOption) (*GetDefinitionResponse, error)
	GetDefinitionByName(ctx context.Context, in *GetDefinitionByNameRequest, opts ...grpc.CallOption) (*GetDefinitionByNameResponse, error)
	ListDefinitions(ctx context.Context, in *ListDefinitionsRequest, opts ...grpc.CallOption) (*ListDefinitionsResponse, error)
	UpdateDefinition(ctx context.Context, in *UpdateDefinitionRequest, opts ...grpc.CallOption) (*UpdateDefinitionResponse, error)
	ArchiveDefinition(ctx context.Context, in *ArchiveDefinitionRequest, opts ...grpc.CallOption) (*ArchiveDefinitionResponse, error)
	ListValuesForDefinition(ctx context.Context, in *ListValuesForDefinitionRequest, opts ...grpc.CallOption) (*ListValuesForDefinitionResponse, error)
	SetValue(ctx context.Context, in *SetValueRequest, opts ...grpc.CallOption) (*SetValueResponse, error)
	GetValue(ctx context.Context, in *GetValueRequest, opts ...grpc.CallOption) (*GetValueResponse, error)
	ClearValue(ctx context.Context, in *ClearValueRequest, opts ...grpc.CallOption) (*ClearValueResponse, error)
	ListValuesForSubject(ctx context.Context, in *ListValuesForSubjectRequest, opts ...grpc.CallOption) (*ListValuesForSubjectResponse, error)
	Resolve(ctx context.Context, in *ResolveRequest, opts ...grpc.CallOption) (*ResolveResponse, error)
	ResolveAll(ctx context.Context, in *ResolveAllRequest, opts ...grpc.CallOption) (*ResolveAllResponse, error)
}

type settingsServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewSettingsServiceClient(cc grpc.ClientConnInterface) SettingsServiceClient {
	return &settingsServiceClient{cc}
}

func (c *settingsServiceClient) CreateDefinition(ctx context.Context, in *CreateDefinitionRequest, opts ...grpc.CallOption) (*CreateDefinitionResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(CreateDefinitionResponse)
	err := c.cc.Invoke(ctx, SettingsService_CreateDefinition_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) GetDefinition(ctx context.Context, in *GetDefinitionRequest, opts ...grpc.CallOption) (*GetDefinitionResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetDefinitionResponse)
	err := c.cc.Invoke(ctx, SettingsService_GetDefinition_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) GetDefinitionByName(ctx context.Context, in *GetDefinitionByNameRequest, opts ...grpc.CallOption) (*GetDefinitionByNameResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetDefinitionByNameResponse)
	err := c.cc.Invoke(ctx, SettingsService_GetDefinitionByName_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) ListDefinitions(ctx context.Context, in *ListDefinitionsRequest, opts ...grpc.CallOption) (*ListDefinitionsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListDefinitionsResponse)
	err := c.cc.Invoke(ctx, SettingsService_ListDefinitions_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) UpdateDefinition(ctx context.Context, in *UpdateDefinitionRequest, opts ...grpc.CallOption) (*UpdateDefinitionResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(UpdateDefinitionResponse)
	err := c.cc.Invoke(ctx, SettingsService_UpdateDefinition_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) ArchiveDefinition(ctx context.Context, in *ArchiveDefinitionRequest, opts ...grpc.CallOption) (*ArchiveDefinitionResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ArchiveDefinitionResponse)
	err := c.cc.Invoke(ctx, SettingsService_ArchiveDefinition_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) ListValuesForDefinition(ctx context.Context, in *ListValuesForDefinitionRequest, opts ...grpc.CallOption) (*ListValuesForDefinitionResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListValuesForDefinitionResponse)
	err := c.cc.Invoke(ctx, SettingsService_ListValuesForDefinition_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) SetValue(ctx context.Context, in *SetValueRequest, opts ...grpc.CallOption) (*SetValueResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(SetValueResponse)
	err := c.cc.Invoke(ctx, SettingsService_SetValue_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) GetValue(ctx context.Context, in *GetValueRequest, opts ...grpc.CallOption) (*GetValueResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetValueResponse)
	err := c.cc.Invoke(ctx, SettingsService_GetValue_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) ClearValue(ctx context.Context, in *ClearValueRequest, opts ...grpc.CallOption) (*ClearValueResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ClearValueResponse)
	err := c.cc.Invoke(ctx, SettingsService_ClearValue_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) ListValuesForSubject(ctx context.Context, in *ListValuesForSubjectRequest, opts ...grpc.CallOption) (*ListValuesForSubjectResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListValuesForSubjectResponse)
	err := c.cc.Invoke(ctx, SettingsService_ListValuesForSubject_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) Resolve(ctx context.Context, in *ResolveRequest, opts ...grpc.CallOption) (*ResolveResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ResolveResponse)
	err := c.cc.Invoke(ctx, SettingsService_Resolve_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsServiceClient) ResolveAll(ctx context.Context, in *ResolveAllRequest, opts ...grpc.CallOption) (*ResolveAllResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ResolveAllResponse)
	err := c.cc.Invoke(ctx, SettingsService_ResolveAll_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SettingsServiceServer is the server API for SettingsService service.
// All implementations must embed UnimplementedSettingsServiceServer
// for forward compatibility.
//
// SettingsService is the catalog and the answers stored against it.
//
// Thirteen RPCs over the fourteen methods of settings.Store, and the split in
// them is two audiences rather than two nouns. The seven definition methods are
// an operator's: what settings exist, what each holds, and who has overridden
// one. The six value methods are a person acting on themselves, and they are
// the settings screen every consumer ships.
//
// The fourteenth is DeleteValuesForSubject and it is deliberately absent. It
// destroys everything one subject answered, cleared answers included, and it is
// erasure machinery -- a dataprivacy.Eraser or a retention sweep calling on a
// subject's behalf from inside the transaction that removes the rest of them.
// A write whose whole property is that it commits with its caller's other
// writes is not an RPC: over a wire it lands in a transaction of its own, at a
// moment the caller does not choose, and what you get is a person erased from
// one table and present in the others. settings.Store documents the same
// absence on the method itself.
//
// Every method takes its scope off the caller's principal, and six of them take
// a subject from the request -- which a grant on the method cannot check, so
// settings/grpc asks a SubjectAuthorizer before any of the six reads or writes
// a row.
type SettingsServiceServer interface {
	CreateDefinition(context.Context, *CreateDefinitionRequest) (*CreateDefinitionResponse, error)
	GetDefinition(context.Context, *GetDefinitionRequest) (*GetDefinitionResponse, error)
	GetDefinitionByName(context.Context, *GetDefinitionByNameRequest) (*GetDefinitionByNameResponse, error)
	ListDefinitions(context.Context, *ListDefinitionsRequest) (*ListDefinitionsResponse, error)
	UpdateDefinition(context.Context, *UpdateDefinitionRequest) (*UpdateDefinitionResponse, error)
	ArchiveDefinition(context.Context, *ArchiveDefinitionRequest) (*ArchiveDefinitionResponse, error)
	ListValuesForDefinition(context.Context, *ListValuesForDefinitionRequest) (*ListValuesForDefinitionResponse, error)
	SetValue(context.Context, *SetValueRequest) (*SetValueResponse, error)
	GetValue(context.Context, *GetValueRequest) (*GetValueResponse, error)
	ClearValue(context.Context, *ClearValueRequest) (*ClearValueResponse, error)
	ListValuesForSubject(context.Context, *ListValuesForSubjectRequest) (*ListValuesForSubjectResponse, error)
	Resolve(context.Context, *ResolveRequest) (*ResolveResponse, error)
	ResolveAll(context.Context, *ResolveAllRequest) (*ResolveAllResponse, error)
	mustEmbedUnimplementedSettingsServiceServer()
}

// UnimplementedSettingsServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedSettingsServiceServer struct{}

func (UnimplementedSettingsServiceServer) CreateDefinition(context.Context, *CreateDefinitionRequest) (*CreateDefinitionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateDefinition not implemented")
}
func (UnimplementedSettingsServiceServer) GetDefinition(context.Context, *GetDefinitionRequest) (*GetDefinitionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetDefinition not implemented")
}
func (UnimplementedSettingsServiceServer) GetDefinitionByName(context.Context, *GetDefinitionByNameRequest) (*GetDefinitionByNameResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetDefinitionByName not implemented")
}
func (UnimplementedSettingsServiceServer) ListDefinitions(context.Context, *ListDefinitionsRequest) (*ListDefinitionsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListDefinitions not implemented")
}
func (UnimplementedSettingsServiceServer) UpdateDefinition(context.Context, *UpdateDefinitionRequest) (*UpdateDefinitionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateDefinition not implemented")
}
func (UnimplementedSettingsServiceServer) ArchiveDefinition(context.Context, *ArchiveDefinitionRequest) (*ArchiveDefinitionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ArchiveDefinition not implemented")
}
func (UnimplementedSettingsServiceServer) ListValuesForDefinition(context.Context, *ListValuesForDefinitionRequest) (*ListValuesForDefinitionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListValuesForDefinition not implemented")
}
func (UnimplementedSettingsServiceServer) SetValue(context.Context, *SetValueRequest) (*SetValueResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetValue not implemented")
}
func (UnimplementedSettingsServiceServer) GetValue(context.Context, *GetValueRequest) (*GetValueResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetValue not implemented")
}
func (UnimplementedSettingsServiceServer) ClearValue(context.Context, *ClearValueRequest) (*ClearValueResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ClearValue not implemented")
}
func (UnimplementedSettingsServiceServer) ListValuesForSubject(context.Context, *ListValuesForSubjectRequest) (*ListValuesForSubjectResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListValuesForSubject not implemented")
}
func (UnimplementedSettingsServiceServer) Resolve(context.Context, *ResolveRequest) (*ResolveResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Resolve not implemented")
}
func (UnimplementedSettingsServiceServer) ResolveAll(context.Context, *ResolveAllRequest) (*ResolveAllResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ResolveAll not implemented")
}
func (UnimplementedSettingsServiceServer) mustEmbedUnimplementedSettingsServiceServer() {}
func (UnimplementedSettingsServiceServer) testEmbeddedByValue()                         {}

// UnsafeSettingsServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to SettingsServiceServer will
// result in compilation errors.
type UnsafeSettingsServiceServer interface {
	mustEmbedUnimplementedSettingsServiceServer()
}

func RegisterSettingsServiceServer(s grpc.ServiceRegistrar, srv SettingsServiceServer) {
	// If the following call pancis, it indicates UnimplementedSettingsServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&SettingsService_ServiceDesc, srv)
}

func _SettingsService_CreateDefinition_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateDefinitionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).CreateDefinition(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_CreateDefinition_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).CreateDefinition(ctx, req.(*CreateDefinitionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_GetDefinition_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetDefinitionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).GetDefinition(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_GetDefinition_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).GetDefinition(ctx, req.(*GetDefinitionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_GetDefinitionByName_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetDefinitionByNameRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).GetDefinitionByName(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_GetDefinitionByName_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).GetDefinitionByName(ctx, req.(*GetDefinitionByNameRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_ListDefinitions_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListDefinitionsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).ListDefinitions(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_ListDefinitions_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).ListDefinitions(ctx, req.(*ListDefinitionsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_UpdateDefinition_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(UpdateDefinitionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).UpdateDefinition(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_UpdateDefinition_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).UpdateDefinition(ctx, req.(*UpdateDefinitionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_ArchiveDefinition_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ArchiveDefinitionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).ArchiveDefinition(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_ArchiveDefinition_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).ArchiveDefinition(ctx, req.(*ArchiveDefinitionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_ListValuesForDefinition_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListValuesForDefinitionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).ListValuesForDefinition(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_ListValuesForDefinition_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).ListValuesForDefinition(ctx, req.(*ListValuesForDefinitionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_SetValue_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(SetValueRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).SetValue(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_SetValue_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).SetValue(ctx, req.(*SetValueRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_GetValue_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetValueRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).GetValue(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_GetValue_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).GetValue(ctx, req.(*GetValueRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_ClearValue_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ClearValueRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).ClearValue(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_ClearValue_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).ClearValue(ctx, req.(*ClearValueRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_ListValuesForSubject_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListValuesForSubjectRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).ListValuesForSubject(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_ListValuesForSubject_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).ListValuesForSubject(ctx, req.(*ListValuesForSubjectRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_Resolve_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ResolveRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).Resolve(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_Resolve_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).Resolve(ctx, req.(*ResolveRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsService_ResolveAll_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ResolveAllRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsServiceServer).ResolveAll(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsService_ResolveAll_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsServiceServer).ResolveAll(ctx, req.(*ResolveAllRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// SettingsService_ServiceDesc is the grpc.ServiceDesc for SettingsService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var SettingsService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "primandproper.platform.settings.v1.SettingsService",
	HandlerType: (*SettingsServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "CreateDefinition",
			Handler:    _SettingsService_CreateDefinition_Handler,
		},
		{
			MethodName: "GetDefinition",
			Handler:    _SettingsService_GetDefinition_Handler,
		},
		{
			MethodName: "GetDefinitionByName",
			Handler:    _SettingsService_GetDefinitionByName_Handler,
		},
		{
			MethodName: "ListDefinitions",
			Handler:    _SettingsService_ListDefinitions_Handler,
		},
		{
			MethodName: "UpdateDefinition",
			Handler:    _SettingsService_UpdateDefinition_Handler,
		},
		{
			MethodName: "ArchiveDefinition",
			Handler:    _SettingsService_ArchiveDefinition_Handler,
		},
		{
			MethodName: "ListValuesForDefinition",
			Handler:    _SettingsService_ListValuesForDefinition_Handler,
		},
		{
			MethodName: "SetValue",
			Handler:    _SettingsService_SetValue_Handler,
		},
		{
			MethodName: "GetValue",
			Handler:    _SettingsService_GetValue_Handler,
		},
		{
			MethodName: "ClearValue",
			Handler:    _SettingsService_ClearValue_Handler,
		},
		{
			MethodName: "ListValuesForSubject",
			Handler:    _SettingsService_ListValuesForSubject_Handler,
		},
		{
			MethodName: "Resolve",
			Handler:    _SettingsService_Resolve_Handler,
		},
		{
			MethodName: "ResolveAll",
			Handler:    _SettingsService_ResolveAll_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "primandproper/platform/settings/v1/settings.proto",
}
