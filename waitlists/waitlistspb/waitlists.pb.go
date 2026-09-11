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

// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.11
// 	protoc        v6.33.1
// source: primandproper/platform/waitlists/v1/waitlists.proto

package waitlistspb

import (
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"

	filteringpb "github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// SignupStatus is where one signup stands. It is a closed set this package owns
// -- see the file comment for why this one is an enum where a consumer's catalog
// would be a string.
type SignupStatus int32

const (
	// SIGNUP_STATUS_UNSPECIFIED is the zero value, and no stored signup has it.
	// A request carrying it is a client that did not set the field.
	SignupStatus_SIGNUP_STATUS_UNSPECIFIED SignupStatus = 0
	// SIGNUP_STATUS_WAITING is somebody who has joined and not yet been invited.
	// It is where every signup starts, and it is the only status Join writes.
	SignupStatus_SIGNUP_STATUS_WAITING SignupStatus = 1
	// SIGNUP_STATUS_INVITED is somebody who has been let in and has not yet taken
	// it up. status_changed_at is when, which is the field a reminder is
	// scheduled off.
	SignupStatus_SIGNUP_STATUS_INVITED SignupStatus = 2
	// SIGNUP_STATUS_CONVERTED is somebody who took the invitation up. The
	// waitlist has done its job and whatever they became is a row of yours.
	SignupStatus_SIGNUP_STATUS_CONVERTED SignupStatus = 3
	// SIGNUP_STATUS_WITHDRAWN is somebody who asked to come off the list. It is
	// the one status nothing moves out of, and it is a suppression rather than a
	// deletion: the row keeps a digest of the address and loses everything else
	// that identifies a person, so a later signup from the same address is
	// refused rather than quietly re-subscribing whoever asked to be left alone.
	SignupStatus_SIGNUP_STATUS_WITHDRAWN SignupStatus = 4
)

// Enum value maps for SignupStatus.
var (
	SignupStatus_name = map[int32]string{
		0: "SIGNUP_STATUS_UNSPECIFIED",
		1: "SIGNUP_STATUS_WAITING",
		2: "SIGNUP_STATUS_INVITED",
		3: "SIGNUP_STATUS_CONVERTED",
		4: "SIGNUP_STATUS_WITHDRAWN",
	}
	SignupStatus_value = map[string]int32{
		"SIGNUP_STATUS_UNSPECIFIED": 0,
		"SIGNUP_STATUS_WAITING":     1,
		"SIGNUP_STATUS_INVITED":     2,
		"SIGNUP_STATUS_CONVERTED":   3,
		"SIGNUP_STATUS_WITHDRAWN":   4,
	}
)

func (x SignupStatus) Enum() *SignupStatus {
	p := new(SignupStatus)
	*p = x
	return p
}

func (x SignupStatus) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (SignupStatus) Descriptor() protoreflect.EnumDescriptor {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_enumTypes[0].Descriptor()
}

func (SignupStatus) Type() protoreflect.EnumType {
	return &file_primandproper_platform_waitlists_v1_waitlists_proto_enumTypes[0]
}

func (x SignupStatus) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use SignupStatus.Descriptor instead.
func (SignupStatus) EnumDescriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{0}
}

// Waitlist is one named queue, as a client sees it.
//
// It is a Waitlist here and a waitlists.List in Go, because List is a word every
// language a consumer generates into has already spent.
type Waitlist struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// created_at is when the list was opened, assigned by the database.
	CreatedAt *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=created_at,json=createdAt,proto3" json:"created_at,omitempty"`
	// closes_at is when the list stops taking signups. Required, and there is no
	// way to say "never": a list whose end is not yet decided names a far horizon
	// and is brought in by UpdateList, and one that should stop this instant is
	// archived. The column behind it is NOT NULL so that the open-lists read is
	// one comparison against a bound instant rather than a disjunction over the
	// column it pages by.
	ClosesAt *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=closes_at,json=closesAt,proto3" json:"closes_at,omitempty"`
	// last_updated_at is when the list last changed, unset for one nobody has
	// edited.
	LastUpdatedAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=last_updated_at,json=lastUpdatedAt,proto3" json:"last_updated_at,omitempty"`
	// archived_at is when the list was retired, unset while it is live. An
	// archived list takes no further signups whatever closes_at says, and the
	// signups already against it are left alone -- archiving is not erasure.
	ArchivedAt *timestamppb.Timestamp `protobuf:"bytes,4,opt,name=archived_at,json=archivedAt,proto3" json:"archived_at,omitempty"`
	// id is the row, and is what every other RPC names a list by.
	Id string `protobuf:"bytes,5,opt,name=id,proto3" json:"id,omitempty"`
	// name is what the list is called, for whoever administers it and for
	// whatever renders the signup form. Required, not unique, and not a handle:
	// two lists may share a name and neither is reachable by it. Text somebody
	// typed -- render it, never trust it.
	Name string `protobuf:"bytes,6,opt,name=name,proto3" json:"name,omitempty"`
	// description is prose about what people are queueing for.
	Description   string `protobuf:"bytes,7,opt,name=description,proto3" json:"description,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Waitlist) Reset() {
	*x = Waitlist{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Waitlist) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Waitlist) ProtoMessage() {}

func (x *Waitlist) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Waitlist.ProtoReflect.Descriptor instead.
func (*Waitlist) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{0}
}

func (x *Waitlist) GetCreatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CreatedAt
	}
	return nil
}

func (x *Waitlist) GetClosesAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ClosesAt
	}
	return nil
}

func (x *Waitlist) GetLastUpdatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.LastUpdatedAt
	}
	return nil
}

func (x *Waitlist) GetArchivedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ArchivedAt
	}
	return nil
}

func (x *Waitlist) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *Waitlist) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *Waitlist) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

// SignupSubject is who a signup belongs to, where anybody does.
//
// It is two fields rather than one composite string for the reason the tenancy
// doctrine gives about the scope: a key spelling "user:abc123" carries two facts
// in a column that can only be indexed as one. Both empty is a signup that names
// nobody, which is the ordinary case for a pre-launch list; half of one is
// refused.
type SignupSubject struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// type says what kind of principal this is. It is an opaque string with
	// "user" and "account" as the suggestions, because what kinds of thing a
	// deployment queues is the application's vocabulary -- see the file comment.
	Type string `protobuf:"bytes,1,opt,name=type,proto3" json:"type,omitempty"`
	// id identifies the principal within that type.
	Id            string `protobuf:"bytes,2,opt,name=id,proto3" json:"id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SignupSubject) Reset() {
	*x = SignupSubject{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SignupSubject) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SignupSubject) ProtoMessage() {}

func (x *SignupSubject) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SignupSubject.ProtoReflect.Descriptor instead.
func (*SignupSubject) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{1}
}

func (x *SignupSubject) GetType() string {
	if x != nil {
		return x.Type
	}
	return ""
}

func (x *SignupSubject) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

// Signup is one person's place on one list.
type Signup struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// created_at is when they joined, which is also the order they joined in.
	CreatedAt *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=created_at,json=createdAt,proto3" json:"created_at,omitempty"`
	// last_updated_at is when the row last changed, unset for one nobody has
	// touched since it was written.
	LastUpdatedAt *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=last_updated_at,json=lastUpdatedAt,proto3" json:"last_updated_at,omitempty"`
	// status_changed_at is when the signup last moved through the lifecycle,
	// unset for one still where it started.
	//
	// It is not last_updated_at, and the difference is the point: an
	// administrator fixing a typo in notes changes the row without moving
	// anybody, and the reminder that goes out three days after an invitation is
	// scheduled off this field.
	StatusChangedAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=status_changed_at,json=statusChangedAt,proto3" json:"status_changed_at,omitempty"`
	// archived_at is when the signup was retired administratively, unset while it
	// is live. It is not a withdrawal: nothing about what the row holds changes
	// and nothing is suppressed.
	ArchivedAt *timestamppb.Timestamp `protobuf:"bytes,4,opt,name=archived_at,json=archivedAt,proto3" json:"archived_at,omitempty"`
	// subject is who the signup belongs to, where anybody does. Output only: no
	// request sets it, and Join fills it from the caller.
	Subject *SignupSubject `protobuf:"bytes,5,opt,name=subject,proto3" json:"subject,omitempty"`
	// id is the row.
	Id string `protobuf:"bytes,6,opt,name=id,proto3" json:"id,omitempty"`
	// list_id is the list this signup is for. It is half of what addresses a
	// signup -- every signup-side RPC names both, so that a caller holding one
	// list's id cannot be handed another list's row.
	ListId string `protobuf:"bytes,7,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	// contact is the address the list exists to write to, as it was given. It is
	// empty for a withdrawn signup, which is the whole of what a withdrawal
	// erases from this field.
	Contact string `protobuf:"bytes,8,opt,name=contact,proto3" json:"contact,omitempty"`
	// notes is whatever whoever administers the list wrote about this signup, and
	// it is empty for a withdrawn signup. It is written by UpdateSignupNotes,
	// which is behind a grant, and never by the form.
	Notes string `protobuf:"bytes,9,opt,name=notes,proto3" json:"notes,omitempty"`
	// status is where the signup stands.
	Status        SignupStatus `protobuf:"varint,10,opt,name=status,proto3,enum=primandproper.platform.waitlists.v1.SignupStatus" json:"status,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Signup) Reset() {
	*x = Signup{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Signup) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Signup) ProtoMessage() {}

func (x *Signup) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Signup.ProtoReflect.Descriptor instead.
func (*Signup) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{2}
}

func (x *Signup) GetCreatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CreatedAt
	}
	return nil
}

func (x *Signup) GetLastUpdatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.LastUpdatedAt
	}
	return nil
}

func (x *Signup) GetStatusChangedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.StatusChangedAt
	}
	return nil
}

func (x *Signup) GetArchivedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ArchivedAt
	}
	return nil
}

func (x *Signup) GetSubject() *SignupSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

func (x *Signup) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *Signup) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *Signup) GetContact() string {
	if x != nil {
		return x.Contact
	}
	return ""
}

func (x *Signup) GetNotes() string {
	if x != nil {
		return x.Notes
	}
	return ""
}

func (x *Signup) GetStatus() SignupStatus {
	if x != nil {
		return x.Status
	}
	return SignupStatus_SIGNUP_STATUS_UNSPECIFIED
}

// WaitlistInput is what a caller supplies to open or revise a list. Everything
// else about the row -- the id on a creation, the timestamps, the scope -- is
// decided by the service.
type WaitlistInput struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// id is the list to revise, and is ignored by CreateList, which mints one.
	Id string `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
	// name is required. A list nobody can name is a list nobody can administer.
	Name        string `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
	Description string `protobuf:"bytes,3,opt,name=description,proto3" json:"description,omitempty"`
	// closes_at is required and is refused rather than defaulted: an hour is too
	// short for anything, a decade is a list nobody will ever close, and "never"
	// is the state the column deliberately cannot hold.
	ClosesAt      *timestamppb.Timestamp `protobuf:"bytes,4,opt,name=closes_at,json=closesAt,proto3" json:"closes_at,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *WaitlistInput) Reset() {
	*x = WaitlistInput{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *WaitlistInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*WaitlistInput) ProtoMessage() {}

func (x *WaitlistInput) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use WaitlistInput.ProtoReflect.Descriptor instead.
func (*WaitlistInput) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{3}
}

func (x *WaitlistInput) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *WaitlistInput) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *WaitlistInput) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *WaitlistInput) GetClosesAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ClosesAt
	}
	return nil
}

type CreateListRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	List          *WaitlistInput         `protobuf:"bytes,1,opt,name=list,proto3" json:"list,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CreateListRequest) Reset() {
	*x = CreateListRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CreateListRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CreateListRequest) ProtoMessage() {}

func (x *CreateListRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CreateListRequest.ProtoReflect.Descriptor instead.
func (*CreateListRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{4}
}

func (x *CreateListRequest) GetList() *WaitlistInput {
	if x != nil {
		return x.List
	}
	return nil
}

type CreateListResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Waitlist              `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CreateListResponse) Reset() {
	*x = CreateListResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CreateListResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CreateListResponse) ProtoMessage() {}

func (x *CreateListResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CreateListResponse.ProtoReflect.Descriptor instead.
func (*CreateListResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{5}
}

func (x *CreateListResponse) GetResult() *Waitlist {
	if x != nil {
		return x.Result
	}
	return nil
}

type GetListRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ListId        string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetListRequest) Reset() {
	*x = GetListRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetListRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetListRequest) ProtoMessage() {}

func (x *GetListRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetListRequest.ProtoReflect.Descriptor instead.
func (*GetListRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{6}
}

func (x *GetListRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

type GetListResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Waitlist              `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetListResponse) Reset() {
	*x = GetListResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetListResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetListResponse) ProtoMessage() {}

func (x *GetListResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetListResponse.ProtoReflect.Descriptor instead.
func (*GetListResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{7}
}

func (x *GetListResponse) GetResult() *Waitlist {
	if x != nil {
		return x.Result
	}
	return nil
}

type ListListsRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,1,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListListsRequest) Reset() {
	*x = ListListsRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[8]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListListsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListListsRequest) ProtoMessage() {}

func (x *ListListsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[8]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListListsRequest.ProtoReflect.Descriptor instead.
func (*ListListsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{8}
}

func (x *ListListsRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListListsResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Waitlist             `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListListsResponse) Reset() {
	*x = ListListsResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[9]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListListsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListListsResponse) ProtoMessage() {}

func (x *ListListsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[9]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListListsResponse.ProtoReflect.Descriptor instead.
func (*ListListsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{9}
}

func (x *ListListsResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListListsResponse) GetResults() []*Waitlist {
	if x != nil {
		return x.Results
	}
	return nil
}

// ListOpenListsRequest asks for the lists still taking signups. It is the one
// read on this service a caller reaches without a grant, and it is what a "join
// the waitlist" page offers.
type ListOpenListsRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,1,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListOpenListsRequest) Reset() {
	*x = ListOpenListsRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[10]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListOpenListsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListOpenListsRequest) ProtoMessage() {}

func (x *ListOpenListsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[10]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListOpenListsRequest.ProtoReflect.Descriptor instead.
func (*ListOpenListsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{10}
}

func (x *ListOpenListsRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListOpenListsResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Waitlist             `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListOpenListsResponse) Reset() {
	*x = ListOpenListsResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[11]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListOpenListsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListOpenListsResponse) ProtoMessage() {}

func (x *ListOpenListsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[11]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListOpenListsResponse.ProtoReflect.Descriptor instead.
func (*ListOpenListsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{11}
}

func (x *ListOpenListsResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListOpenListsResponse) GetResults() []*Waitlist {
	if x != nil {
		return x.Results
	}
	return nil
}

type UpdateListRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// list names the row by its id and carries what it should say. Moving
	// closes_at is how a list is extended or brought in, and it is not guarded
	// against the signups already on it: a list closed early keeps everybody who
	// joined while it was open. It will not revive an archived list.
	List          *WaitlistInput `protobuf:"bytes,1,opt,name=list,proto3" json:"list,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateListRequest) Reset() {
	*x = UpdateListRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[12]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateListRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateListRequest) ProtoMessage() {}

func (x *UpdateListRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[12]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateListRequest.ProtoReflect.Descriptor instead.
func (*UpdateListRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{12}
}

func (x *UpdateListRequest) GetList() *WaitlistInput {
	if x != nil {
		return x.List
	}
	return nil
}

// UpdateListResponse carries the list as stored, so that a client reads back
// the last_updated_at the write stamped rather than the one it sent.
type UpdateListResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Waitlist              `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateListResponse) Reset() {
	*x = UpdateListResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[13]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateListResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateListResponse) ProtoMessage() {}

func (x *UpdateListResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[13]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateListResponse.ProtoReflect.Descriptor instead.
func (*UpdateListResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{13}
}

func (x *UpdateListResponse) GetResult() *Waitlist {
	if x != nil {
		return x.Result
	}
	return nil
}

type ArchiveListRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ListId        string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveListRequest) Reset() {
	*x = ArchiveListRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[14]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveListRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveListRequest) ProtoMessage() {}

func (x *ArchiveListRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[14]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveListRequest.ProtoReflect.Descriptor instead.
func (*ArchiveListRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{14}
}

func (x *ArchiveListRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

type ArchiveListResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveListResponse) Reset() {
	*x = ArchiveListResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[15]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveListResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveListResponse) ProtoMessage() {}

func (x *ArchiveListResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[15]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveListResponse.ProtoReflect.Descriptor instead.
func (*ArchiveListResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{15}
}

// JoinRequest is the form somebody filled in.
//
// It carries an address and the list it is for, and nothing else. See the file
// comment for why it names no subject and no notes.
type JoinRequest struct {
	state  protoimpl.MessageState `protogen:"open.v1"`
	ListId string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	// contact is the address, as the person typed it. It is stored as given, so
	// that a mail client renders the capitalization they chose, and it is
	// digested trimmed and folded to lower case, so that two capitalizations of
	// one address are one person.
	Contact       string `protobuf:"bytes,2,opt,name=contact,proto3" json:"contact,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *JoinRequest) Reset() {
	*x = JoinRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[16]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *JoinRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JoinRequest) ProtoMessage() {}

func (x *JoinRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[16]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JoinRequest.ProtoReflect.Descriptor instead.
func (*JoinRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{16}
}

func (x *JoinRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *JoinRequest) GetContact() string {
	if x != nil {
		return x.Contact
	}
	return ""
}

type JoinResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Signup                `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *JoinResponse) Reset() {
	*x = JoinResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[17]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *JoinResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JoinResponse) ProtoMessage() {}

func (x *JoinResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[17]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JoinResponse.ProtoReflect.Descriptor instead.
func (*JoinResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{17}
}

func (x *JoinResponse) GetResult() *Signup {
	if x != nil {
		return x.Result
	}
	return nil
}

type GetSignupRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ListId        string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	SignupId      string                 `protobuf:"bytes,2,opt,name=signup_id,json=signupID,proto3" json:"signup_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetSignupRequest) Reset() {
	*x = GetSignupRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[18]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetSignupRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetSignupRequest) ProtoMessage() {}

func (x *GetSignupRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[18]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetSignupRequest.ProtoReflect.Descriptor instead.
func (*GetSignupRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{18}
}

func (x *GetSignupRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *GetSignupRequest) GetSignupId() string {
	if x != nil {
		return x.SignupId
	}
	return ""
}

type GetSignupResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Signup                `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetSignupResponse) Reset() {
	*x = GetSignupResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[19]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetSignupResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetSignupResponse) ProtoMessage() {}

func (x *GetSignupResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[19]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetSignupResponse.ProtoReflect.Descriptor instead.
func (*GetSignupResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{19}
}

func (x *GetSignupResponse) GetResult() *Signup {
	if x != nil {
		return x.Result
	}
	return nil
}

// GetSignupByContactRequest is the read behind "is this address on this list".
//
// It is behind a grant rather than on the public half, and that is the sharpest
// authorization decision on this service. Answering it for anybody who can reach
// the port makes the surface an oracle over which addresses are on which list --
// which is what the list holds and what a person joining one has not agreed to
// publish.
type GetSignupByContactRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ListId        string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	Contact       string                 `protobuf:"bytes,2,opt,name=contact,proto3" json:"contact,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetSignupByContactRequest) Reset() {
	*x = GetSignupByContactRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[20]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetSignupByContactRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetSignupByContactRequest) ProtoMessage() {}

func (x *GetSignupByContactRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[20]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetSignupByContactRequest.ProtoReflect.Descriptor instead.
func (*GetSignupByContactRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{20}
}

func (x *GetSignupByContactRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *GetSignupByContactRequest) GetContact() string {
	if x != nil {
		return x.Contact
	}
	return ""
}

type GetSignupByContactResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Signup                `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetSignupByContactResponse) Reset() {
	*x = GetSignupByContactResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[21]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetSignupByContactResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetSignupByContactResponse) ProtoMessage() {}

func (x *GetSignupByContactResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[21]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetSignupByContactResponse.ProtoReflect.Descriptor instead.
func (*GetSignupByContactResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{21}
}

func (x *GetSignupByContactResponse) GetResult() *Signup {
	if x != nil {
		return x.Result
	}
	return nil
}

type ListSignupsRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	ListId        string                   `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,2,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSignupsRequest) Reset() {
	*x = ListSignupsRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[22]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSignupsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSignupsRequest) ProtoMessage() {}

func (x *ListSignupsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[22]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSignupsRequest.ProtoReflect.Descriptor instead.
func (*ListSignupsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{22}
}

func (x *ListSignupsRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *ListSignupsRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListSignupsResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Signup               `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSignupsResponse) Reset() {
	*x = ListSignupsResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[23]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSignupsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSignupsResponse) ProtoMessage() {}

func (x *ListSignupsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[23]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSignupsResponse.ProtoReflect.Descriptor instead.
func (*ListSignupsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{23}
}

func (x *ListSignupsResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListSignupsResponse) GetResults() []*Signup {
	if x != nil {
		return x.Results
	}
	return nil
}

// ListSignupsForSubjectRequest pages the signups one principal holds across
// every list in the tenant. It is the read a profile page makes and the one a
// privacy export walks; the filter's include_archived is what an export sets.
//
// A withdrawn signup is never among them: a withdrawal blanks the subject along
// with the contact, so the row that remembers a suppression no longer says whose
// it was.
type ListSignupsForSubjectRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	Subject       *SignupSubject           `protobuf:"bytes,1,opt,name=subject,proto3" json:"subject,omitempty"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,2,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSignupsForSubjectRequest) Reset() {
	*x = ListSignupsForSubjectRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[24]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSignupsForSubjectRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSignupsForSubjectRequest) ProtoMessage() {}

func (x *ListSignupsForSubjectRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[24]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSignupsForSubjectRequest.ProtoReflect.Descriptor instead.
func (*ListSignupsForSubjectRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{24}
}

func (x *ListSignupsForSubjectRequest) GetSubject() *SignupSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

func (x *ListSignupsForSubjectRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListSignupsForSubjectResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Signup               `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSignupsForSubjectResponse) Reset() {
	*x = ListSignupsForSubjectResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[25]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSignupsForSubjectResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSignupsForSubjectResponse) ProtoMessage() {}

func (x *ListSignupsForSubjectResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[25]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSignupsForSubjectResponse.ProtoReflect.Descriptor instead.
func (*ListSignupsForSubjectResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{25}
}

func (x *ListSignupsForSubjectResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListSignupsForSubjectResponse) GetResults() []*Signup {
	if x != nil {
		return x.Results
	}
	return nil
}

// UpdateSignupNotesRequest rewrites the operator's note against a signup.
//
// It deliberately does not move the signup: a typo fixed in a note must not
// reschedule the reminder somebody's invitation started, so status_changed_at is
// left alone.
type UpdateSignupNotesRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ListId        string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	SignupId      string                 `protobuf:"bytes,2,opt,name=signup_id,json=signupID,proto3" json:"signup_id,omitempty"`
	Notes         string                 `protobuf:"bytes,3,opt,name=notes,proto3" json:"notes,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateSignupNotesRequest) Reset() {
	*x = UpdateSignupNotesRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[26]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateSignupNotesRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateSignupNotesRequest) ProtoMessage() {}

func (x *UpdateSignupNotesRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[26]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateSignupNotesRequest.ProtoReflect.Descriptor instead.
func (*UpdateSignupNotesRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{26}
}

func (x *UpdateSignupNotesRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *UpdateSignupNotesRequest) GetSignupId() string {
	if x != nil {
		return x.SignupId
	}
	return ""
}

func (x *UpdateSignupNotesRequest) GetNotes() string {
	if x != nil {
		return x.Notes
	}
	return ""
}

type UpdateSignupNotesResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Signup                `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateSignupNotesResponse) Reset() {
	*x = UpdateSignupNotesResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[27]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateSignupNotesResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateSignupNotesResponse) ProtoMessage() {}

func (x *UpdateSignupNotesResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[27]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateSignupNotesResponse.ProtoReflect.Descriptor instead.
func (*UpdateSignupNotesResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{27}
}

func (x *UpdateSignupNotesResponse) GetResult() *Signup {
	if x != nil {
		return x.Result
	}
	return nil
}

// InviteRequest moves a waiting signup to invited and stamps the moment.
//
// Anything that is not waiting is refused, and the refusal is the affected-row
// count of a guarded update rather than a decision made on a read -- so two
// requests inviting the same person send one email between them.
type InviteRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ListId        string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	SignupId      string                 `protobuf:"bytes,2,opt,name=signup_id,json=signupID,proto3" json:"signup_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *InviteRequest) Reset() {
	*x = InviteRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[28]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *InviteRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*InviteRequest) ProtoMessage() {}

func (x *InviteRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[28]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use InviteRequest.ProtoReflect.Descriptor instead.
func (*InviteRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{28}
}

func (x *InviteRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *InviteRequest) GetSignupId() string {
	if x != nil {
		return x.SignupId
	}
	return ""
}

// InviteResponse carries the signup as it now stands, because status_changed_at
// is the field the reminder is scheduled off and this is the call that set it.
type InviteResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Signup                `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *InviteResponse) Reset() {
	*x = InviteResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[29]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *InviteResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*InviteResponse) ProtoMessage() {}

func (x *InviteResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[29]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use InviteResponse.ProtoReflect.Descriptor instead.
func (*InviteResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{29}
}

func (x *InviteResponse) GetResult() *Signup {
	if x != nil {
		return x.Result
	}
	return nil
}

type ConvertRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ListId        string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	SignupId      string                 `protobuf:"bytes,2,opt,name=signup_id,json=signupID,proto3" json:"signup_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ConvertRequest) Reset() {
	*x = ConvertRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[30]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ConvertRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConvertRequest) ProtoMessage() {}

func (x *ConvertRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[30]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConvertRequest.ProtoReflect.Descriptor instead.
func (*ConvertRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{30}
}

func (x *ConvertRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *ConvertRequest) GetSignupId() string {
	if x != nil {
		return x.SignupId
	}
	return ""
}

type ConvertResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Signup                `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ConvertResponse) Reset() {
	*x = ConvertResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[31]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ConvertResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConvertResponse) ProtoMessage() {}

func (x *ConvertResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[31]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConvertResponse.ProtoReflect.Descriptor instead.
func (*ConvertResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{31}
}

func (x *ConvertResponse) GetResult() *Signup {
	if x != nil {
		return x.Result
	}
	return nil
}

// WithdrawRequest is somebody asking to come off a list, at their own request.
//
// It is the second of the three RPCs a caller reaches without a grant, and it is
// the one that names a row. A grant on the method could not have said whose row
// this is, and neither can a signup identifier, which is minted by the store and
// is not a credential -- so the standing to withdraw this signup is asked of the
// consumer's own [waitlists/grpc.SignupAuthorizer], from inside the handler,
// before anything is written. An unsubscribe link redeemed through
// platform-go/links is the shape that answer usually takes.
type WithdrawRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ListId        string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	SignupId      string                 `protobuf:"bytes,2,opt,name=signup_id,json=signupID,proto3" json:"signup_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *WithdrawRequest) Reset() {
	*x = WithdrawRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[32]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *WithdrawRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*WithdrawRequest) ProtoMessage() {}

func (x *WithdrawRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[32]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use WithdrawRequest.ProtoReflect.Descriptor instead.
func (*WithdrawRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{32}
}

func (x *WithdrawRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *WithdrawRequest) GetSignupId() string {
	if x != nil {
		return x.SignupId
	}
	return ""
}

// WithdrawResponse is empty, and carries no signup on purpose. What is left of
// the row after a withdrawal is a status and a digest, and the caller who just
// asked to be forgotten is not the caller to hand it to.
type WithdrawResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *WithdrawResponse) Reset() {
	*x = WithdrawResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[33]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *WithdrawResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*WithdrawResponse) ProtoMessage() {}

func (x *WithdrawResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[33]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use WithdrawResponse.ProtoReflect.Descriptor instead.
func (*WithdrawResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{33}
}

// WithdrawSignupsForSubjectRequest withdraws every signup one principal holds in
// the tenant, archived signups included, and is the erasure path.
//
// It is a withdrawal rather than a delete for the reason a single withdrawal is:
// a delete frees the unique key, so somebody erased at their own request could
// be re-subscribed by the next form submission, which is the opposite of an
// erasure.
//
// A subject naming nobody is refused rather than answered -- bound to nobody,
// the statement would withdraw every signup nobody claimed.
type WithdrawSignupsForSubjectRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Subject       *SignupSubject         `protobuf:"bytes,1,opt,name=subject,proto3" json:"subject,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *WithdrawSignupsForSubjectRequest) Reset() {
	*x = WithdrawSignupsForSubjectRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[34]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *WithdrawSignupsForSubjectRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*WithdrawSignupsForSubjectRequest) ProtoMessage() {}

func (x *WithdrawSignupsForSubjectRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[34]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use WithdrawSignupsForSubjectRequest.ProtoReflect.Descriptor instead.
func (*WithdrawSignupsForSubjectRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{34}
}

func (x *WithdrawSignupsForSubjectRequest) GetSubject() *SignupSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

type WithdrawSignupsForSubjectResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// withdrawn is how many rows moved. Zero is not an error: a person who never
	// joined a list is a person with nothing here to erase.
	Withdrawn     int64 `protobuf:"varint,1,opt,name=withdrawn,proto3" json:"withdrawn,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *WithdrawSignupsForSubjectResponse) Reset() {
	*x = WithdrawSignupsForSubjectResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[35]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *WithdrawSignupsForSubjectResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*WithdrawSignupsForSubjectResponse) ProtoMessage() {}

func (x *WithdrawSignupsForSubjectResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[35]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use WithdrawSignupsForSubjectResponse.ProtoReflect.Descriptor instead.
func (*WithdrawSignupsForSubjectResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{35}
}

func (x *WithdrawSignupsForSubjectResponse) GetWithdrawn() int64 {
	if x != nil {
		return x.Withdrawn
	}
	return 0
}

// ArchiveSignupRequest retires a signup administratively.
//
// It is not a withdrawal and must not be used as one. It hides the row and
// changes nothing about what it holds, so the contact is still stored, nothing
// is suppressed, and the uniqueness still covers the row -- the next attempt
// from that address gets "already signed up". Somebody asking to come off a list
// wants Withdraw.
type ArchiveSignupRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ListId        string                 `protobuf:"bytes,1,opt,name=list_id,json=listID,proto3" json:"list_id,omitempty"`
	SignupId      string                 `protobuf:"bytes,2,opt,name=signup_id,json=signupID,proto3" json:"signup_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveSignupRequest) Reset() {
	*x = ArchiveSignupRequest{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[36]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveSignupRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveSignupRequest) ProtoMessage() {}

func (x *ArchiveSignupRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[36]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveSignupRequest.ProtoReflect.Descriptor instead.
func (*ArchiveSignupRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{36}
}

func (x *ArchiveSignupRequest) GetListId() string {
	if x != nil {
		return x.ListId
	}
	return ""
}

func (x *ArchiveSignupRequest) GetSignupId() string {
	if x != nil {
		return x.SignupId
	}
	return ""
}

type ArchiveSignupResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveSignupResponse) Reset() {
	*x = ArchiveSignupResponse{}
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[37]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveSignupResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveSignupResponse) ProtoMessage() {}

func (x *ArchiveSignupResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes[37]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveSignupResponse.ProtoReflect.Descriptor instead.
func (*ArchiveSignupResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP(), []int{37}
}

var File_primandproper_platform_waitlists_v1_waitlists_proto protoreflect.FileDescriptor

const file_primandproper_platform_waitlists_v1_waitlists_proto_rawDesc = "" +
	"\n" +
	"3primandproper/platform/waitlists/v1/waitlists.proto\x12#primandproper.platform.waitlists.v1\x1a\x1fgoogle/protobuf/timestamp.proto\x1a3primandproper/platform/filtering/v1/filtering.proto\"\xcc\x02\n" +
	"\bWaitlist\x129\n" +
	"\n" +
	"created_at\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\tcreatedAt\x127\n" +
	"\tcloses_at\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\bclosesAt\x12B\n" +
	"\x0flast_updated_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\rlastUpdatedAt\x12;\n" +
	"\varchived_at\x18\x04 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"archivedAt\x12\x0e\n" +
	"\x02id\x18\x05 \x01(\tR\x02id\x12\x12\n" +
	"\x04name\x18\x06 \x01(\tR\x04name\x12 \n" +
	"\vdescription\x18\a \x01(\tR\vdescriptionR\x05scope\":\n" +
	"\rSignupSubject\x12\x12\n" +
	"\x04type\x18\x01 \x01(\tR\x04type\x12\x0e\n" +
	"\x02id\x18\x02 \x01(\tR\x02idR\x05scope\"\x95\x04\n" +
	"\x06Signup\x129\n" +
	"\n" +
	"created_at\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\tcreatedAt\x12B\n" +
	"\x0flast_updated_at\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\rlastUpdatedAt\x12F\n" +
	"\x11status_changed_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\x0fstatusChangedAt\x12;\n" +
	"\varchived_at\x18\x04 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"archivedAt\x12L\n" +
	"\asubject\x18\x05 \x01(\v22.primandproper.platform.waitlists.v1.SignupSubjectR\asubject\x12\x0e\n" +
	"\x02id\x18\x06 \x01(\tR\x02id\x12\x17\n" +
	"\alist_id\x18\a \x01(\tR\x06listID\x12\x18\n" +
	"\acontact\x18\b \x01(\tR\acontact\x12\x14\n" +
	"\x05notes\x18\t \x01(\tR\x05notes\x12I\n" +
	"\x06status\x18\n" +
	" \x01(\x0e21.primandproper.platform.waitlists.v1.SignupStatusR\x06statusR\x05scopeR\x0econtact_digest\"\x95\x01\n" +
	"\rWaitlistInput\x12\x0e\n" +
	"\x02id\x18\x01 \x01(\tR\x02id\x12\x12\n" +
	"\x04name\x18\x02 \x01(\tR\x04name\x12 \n" +
	"\vdescription\x18\x03 \x01(\tR\vdescription\x127\n" +
	"\tcloses_at\x18\x04 \x01(\v2\x1a.google.protobuf.TimestampR\bclosesAtR\x05scope\"b\n" +
	"\x11CreateListRequest\x12F\n" +
	"\x04list\x18\x01 \x01(\v22.primandproper.platform.waitlists.v1.WaitlistInputR\x04listR\x05scope\"[\n" +
	"\x12CreateListResponse\x12E\n" +
	"\x06result\x18\x01 \x01(\v2-.primandproper.platform.waitlists.v1.WaitlistR\x06result\"0\n" +
	"\x0eGetListRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listIDR\x05scope\"X\n" +
	"\x0fGetListResponse\x12E\n" +
	"\x06result\x18\x01 \x01(\v2-.primandproper.platform.waitlists.v1.WaitlistR\x06result\"c\n" +
	"\x10ListListsRequest\x12H\n" +
	"\x06filter\x18\x01 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xad\x01\n" +
	"\x11ListListsResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12G\n" +
	"\aresults\x18\x02 \x03(\v2-.primandproper.platform.waitlists.v1.WaitlistR\aresults\"g\n" +
	"\x14ListOpenListsRequest\x12H\n" +
	"\x06filter\x18\x01 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xb1\x01\n" +
	"\x15ListOpenListsResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12G\n" +
	"\aresults\x18\x02 \x03(\v2-.primandproper.platform.waitlists.v1.WaitlistR\aresults\"b\n" +
	"\x11UpdateListRequest\x12F\n" +
	"\x04list\x18\x01 \x01(\v22.primandproper.platform.waitlists.v1.WaitlistInputR\x04listR\x05scope\"[\n" +
	"\x12UpdateListResponse\x12E\n" +
	"\x06result\x18\x01 \x01(\v2-.primandproper.platform.waitlists.v1.WaitlistR\x06result\"4\n" +
	"\x12ArchiveListRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listIDR\x05scope\"\x15\n" +
	"\x13ArchiveListResponse\"W\n" +
	"\vJoinRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listID\x12\x18\n" +
	"\acontact\x18\x02 \x01(\tR\acontactR\x05scopeR\asubjectR\x05notes\"S\n" +
	"\fJoinResponse\x12C\n" +
	"\x06result\x18\x01 \x01(\v2+.primandproper.platform.waitlists.v1.SignupR\x06result\"O\n" +
	"\x10GetSignupRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listID\x12\x1b\n" +
	"\tsignup_id\x18\x02 \x01(\tR\bsignupIDR\x05scope\"X\n" +
	"\x11GetSignupResponse\x12C\n" +
	"\x06result\x18\x01 \x01(\v2+.primandproper.platform.waitlists.v1.SignupR\x06result\"U\n" +
	"\x19GetSignupByContactRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listID\x12\x18\n" +
	"\acontact\x18\x02 \x01(\tR\acontactR\x05scope\"a\n" +
	"\x1aGetSignupByContactResponse\x12C\n" +
	"\x06result\x18\x01 \x01(\v2+.primandproper.platform.waitlists.v1.SignupR\x06result\"~\n" +
	"\x12ListSignupsRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listID\x12H\n" +
	"\x06filter\x18\x02 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xad\x01\n" +
	"\x13ListSignupsResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12E\n" +
	"\aresults\x18\x02 \x03(\v2+.primandproper.platform.waitlists.v1.SignupR\aresults\"\xbd\x01\n" +
	"\x1cListSignupsForSubjectRequest\x12L\n" +
	"\asubject\x18\x01 \x01(\v22.primandproper.platform.waitlists.v1.SignupSubjectR\asubject\x12H\n" +
	"\x06filter\x18\x02 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xb7\x01\n" +
	"\x1dListSignupsForSubjectResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12E\n" +
	"\aresults\x18\x02 \x03(\v2+.primandproper.platform.waitlists.v1.SignupR\aresults\"m\n" +
	"\x18UpdateSignupNotesRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listID\x12\x1b\n" +
	"\tsignup_id\x18\x02 \x01(\tR\bsignupID\x12\x14\n" +
	"\x05notes\x18\x03 \x01(\tR\x05notesR\x05scope\"`\n" +
	"\x19UpdateSignupNotesResponse\x12C\n" +
	"\x06result\x18\x01 \x01(\v2+.primandproper.platform.waitlists.v1.SignupR\x06result\"L\n" +
	"\rInviteRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listID\x12\x1b\n" +
	"\tsignup_id\x18\x02 \x01(\tR\bsignupIDR\x05scope\"U\n" +
	"\x0eInviteResponse\x12C\n" +
	"\x06result\x18\x01 \x01(\v2+.primandproper.platform.waitlists.v1.SignupR\x06result\"M\n" +
	"\x0eConvertRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listID\x12\x1b\n" +
	"\tsignup_id\x18\x02 \x01(\tR\bsignupIDR\x05scope\"V\n" +
	"\x0fConvertResponse\x12C\n" +
	"\x06result\x18\x01 \x01(\v2+.primandproper.platform.waitlists.v1.SignupR\x06result\"N\n" +
	"\x0fWithdrawRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listID\x12\x1b\n" +
	"\tsignup_id\x18\x02 \x01(\tR\bsignupIDR\x05scope\"\x12\n" +
	"\x10WithdrawResponse\"w\n" +
	" WithdrawSignupsForSubjectRequest\x12L\n" +
	"\asubject\x18\x01 \x01(\v22.primandproper.platform.waitlists.v1.SignupSubjectR\asubjectR\x05scope\"A\n" +
	"!WithdrawSignupsForSubjectResponse\x12\x1c\n" +
	"\twithdrawn\x18\x01 \x01(\x03R\twithdrawn\"S\n" +
	"\x14ArchiveSignupRequest\x12\x17\n" +
	"\alist_id\x18\x01 \x01(\tR\x06listID\x12\x1b\n" +
	"\tsignup_id\x18\x02 \x01(\tR\bsignupIDR\x05scope\"\x17\n" +
	"\x15ArchiveSignupResponse*\x9d\x01\n" +
	"\fSignupStatus\x12\x1d\n" +
	"\x19SIGNUP_STATUS_UNSPECIFIED\x10\x00\x12\x19\n" +
	"\x15SIGNUP_STATUS_WAITING\x10\x01\x12\x19\n" +
	"\x15SIGNUP_STATUS_INVITED\x10\x02\x12\x1b\n" +
	"\x17SIGNUP_STATUS_CONVERTED\x10\x03\x12\x1b\n" +
	"\x17SIGNUP_STATUS_WITHDRAWN\x10\x042\xe0\x11\n" +
	"\x10WaitlistsService\x12}\n" +
	"\n" +
	"CreateList\x126.primandproper.platform.waitlists.v1.CreateListRequest\x1a7.primandproper.platform.waitlists.v1.CreateListResponse\x12t\n" +
	"\aGetList\x123.primandproper.platform.waitlists.v1.GetListRequest\x1a4.primandproper.platform.waitlists.v1.GetListResponse\x12z\n" +
	"\tListLists\x125.primandproper.platform.waitlists.v1.ListListsRequest\x1a6.primandproper.platform.waitlists.v1.ListListsResponse\x12\x86\x01\n" +
	"\rListOpenLists\x129.primandproper.platform.waitlists.v1.ListOpenListsRequest\x1a:.primandproper.platform.waitlists.v1.ListOpenListsResponse\x12}\n" +
	"\n" +
	"UpdateList\x126.primandproper.platform.waitlists.v1.UpdateListRequest\x1a7.primandproper.platform.waitlists.v1.UpdateListResponse\x12\x80\x01\n" +
	"\vArchiveList\x127.primandproper.platform.waitlists.v1.ArchiveListRequest\x1a8.primandproper.platform.waitlists.v1.ArchiveListResponse\x12k\n" +
	"\x04Join\x120.primandproper.platform.waitlists.v1.JoinRequest\x1a1.primandproper.platform.waitlists.v1.JoinResponse\x12z\n" +
	"\tGetSignup\x125.primandproper.platform.waitlists.v1.GetSignupRequest\x1a6.primandproper.platform.waitlists.v1.GetSignupResponse\x12\x95\x01\n" +
	"\x12GetSignupByContact\x12>.primandproper.platform.waitlists.v1.GetSignupByContactRequest\x1a?.primandproper.platform.waitlists.v1.GetSignupByContactResponse\x12\x80\x01\n" +
	"\vListSignups\x127.primandproper.platform.waitlists.v1.ListSignupsRequest\x1a8.primandproper.platform.waitlists.v1.ListSignupsResponse\x12\x9e\x01\n" +
	"\x15ListSignupsForSubject\x12A.primandproper.platform.waitlists.v1.ListSignupsForSubjectRequest\x1aB.primandproper.platform.waitlists.v1.ListSignupsForSubjectResponse\x12\x92\x01\n" +
	"\x11UpdateSignupNotes\x12=.primandproper.platform.waitlists.v1.UpdateSignupNotesRequest\x1a>.primandproper.platform.waitlists.v1.UpdateSignupNotesResponse\x12q\n" +
	"\x06Invite\x122.primandproper.platform.waitlists.v1.InviteRequest\x1a3.primandproper.platform.waitlists.v1.InviteResponse\x12t\n" +
	"\aConvert\x123.primandproper.platform.waitlists.v1.ConvertRequest\x1a4.primandproper.platform.waitlists.v1.ConvertResponse\x12w\n" +
	"\bWithdraw\x124.primandproper.platform.waitlists.v1.WithdrawRequest\x1a5.primandproper.platform.waitlists.v1.WithdrawResponse\x12\xaa\x01\n" +
	"\x19WithdrawSignupsForSubject\x12E.primandproper.platform.waitlists.v1.WithdrawSignupsForSubjectRequest\x1aF.primandproper.platform.waitlists.v1.WithdrawSignupsForSubjectResponse\x12\x86\x01\n" +
	"\rArchiveSignup\x129.primandproper.platform.waitlists.v1.ArchiveSignupRequest\x1a:.primandproper.platform.waitlists.v1.ArchiveSignupResponseBLZJgithub.com/primandproper/platform-go/v14/waitlists/waitlistspb;waitlistspbb\x06proto3"

var (
	file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescOnce sync.Once
	file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescData []byte
)

func file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescGZIP() []byte {
	file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescOnce.Do(func() {
		file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_primandproper_platform_waitlists_v1_waitlists_proto_rawDesc), len(file_primandproper_platform_waitlists_v1_waitlists_proto_rawDesc)))
	})
	return file_primandproper_platform_waitlists_v1_waitlists_proto_rawDescData
}

var file_primandproper_platform_waitlists_v1_waitlists_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes = make([]protoimpl.MessageInfo, 38)
var file_primandproper_platform_waitlists_v1_waitlists_proto_goTypes = []any{
	(SignupStatus)(0),                         // 0: primandproper.platform.waitlists.v1.SignupStatus
	(*Waitlist)(nil),                          // 1: primandproper.platform.waitlists.v1.Waitlist
	(*SignupSubject)(nil),                     // 2: primandproper.platform.waitlists.v1.SignupSubject
	(*Signup)(nil),                            // 3: primandproper.platform.waitlists.v1.Signup
	(*WaitlistInput)(nil),                     // 4: primandproper.platform.waitlists.v1.WaitlistInput
	(*CreateListRequest)(nil),                 // 5: primandproper.platform.waitlists.v1.CreateListRequest
	(*CreateListResponse)(nil),                // 6: primandproper.platform.waitlists.v1.CreateListResponse
	(*GetListRequest)(nil),                    // 7: primandproper.platform.waitlists.v1.GetListRequest
	(*GetListResponse)(nil),                   // 8: primandproper.platform.waitlists.v1.GetListResponse
	(*ListListsRequest)(nil),                  // 9: primandproper.platform.waitlists.v1.ListListsRequest
	(*ListListsResponse)(nil),                 // 10: primandproper.platform.waitlists.v1.ListListsResponse
	(*ListOpenListsRequest)(nil),              // 11: primandproper.platform.waitlists.v1.ListOpenListsRequest
	(*ListOpenListsResponse)(nil),             // 12: primandproper.platform.waitlists.v1.ListOpenListsResponse
	(*UpdateListRequest)(nil),                 // 13: primandproper.platform.waitlists.v1.UpdateListRequest
	(*UpdateListResponse)(nil),                // 14: primandproper.platform.waitlists.v1.UpdateListResponse
	(*ArchiveListRequest)(nil),                // 15: primandproper.platform.waitlists.v1.ArchiveListRequest
	(*ArchiveListResponse)(nil),               // 16: primandproper.platform.waitlists.v1.ArchiveListResponse
	(*JoinRequest)(nil),                       // 17: primandproper.platform.waitlists.v1.JoinRequest
	(*JoinResponse)(nil),                      // 18: primandproper.platform.waitlists.v1.JoinResponse
	(*GetSignupRequest)(nil),                  // 19: primandproper.platform.waitlists.v1.GetSignupRequest
	(*GetSignupResponse)(nil),                 // 20: primandproper.platform.waitlists.v1.GetSignupResponse
	(*GetSignupByContactRequest)(nil),         // 21: primandproper.platform.waitlists.v1.GetSignupByContactRequest
	(*GetSignupByContactResponse)(nil),        // 22: primandproper.platform.waitlists.v1.GetSignupByContactResponse
	(*ListSignupsRequest)(nil),                // 23: primandproper.platform.waitlists.v1.ListSignupsRequest
	(*ListSignupsResponse)(nil),               // 24: primandproper.platform.waitlists.v1.ListSignupsResponse
	(*ListSignupsForSubjectRequest)(nil),      // 25: primandproper.platform.waitlists.v1.ListSignupsForSubjectRequest
	(*ListSignupsForSubjectResponse)(nil),     // 26: primandproper.platform.waitlists.v1.ListSignupsForSubjectResponse
	(*UpdateSignupNotesRequest)(nil),          // 27: primandproper.platform.waitlists.v1.UpdateSignupNotesRequest
	(*UpdateSignupNotesResponse)(nil),         // 28: primandproper.platform.waitlists.v1.UpdateSignupNotesResponse
	(*InviteRequest)(nil),                     // 29: primandproper.platform.waitlists.v1.InviteRequest
	(*InviteResponse)(nil),                    // 30: primandproper.platform.waitlists.v1.InviteResponse
	(*ConvertRequest)(nil),                    // 31: primandproper.platform.waitlists.v1.ConvertRequest
	(*ConvertResponse)(nil),                   // 32: primandproper.platform.waitlists.v1.ConvertResponse
	(*WithdrawRequest)(nil),                   // 33: primandproper.platform.waitlists.v1.WithdrawRequest
	(*WithdrawResponse)(nil),                  // 34: primandproper.platform.waitlists.v1.WithdrawResponse
	(*WithdrawSignupsForSubjectRequest)(nil),  // 35: primandproper.platform.waitlists.v1.WithdrawSignupsForSubjectRequest
	(*WithdrawSignupsForSubjectResponse)(nil), // 36: primandproper.platform.waitlists.v1.WithdrawSignupsForSubjectResponse
	(*ArchiveSignupRequest)(nil),              // 37: primandproper.platform.waitlists.v1.ArchiveSignupRequest
	(*ArchiveSignupResponse)(nil),             // 38: primandproper.platform.waitlists.v1.ArchiveSignupResponse
	(*timestamppb.Timestamp)(nil),             // 39: google.protobuf.Timestamp
	(*filteringpb.QueryFilter)(nil),           // 40: primandproper.platform.filtering.v1.QueryFilter
	(*filteringpb.Pagination)(nil),            // 41: primandproper.platform.filtering.v1.Pagination
}
var file_primandproper_platform_waitlists_v1_waitlists_proto_depIdxs = []int32{
	39, // 0: primandproper.platform.waitlists.v1.Waitlist.created_at:type_name -> google.protobuf.Timestamp
	39, // 1: primandproper.platform.waitlists.v1.Waitlist.closes_at:type_name -> google.protobuf.Timestamp
	39, // 2: primandproper.platform.waitlists.v1.Waitlist.last_updated_at:type_name -> google.protobuf.Timestamp
	39, // 3: primandproper.platform.waitlists.v1.Waitlist.archived_at:type_name -> google.protobuf.Timestamp
	39, // 4: primandproper.platform.waitlists.v1.Signup.created_at:type_name -> google.protobuf.Timestamp
	39, // 5: primandproper.platform.waitlists.v1.Signup.last_updated_at:type_name -> google.protobuf.Timestamp
	39, // 6: primandproper.platform.waitlists.v1.Signup.status_changed_at:type_name -> google.protobuf.Timestamp
	39, // 7: primandproper.platform.waitlists.v1.Signup.archived_at:type_name -> google.protobuf.Timestamp
	2,  // 8: primandproper.platform.waitlists.v1.Signup.subject:type_name -> primandproper.platform.waitlists.v1.SignupSubject
	0,  // 9: primandproper.platform.waitlists.v1.Signup.status:type_name -> primandproper.platform.waitlists.v1.SignupStatus
	39, // 10: primandproper.platform.waitlists.v1.WaitlistInput.closes_at:type_name -> google.protobuf.Timestamp
	4,  // 11: primandproper.platform.waitlists.v1.CreateListRequest.list:type_name -> primandproper.platform.waitlists.v1.WaitlistInput
	1,  // 12: primandproper.platform.waitlists.v1.CreateListResponse.result:type_name -> primandproper.platform.waitlists.v1.Waitlist
	1,  // 13: primandproper.platform.waitlists.v1.GetListResponse.result:type_name -> primandproper.platform.waitlists.v1.Waitlist
	40, // 14: primandproper.platform.waitlists.v1.ListListsRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	41, // 15: primandproper.platform.waitlists.v1.ListListsResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	1,  // 16: primandproper.platform.waitlists.v1.ListListsResponse.results:type_name -> primandproper.platform.waitlists.v1.Waitlist
	40, // 17: primandproper.platform.waitlists.v1.ListOpenListsRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	41, // 18: primandproper.platform.waitlists.v1.ListOpenListsResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	1,  // 19: primandproper.platform.waitlists.v1.ListOpenListsResponse.results:type_name -> primandproper.platform.waitlists.v1.Waitlist
	4,  // 20: primandproper.platform.waitlists.v1.UpdateListRequest.list:type_name -> primandproper.platform.waitlists.v1.WaitlistInput
	1,  // 21: primandproper.platform.waitlists.v1.UpdateListResponse.result:type_name -> primandproper.platform.waitlists.v1.Waitlist
	3,  // 22: primandproper.platform.waitlists.v1.JoinResponse.result:type_name -> primandproper.platform.waitlists.v1.Signup
	3,  // 23: primandproper.platform.waitlists.v1.GetSignupResponse.result:type_name -> primandproper.platform.waitlists.v1.Signup
	3,  // 24: primandproper.platform.waitlists.v1.GetSignupByContactResponse.result:type_name -> primandproper.platform.waitlists.v1.Signup
	40, // 25: primandproper.platform.waitlists.v1.ListSignupsRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	41, // 26: primandproper.platform.waitlists.v1.ListSignupsResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	3,  // 27: primandproper.platform.waitlists.v1.ListSignupsResponse.results:type_name -> primandproper.platform.waitlists.v1.Signup
	2,  // 28: primandproper.platform.waitlists.v1.ListSignupsForSubjectRequest.subject:type_name -> primandproper.platform.waitlists.v1.SignupSubject
	40, // 29: primandproper.platform.waitlists.v1.ListSignupsForSubjectRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	41, // 30: primandproper.platform.waitlists.v1.ListSignupsForSubjectResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	3,  // 31: primandproper.platform.waitlists.v1.ListSignupsForSubjectResponse.results:type_name -> primandproper.platform.waitlists.v1.Signup
	3,  // 32: primandproper.platform.waitlists.v1.UpdateSignupNotesResponse.result:type_name -> primandproper.platform.waitlists.v1.Signup
	3,  // 33: primandproper.platform.waitlists.v1.InviteResponse.result:type_name -> primandproper.platform.waitlists.v1.Signup
	3,  // 34: primandproper.platform.waitlists.v1.ConvertResponse.result:type_name -> primandproper.platform.waitlists.v1.Signup
	2,  // 35: primandproper.platform.waitlists.v1.WithdrawSignupsForSubjectRequest.subject:type_name -> primandproper.platform.waitlists.v1.SignupSubject
	5,  // 36: primandproper.platform.waitlists.v1.WaitlistsService.CreateList:input_type -> primandproper.platform.waitlists.v1.CreateListRequest
	7,  // 37: primandproper.platform.waitlists.v1.WaitlistsService.GetList:input_type -> primandproper.platform.waitlists.v1.GetListRequest
	9,  // 38: primandproper.platform.waitlists.v1.WaitlistsService.ListLists:input_type -> primandproper.platform.waitlists.v1.ListListsRequest
	11, // 39: primandproper.platform.waitlists.v1.WaitlistsService.ListOpenLists:input_type -> primandproper.platform.waitlists.v1.ListOpenListsRequest
	13, // 40: primandproper.platform.waitlists.v1.WaitlistsService.UpdateList:input_type -> primandproper.platform.waitlists.v1.UpdateListRequest
	15, // 41: primandproper.platform.waitlists.v1.WaitlistsService.ArchiveList:input_type -> primandproper.platform.waitlists.v1.ArchiveListRequest
	17, // 42: primandproper.platform.waitlists.v1.WaitlistsService.Join:input_type -> primandproper.platform.waitlists.v1.JoinRequest
	19, // 43: primandproper.platform.waitlists.v1.WaitlistsService.GetSignup:input_type -> primandproper.platform.waitlists.v1.GetSignupRequest
	21, // 44: primandproper.platform.waitlists.v1.WaitlistsService.GetSignupByContact:input_type -> primandproper.platform.waitlists.v1.GetSignupByContactRequest
	23, // 45: primandproper.platform.waitlists.v1.WaitlistsService.ListSignups:input_type -> primandproper.platform.waitlists.v1.ListSignupsRequest
	25, // 46: primandproper.platform.waitlists.v1.WaitlistsService.ListSignupsForSubject:input_type -> primandproper.platform.waitlists.v1.ListSignupsForSubjectRequest
	27, // 47: primandproper.platform.waitlists.v1.WaitlistsService.UpdateSignupNotes:input_type -> primandproper.platform.waitlists.v1.UpdateSignupNotesRequest
	29, // 48: primandproper.platform.waitlists.v1.WaitlistsService.Invite:input_type -> primandproper.platform.waitlists.v1.InviteRequest
	31, // 49: primandproper.platform.waitlists.v1.WaitlistsService.Convert:input_type -> primandproper.platform.waitlists.v1.ConvertRequest
	33, // 50: primandproper.platform.waitlists.v1.WaitlistsService.Withdraw:input_type -> primandproper.platform.waitlists.v1.WithdrawRequest
	35, // 51: primandproper.platform.waitlists.v1.WaitlistsService.WithdrawSignupsForSubject:input_type -> primandproper.platform.waitlists.v1.WithdrawSignupsForSubjectRequest
	37, // 52: primandproper.platform.waitlists.v1.WaitlistsService.ArchiveSignup:input_type -> primandproper.platform.waitlists.v1.ArchiveSignupRequest
	6,  // 53: primandproper.platform.waitlists.v1.WaitlistsService.CreateList:output_type -> primandproper.platform.waitlists.v1.CreateListResponse
	8,  // 54: primandproper.platform.waitlists.v1.WaitlistsService.GetList:output_type -> primandproper.platform.waitlists.v1.GetListResponse
	10, // 55: primandproper.platform.waitlists.v1.WaitlistsService.ListLists:output_type -> primandproper.platform.waitlists.v1.ListListsResponse
	12, // 56: primandproper.platform.waitlists.v1.WaitlistsService.ListOpenLists:output_type -> primandproper.platform.waitlists.v1.ListOpenListsResponse
	14, // 57: primandproper.platform.waitlists.v1.WaitlistsService.UpdateList:output_type -> primandproper.platform.waitlists.v1.UpdateListResponse
	16, // 58: primandproper.platform.waitlists.v1.WaitlistsService.ArchiveList:output_type -> primandproper.platform.waitlists.v1.ArchiveListResponse
	18, // 59: primandproper.platform.waitlists.v1.WaitlistsService.Join:output_type -> primandproper.platform.waitlists.v1.JoinResponse
	20, // 60: primandproper.platform.waitlists.v1.WaitlistsService.GetSignup:output_type -> primandproper.platform.waitlists.v1.GetSignupResponse
	22, // 61: primandproper.platform.waitlists.v1.WaitlistsService.GetSignupByContact:output_type -> primandproper.platform.waitlists.v1.GetSignupByContactResponse
	24, // 62: primandproper.platform.waitlists.v1.WaitlistsService.ListSignups:output_type -> primandproper.platform.waitlists.v1.ListSignupsResponse
	26, // 63: primandproper.platform.waitlists.v1.WaitlistsService.ListSignupsForSubject:output_type -> primandproper.platform.waitlists.v1.ListSignupsForSubjectResponse
	28, // 64: primandproper.platform.waitlists.v1.WaitlistsService.UpdateSignupNotes:output_type -> primandproper.platform.waitlists.v1.UpdateSignupNotesResponse
	30, // 65: primandproper.platform.waitlists.v1.WaitlistsService.Invite:output_type -> primandproper.platform.waitlists.v1.InviteResponse
	32, // 66: primandproper.platform.waitlists.v1.WaitlistsService.Convert:output_type -> primandproper.platform.waitlists.v1.ConvertResponse
	34, // 67: primandproper.platform.waitlists.v1.WaitlistsService.Withdraw:output_type -> primandproper.platform.waitlists.v1.WithdrawResponse
	36, // 68: primandproper.platform.waitlists.v1.WaitlistsService.WithdrawSignupsForSubject:output_type -> primandproper.platform.waitlists.v1.WithdrawSignupsForSubjectResponse
	38, // 69: primandproper.platform.waitlists.v1.WaitlistsService.ArchiveSignup:output_type -> primandproper.platform.waitlists.v1.ArchiveSignupResponse
	53, // [53:70] is the sub-list for method output_type
	36, // [36:53] is the sub-list for method input_type
	36, // [36:36] is the sub-list for extension type_name
	36, // [36:36] is the sub-list for extension extendee
	0,  // [0:36] is the sub-list for field type_name
}

func init() { file_primandproper_platform_waitlists_v1_waitlists_proto_init() }
func file_primandproper_platform_waitlists_v1_waitlists_proto_init() {
	if File_primandproper_platform_waitlists_v1_waitlists_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_primandproper_platform_waitlists_v1_waitlists_proto_rawDesc), len(file_primandproper_platform_waitlists_v1_waitlists_proto_rawDesc)),
			NumEnums:      1,
			NumMessages:   38,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_primandproper_platform_waitlists_v1_waitlists_proto_goTypes,
		DependencyIndexes: file_primandproper_platform_waitlists_v1_waitlists_proto_depIdxs,
		EnumInfos:         file_primandproper_platform_waitlists_v1_waitlists_proto_enumTypes,
		MessageInfos:      file_primandproper_platform_waitlists_v1_waitlists_proto_msgTypes,
	}.Build()
	File_primandproper_platform_waitlists_v1_waitlists_proto = out.File
	file_primandproper_platform_waitlists_v1_waitlists_proto_goTypes = nil
	file_primandproper_platform_waitlists_v1_waitlists_proto_depIdxs = nil
}
