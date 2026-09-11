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

// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.11
// 	protoc        v6.33.1
// source: primandproper/platform/settings/v1/settings.proto

package settingspb

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

// SettingKind is how a setting's stored text is parsed. It is a closed set this
// package owns; see the file comment.
type SettingKind int32

const (
	// Unset. A request naming it is refused rather than defaulted: guessing which
	// of four a caller meant is how a setting ends up holding a value nothing can
	// read back.
	SettingKind_SETTING_KIND_UNSPECIFIED SettingKind = 0
	// Any text. Every value is legal unless the definition enumerates its own,
	// and the empty string is one of them.
	SettingKind_SETTING_KIND_STRING SettingKind = 1
	// A flag.
	SettingKind_SETTING_KIND_BOOLEAN SettingKind = 2
	// A signed 64-bit integer.
	SettingKind_SETTING_KIND_INTEGER SettingKind = 3
	// A 64-bit float.
	SettingKind_SETTING_KIND_FLOAT SettingKind = 4
)

// Enum value maps for SettingKind.
var (
	SettingKind_name = map[int32]string{
		0: "SETTING_KIND_UNSPECIFIED",
		1: "SETTING_KIND_STRING",
		2: "SETTING_KIND_BOOLEAN",
		3: "SETTING_KIND_INTEGER",
		4: "SETTING_KIND_FLOAT",
	}
	SettingKind_value = map[string]int32{
		"SETTING_KIND_UNSPECIFIED": 0,
		"SETTING_KIND_STRING":      1,
		"SETTING_KIND_BOOLEAN":     2,
		"SETTING_KIND_INTEGER":     3,
		"SETTING_KIND_FLOAT":       4,
	}
)

func (x SettingKind) Enum() *SettingKind {
	p := new(SettingKind)
	*p = x
	return p
}

func (x SettingKind) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (SettingKind) Descriptor() protoreflect.EnumDescriptor {
	return file_primandproper_platform_settings_v1_settings_proto_enumTypes[0].Descriptor()
}

func (SettingKind) Type() protoreflect.EnumType {
	return &file_primandproper_platform_settings_v1_settings_proto_enumTypes[0]
}

func (x SettingKind) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use SettingKind.Descriptor instead.
func (SettingKind) EnumDescriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{0}
}

// ValueSource says where a resolved setting's value came from. It is the
// tri-state settings.Resolution exists for, carried as data rather than as an
// error.
type ValueSource int32

const (
	// Unset. No resolution this service returns carries it; a client reading one
	// is reading a message some other producer built.
	ValueSource_VALUE_SOURCE_UNSPECIFIED ValueSource = 0
	// The subject chose it.
	ValueSource_VALUE_SOURCE_SUBJECT ValueSource = 1
	// They have not, and the definition has a default.
	ValueSource_VALUE_SOURCE_DEFAULT ValueSource = 2
	// They have not, and it has no default. typed_value is absent, and the
	// caller's own policy applies.
	ValueSource_VALUE_SOURCE_UNSET ValueSource = 3
)

// Enum value maps for ValueSource.
var (
	ValueSource_name = map[int32]string{
		0: "VALUE_SOURCE_UNSPECIFIED",
		1: "VALUE_SOURCE_SUBJECT",
		2: "VALUE_SOURCE_DEFAULT",
		3: "VALUE_SOURCE_UNSET",
	}
	ValueSource_value = map[string]int32{
		"VALUE_SOURCE_UNSPECIFIED": 0,
		"VALUE_SOURCE_SUBJECT":     1,
		"VALUE_SOURCE_DEFAULT":     2,
		"VALUE_SOURCE_UNSET":       3,
	}
)

func (x ValueSource) Enum() *ValueSource {
	p := new(ValueSource)
	*p = x
	return p
}

func (x ValueSource) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ValueSource) Descriptor() protoreflect.EnumDescriptor {
	return file_primandproper_platform_settings_v1_settings_proto_enumTypes[1].Descriptor()
}

func (ValueSource) Type() protoreflect.EnumType {
	return &file_primandproper_platform_settings_v1_settings_proto_enumTypes[1]
}

func (x ValueSource) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ValueSource.Descriptor instead.
func (ValueSource) EnumDescriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{1}
}

// TypedValue is one setting's value, parsed into the kind its definition
// declares.
//
// The case set is the same closed four [SettingKind] names, and a case that
// disagrees with the setting's kind is refused with settings.ErrKindMismatch
// rather than coerced -- reading an integer setting as a boolean is a mistake
// in the calling code, and false is the worst available answer to it.
type TypedValue struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to Value:
	//
	//	*TypedValue_StringValue
	//	*TypedValue_BoolValue
	//	*TypedValue_IntValue
	//	*TypedValue_FloatValue
	Value         isTypedValue_Value `protobuf_oneof:"value"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *TypedValue) Reset() {
	*x = TypedValue{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *TypedValue) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*TypedValue) ProtoMessage() {}

func (x *TypedValue) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use TypedValue.ProtoReflect.Descriptor instead.
func (*TypedValue) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{0}
}

func (x *TypedValue) GetValue() isTypedValue_Value {
	if x != nil {
		return x.Value
	}
	return nil
}

func (x *TypedValue) GetStringValue() string {
	if x != nil {
		if x, ok := x.Value.(*TypedValue_StringValue); ok {
			return x.StringValue
		}
	}
	return ""
}

func (x *TypedValue) GetBoolValue() bool {
	if x != nil {
		if x, ok := x.Value.(*TypedValue_BoolValue); ok {
			return x.BoolValue
		}
	}
	return false
}

func (x *TypedValue) GetIntValue() int64 {
	if x != nil {
		if x, ok := x.Value.(*TypedValue_IntValue); ok {
			return x.IntValue
		}
	}
	return 0
}

func (x *TypedValue) GetFloatValue() float64 {
	if x != nil {
		if x, ok := x.Value.(*TypedValue_FloatValue); ok {
			return x.FloatValue
		}
	}
	return 0
}

type isTypedValue_Value interface {
	isTypedValue_Value()
}

type TypedValue_StringValue struct {
	StringValue string `protobuf:"bytes,1,opt,name=string_value,json=stringValue,proto3,oneof"`
}

type TypedValue_BoolValue struct {
	BoolValue bool `protobuf:"varint,2,opt,name=bool_value,json=boolValue,proto3,oneof"`
}

type TypedValue_IntValue struct {
	IntValue int64 `protobuf:"varint,3,opt,name=int_value,json=intValue,proto3,oneof"`
}

type TypedValue_FloatValue struct {
	FloatValue float64 `protobuf:"fixed64,4,opt,name=float_value,json=floatValue,proto3,oneof"`
}

func (*TypedValue_StringValue) isTypedValue_Value() {}

func (*TypedValue_BoolValue) isTypedValue_Value() {}

func (*TypedValue_IntValue) isTypedValue_Value() {}

func (*TypedValue_FloatValue) isTypedValue_Value() {}

// SettingSubject is whose setting a value is: a kind of principal and an
// identifier within it.
//
// Two fields rather than one composite string, which is the same reading the
// scope is under -- a key spelling "user:abc123" carries two facts in a column
// that can only be indexed, filtered and enumerated as one.
type SettingSubject struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// type is what kind of principal this is. "user" and "account" are the two
	// this module suggests; the vocabulary is the consumer's, so it is a string.
	Type string `protobuf:"bytes,1,opt,name=type,proto3" json:"type,omitempty"`
	// id identifies the principal within that type.
	Id            string `protobuf:"bytes,2,opt,name=id,proto3" json:"id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SettingSubject) Reset() {
	*x = SettingSubject{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SettingSubject) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SettingSubject) ProtoMessage() {}

func (x *SettingSubject) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SettingSubject.ProtoReflect.Descriptor instead.
func (*SettingSubject) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{1}
}

func (x *SettingSubject) GetType() string {
	if x != nil {
		return x.Type
	}
	return ""
}

func (x *SettingSubject) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

// SettingDefinition is what a setting is: the name application code asks for,
// the kind of value it holds, what it falls back to, and which values it
// admits.
//
// Definitions are administrative rows. Nothing on a request path creates one --
// the catalog is a deployment's decision, in the same sense that a database
// column is -- and what a request path does is read one and store an answer
// against it.
type SettingDefinition struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// created_at is when the definition was added, assigned by the database.
	CreatedAt *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=created_at,json=createdAt,proto3" json:"created_at,omitempty"`
	// last_updated_at is when it last changed, unset for one nobody has edited.
	LastUpdatedAt *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=last_updated_at,json=lastUpdatedAt,proto3" json:"last_updated_at,omitempty"`
	// archived_at is when it was retired, unset while it is live. The values
	// stored against a retired definition are left alone and its name stays
	// claimed, because archiving is not erasure and freeing the name would let a
	// second definition inherit rows written for the first.
	ArchivedAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=archived_at,json=archivedAt,proto3" json:"archived_at,omitempty"`
	// id is the row.
	Id string `protobuf:"bytes,4,opt,name=id,proto3" json:"id,omitempty"`
	// name is what application code asks for, unique within the deployment's
	// scope, and the only handle a value-side call takes.
	Name string `protobuf:"bytes,5,opt,name=name,proto3" json:"name,omitempty"`
	// description is prose for whoever administers the setting.
	Description string `protobuf:"bytes,6,opt,name=description,proto3" json:"description,omitempty"`
	// kind is how a stored value is parsed.
	Kind SettingKind `protobuf:"varint,7,opt,name=kind,proto3,enum=primandproper.platform.settings.v1.SettingKind" json:"kind,omitempty"`
	// default_value is what a subject who has not chosen resolves to, absent for
	// a definition with no default at all. The two are different answers, which
	// is why the field carries presence.
	//
	// It is the string as stored rather than a TypedValue: a default is held to
	// the enumeration by string equality, and a typed round-trip would rewrite
	// the bytes that comparison is made with. See the file comment.
	DefaultValue *string `protobuf:"bytes,8,opt,name=default_value,json=defaultValue,proto3,oneof" json:"default_value,omitempty"`
	// enumeration is the values this setting admits, or empty for one that admits
	// any value of its kind. It comes back sorted, and it is a set rather than a
	// sequence: what it decides is whether a write is legal, and membership has
	// no order.
	Enumeration []string `protobuf:"bytes,9,rep,name=enumeration,proto3" json:"enumeration,omitempty"`
	// admin_only marks a setting only an administrator may write. It is recorded
	// rather than enforced -- this schema has no notion of who is calling -- and
	// what it is for is the consumer's own check and the admin UI that needs to
	// know which settings to hide from a self-service page.
	AdminOnly     bool `protobuf:"varint,10,opt,name=admin_only,json=adminOnly,proto3" json:"admin_only,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SettingDefinition) Reset() {
	*x = SettingDefinition{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SettingDefinition) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SettingDefinition) ProtoMessage() {}

func (x *SettingDefinition) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SettingDefinition.ProtoReflect.Descriptor instead.
func (*SettingDefinition) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{2}
}

func (x *SettingDefinition) GetCreatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CreatedAt
	}
	return nil
}

func (x *SettingDefinition) GetLastUpdatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.LastUpdatedAt
	}
	return nil
}

func (x *SettingDefinition) GetArchivedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ArchivedAt
	}
	return nil
}

func (x *SettingDefinition) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *SettingDefinition) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *SettingDefinition) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *SettingDefinition) GetKind() SettingKind {
	if x != nil {
		return x.Kind
	}
	return SettingKind_SETTING_KIND_UNSPECIFIED
}

func (x *SettingDefinition) GetDefaultValue() string {
	if x != nil && x.DefaultValue != nil {
		return *x.DefaultValue
	}
	return ""
}

func (x *SettingDefinition) GetEnumeration() []string {
	if x != nil {
		return x.Enumeration
	}
	return nil
}

func (x *SettingDefinition) GetAdminOnly() bool {
	if x != nil {
		return x.AdminOnly
	}
	return false
}

// SettingValue is what one subject answered, as stored.
//
// It carries the raw text rather than a TypedValue, deliberately: this is the
// row, and GetValue is the read that hands it back unparsed. [ResolvedSetting]
// is where a value becomes typed, because that is where this package has read
// the definition that says which type it is.
type SettingValue struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// created_at is when the subject first answered. A value cleared and set
	// again keeps it, because the write converges on the row rather than adding a
	// second one.
	CreatedAt *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=created_at,json=createdAt,proto3" json:"created_at,omitempty"`
	// last_updated_at is when the answer last changed, unset for one set once.
	LastUpdatedAt *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=last_updated_at,json=lastUpdatedAt,proto3" json:"last_updated_at,omitempty"`
	// archived_at is when the answer was cleared. A cleared value resolves as
	// though it had never been set, and the row still says what the subject
	// chose, which is what an audit of a preference change reads.
	ArchivedAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=archived_at,json=archivedAt,proto3" json:"archived_at,omitempty"`
	// subject is whose answer it is.
	Subject *SettingSubject `protobuf:"bytes,4,opt,name=subject,proto3" json:"subject,omitempty"`
	// id identifies the row. It is not how the row is addressed -- every
	// single-row statement keys on the subject and the definition -- and what it
	// is for is the cursor a page walks.
	Id string `protobuf:"bytes,5,opt,name=id,proto3" json:"id,omitempty"`
	// definition_id is the definition this answers.
	DefinitionId string `protobuf:"bytes,6,opt,name=definition_id,json=definitionID,proto3" json:"definition_id,omitempty"`
	// raw is the answer as it is stored. One column holds every kind.
	Raw           string `protobuf:"bytes,7,opt,name=raw,proto3" json:"raw,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SettingValue) Reset() {
	*x = SettingValue{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SettingValue) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SettingValue) ProtoMessage() {}

func (x *SettingValue) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SettingValue.ProtoReflect.Descriptor instead.
func (*SettingValue) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{3}
}

func (x *SettingValue) GetCreatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CreatedAt
	}
	return nil
}

func (x *SettingValue) GetLastUpdatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.LastUpdatedAt
	}
	return nil
}

func (x *SettingValue) GetArchivedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ArchivedAt
	}
	return nil
}

func (x *SettingValue) GetSubject() *SettingSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

func (x *SettingValue) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *SettingValue) GetDefinitionId() string {
	if x != nil {
		return x.DefinitionId
	}
	return ""
}

func (x *SettingValue) GetRaw() string {
	if x != nil {
		return x.Raw
	}
	return ""
}

// ResolvedSetting is a setting resolved for a subject: the value, where it came
// from, and the definition it was resolved against.
//
// It is the message this file exists for. Resolution has three answers and not
// two -- the subject chose, the default answered, or nobody has decided -- and
// a client that had to reconstruct that from a value and a definition would
// reconstruct it slightly differently every time.
type ResolvedSetting struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// definition is the setting that was resolved. Always present: resolution
	// begins by reading it, and a setting that does not exist is an error rather
	// than an unset resolution.
	Definition *SettingDefinition `protobuf:"bytes,1,opt,name=definition,proto3" json:"definition,omitempty"`
	// value is the row the subject set, absent when the default answered or
	// nothing did. It is here so a client can show when somebody chose.
	Value *SettingValue `protobuf:"bytes,2,opt,name=value,proto3" json:"value,omitempty"`
	// typed_value is the answer, parsed into the definition's kind. Absent when
	// source is VALUE_SOURCE_UNSET and present otherwise, including where the
	// answer is the definition's default.
	TypedValue *TypedValue `protobuf:"bytes,3,opt,name=typed_value,json=typedValue,proto3" json:"typed_value,omitempty"`
	// source says which of the three cases this is.
	Source        ValueSource `protobuf:"varint,4,opt,name=source,proto3,enum=primandproper.platform.settings.v1.ValueSource" json:"source,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ResolvedSetting) Reset() {
	*x = ResolvedSetting{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ResolvedSetting) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ResolvedSetting) ProtoMessage() {}

func (x *ResolvedSetting) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ResolvedSetting.ProtoReflect.Descriptor instead.
func (*ResolvedSetting) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{4}
}

func (x *ResolvedSetting) GetDefinition() *SettingDefinition {
	if x != nil {
		return x.Definition
	}
	return nil
}

func (x *ResolvedSetting) GetValue() *SettingValue {
	if x != nil {
		return x.Value
	}
	return nil
}

func (x *ResolvedSetting) GetTypedValue() *TypedValue {
	if x != nil {
		return x.TypedValue
	}
	return nil
}

func (x *ResolvedSetting) GetSource() ValueSource {
	if x != nil {
		return x.Source
	}
	return ValueSource_VALUE_SOURCE_UNSPECIFIED
}

// SettingDefinitionInput is what a caller supplies to add a setting to the
// catalog or to rewrite one. Everything else about the row -- the id, the
// timestamps and the scope -- is decided by the service.
type SettingDefinitionInput struct {
	state       protoimpl.MessageState `protogen:"open.v1"`
	Name        string                 `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	Description string                 `protobuf:"bytes,2,opt,name=description,proto3" json:"description,omitempty"`
	Kind        SettingKind            `protobuf:"varint,3,opt,name=kind,proto3,enum=primandproper.platform.settings.v1.SettingKind" json:"kind,omitempty"`
	// default_value is absent for a setting with no default. A default the
	// setting would not admit -- of another kind, or outside the enumeration
	// below -- is refused rather than stored, because it would answer every
	// subject who has not chosen with a value the setting does not admit.
	DefaultValue *string `protobuf:"bytes,4,opt,name=default_value,json=defaultValue,proto3,oneof" json:"default_value,omitempty"`
	// enumeration is the values the setting admits. An empty or repeated entry is
	// refused: the enumeration is what every write is checked against, and a
	// legal "" cannot be told apart from a slot somebody left blank.
	Enumeration   []string `protobuf:"bytes,5,rep,name=enumeration,proto3" json:"enumeration,omitempty"`
	AdminOnly     bool     `protobuf:"varint,6,opt,name=admin_only,json=adminOnly,proto3" json:"admin_only,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SettingDefinitionInput) Reset() {
	*x = SettingDefinitionInput{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SettingDefinitionInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SettingDefinitionInput) ProtoMessage() {}

func (x *SettingDefinitionInput) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SettingDefinitionInput.ProtoReflect.Descriptor instead.
func (*SettingDefinitionInput) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{5}
}

func (x *SettingDefinitionInput) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *SettingDefinitionInput) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *SettingDefinitionInput) GetKind() SettingKind {
	if x != nil {
		return x.Kind
	}
	return SettingKind_SETTING_KIND_UNSPECIFIED
}

func (x *SettingDefinitionInput) GetDefaultValue() string {
	if x != nil && x.DefaultValue != nil {
		return *x.DefaultValue
	}
	return ""
}

func (x *SettingDefinitionInput) GetEnumeration() []string {
	if x != nil {
		return x.Enumeration
	}
	return nil
}

func (x *SettingDefinitionInput) GetAdminOnly() bool {
	if x != nil {
		return x.AdminOnly
	}
	return false
}

type CreateDefinitionRequest struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Definition    *SettingDefinitionInput `protobuf:"bytes,1,opt,name=definition,proto3" json:"definition,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CreateDefinitionRequest) Reset() {
	*x = CreateDefinitionRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CreateDefinitionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CreateDefinitionRequest) ProtoMessage() {}

func (x *CreateDefinitionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CreateDefinitionRequest.ProtoReflect.Descriptor instead.
func (*CreateDefinitionRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{6}
}

func (x *CreateDefinitionRequest) GetDefinition() *SettingDefinitionInput {
	if x != nil {
		return x.Definition
	}
	return nil
}

type CreateDefinitionResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *SettingDefinition     `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CreateDefinitionResponse) Reset() {
	*x = CreateDefinitionResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CreateDefinitionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CreateDefinitionResponse) ProtoMessage() {}

func (x *CreateDefinitionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CreateDefinitionResponse.ProtoReflect.Descriptor instead.
func (*CreateDefinitionResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{7}
}

func (x *CreateDefinitionResponse) GetResult() *SettingDefinition {
	if x != nil {
		return x.Result
	}
	return nil
}

type GetDefinitionRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	DefinitionId  string                 `protobuf:"bytes,1,opt,name=definition_id,json=definitionID,proto3" json:"definition_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetDefinitionRequest) Reset() {
	*x = GetDefinitionRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[8]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetDefinitionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetDefinitionRequest) ProtoMessage() {}

func (x *GetDefinitionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[8]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetDefinitionRequest.ProtoReflect.Descriptor instead.
func (*GetDefinitionRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{8}
}

func (x *GetDefinitionRequest) GetDefinitionId() string {
	if x != nil {
		return x.DefinitionId
	}
	return ""
}

type GetDefinitionResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *SettingDefinition     `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetDefinitionResponse) Reset() {
	*x = GetDefinitionResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[9]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetDefinitionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetDefinitionResponse) ProtoMessage() {}

func (x *GetDefinitionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[9]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetDefinitionResponse.ProtoReflect.Descriptor instead.
func (*GetDefinitionResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{9}
}

func (x *GetDefinitionResponse) GetResult() *SettingDefinition {
	if x != nil {
		return x.Result
	}
	return nil
}

type GetDefinitionByNameRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Name          string                 `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetDefinitionByNameRequest) Reset() {
	*x = GetDefinitionByNameRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[10]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetDefinitionByNameRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetDefinitionByNameRequest) ProtoMessage() {}

func (x *GetDefinitionByNameRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[10]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetDefinitionByNameRequest.ProtoReflect.Descriptor instead.
func (*GetDefinitionByNameRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{10}
}

func (x *GetDefinitionByNameRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

type GetDefinitionByNameResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *SettingDefinition     `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetDefinitionByNameResponse) Reset() {
	*x = GetDefinitionByNameResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[11]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetDefinitionByNameResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetDefinitionByNameResponse) ProtoMessage() {}

func (x *GetDefinitionByNameResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[11]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetDefinitionByNameResponse.ProtoReflect.Descriptor instead.
func (*GetDefinitionByNameResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{11}
}

func (x *GetDefinitionByNameResponse) GetResult() *SettingDefinition {
	if x != nil {
		return x.Result
	}
	return nil
}

type ListDefinitionsRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,1,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListDefinitionsRequest) Reset() {
	*x = ListDefinitionsRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[12]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListDefinitionsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListDefinitionsRequest) ProtoMessage() {}

func (x *ListDefinitionsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[12]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListDefinitionsRequest.ProtoReflect.Descriptor instead.
func (*ListDefinitionsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{12}
}

func (x *ListDefinitionsRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListDefinitionsResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*SettingDefinition    `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListDefinitionsResponse) Reset() {
	*x = ListDefinitionsResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[13]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListDefinitionsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListDefinitionsResponse) ProtoMessage() {}

func (x *ListDefinitionsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[13]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListDefinitionsResponse.ProtoReflect.Descriptor instead.
func (*ListDefinitionsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{13}
}

func (x *ListDefinitionsResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListDefinitionsResponse) GetResults() []*SettingDefinition {
	if x != nil {
		return x.Results
	}
	return nil
}

type UpdateDefinitionRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// definition_id is the row to rewrite. It is a field of the request rather
	// than of the input below, so there is no second place for a caller to name
	// one and no way for the two to disagree.
	DefinitionId  string                  `protobuf:"bytes,1,opt,name=definition_id,json=definitionID,proto3" json:"definition_id,omitempty"`
	Definition    *SettingDefinitionInput `protobuf:"bytes,2,opt,name=definition,proto3" json:"definition,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateDefinitionRequest) Reset() {
	*x = UpdateDefinitionRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[14]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateDefinitionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateDefinitionRequest) ProtoMessage() {}

func (x *UpdateDefinitionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[14]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateDefinitionRequest.ProtoReflect.Descriptor instead.
func (*UpdateDefinitionRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{14}
}

func (x *UpdateDefinitionRequest) GetDefinitionId() string {
	if x != nil {
		return x.DefinitionId
	}
	return ""
}

func (x *UpdateDefinitionRequest) GetDefinition() *SettingDefinitionInput {
	if x != nil {
		return x.Definition
	}
	return nil
}

type UpdateDefinitionResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// result is the definition as rewritten, read back inside the transaction
	// that wrote it, so its last_updated_at is the database's rather than the
	// epoch.
	Result        *SettingDefinition `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateDefinitionResponse) Reset() {
	*x = UpdateDefinitionResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[15]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateDefinitionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateDefinitionResponse) ProtoMessage() {}

func (x *UpdateDefinitionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[15]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateDefinitionResponse.ProtoReflect.Descriptor instead.
func (*UpdateDefinitionResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{15}
}

func (x *UpdateDefinitionResponse) GetResult() *SettingDefinition {
	if x != nil {
		return x.Result
	}
	return nil
}

type ArchiveDefinitionRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	DefinitionId  string                 `protobuf:"bytes,1,opt,name=definition_id,json=definitionID,proto3" json:"definition_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveDefinitionRequest) Reset() {
	*x = ArchiveDefinitionRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[16]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveDefinitionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveDefinitionRequest) ProtoMessage() {}

func (x *ArchiveDefinitionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[16]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveDefinitionRequest.ProtoReflect.Descriptor instead.
func (*ArchiveDefinitionRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{16}
}

func (x *ArchiveDefinitionRequest) GetDefinitionId() string {
	if x != nil {
		return x.DefinitionId
	}
	return ""
}

type ArchiveDefinitionResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveDefinitionResponse) Reset() {
	*x = ArchiveDefinitionResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[17]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveDefinitionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveDefinitionResponse) ProtoMessage() {}

func (x *ArchiveDefinitionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[17]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveDefinitionResponse.ProtoReflect.Descriptor instead.
func (*ArchiveDefinitionResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{17}
}

type ListValuesForDefinitionRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// name is the setting, by the name application code spells rather than by its
	// row id, which is the handle every value-side call takes.
	Name          string                   `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,2,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListValuesForDefinitionRequest) Reset() {
	*x = ListValuesForDefinitionRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[18]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListValuesForDefinitionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListValuesForDefinitionRequest) ProtoMessage() {}

func (x *ListValuesForDefinitionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[18]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListValuesForDefinitionRequest.ProtoReflect.Descriptor instead.
func (*ListValuesForDefinitionRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{18}
}

func (x *ListValuesForDefinitionRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *ListValuesForDefinitionRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListValuesForDefinitionResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*SettingValue         `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListValuesForDefinitionResponse) Reset() {
	*x = ListValuesForDefinitionResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[19]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListValuesForDefinitionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListValuesForDefinitionResponse) ProtoMessage() {}

func (x *ListValuesForDefinitionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[19]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListValuesForDefinitionResponse.ProtoReflect.Descriptor instead.
func (*ListValuesForDefinitionResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{19}
}

func (x *ListValuesForDefinitionResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListValuesForDefinitionResponse) GetResults() []*SettingValue {
	if x != nil {
		return x.Results
	}
	return nil
}

type SetValueRequest struct {
	state   protoimpl.MessageState `protogen:"open.v1"`
	Subject *SettingSubject        `protobuf:"bytes,1,opt,name=subject,proto3" json:"subject,omitempty"`
	Name    string                 `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
	// value is the answer, typed. A case that disagrees with the setting's kind
	// is refused with settings.ErrKindMismatch, and a value outside the
	// definition's enumeration with settings.ErrNotEnumerated; naming no case at
	// all is a request that named no value, which is not the same as a
	// string_value of "".
	Value         *TypedValue `protobuf:"bytes,3,opt,name=value,proto3" json:"value,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SetValueRequest) Reset() {
	*x = SetValueRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[20]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SetValueRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SetValueRequest) ProtoMessage() {}

func (x *SetValueRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[20]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SetValueRequest.ProtoReflect.Descriptor instead.
func (*SetValueRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{20}
}

func (x *SetValueRequest) GetSubject() *SettingSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

func (x *SetValueRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *SetValueRequest) GetValue() *TypedValue {
	if x != nil {
		return x.Value
	}
	return nil
}

type SetValueResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// resolution is the setting as it now resolves for the subject, read back
	// inside the transaction that wrote the value. It is a resolution rather than
	// the row because what a settings screen renders after a save is the
	// effective value, and reading it anywhere but inside that transaction would
	// answer with what the subject had before.
	Resolution    *ResolvedSetting `protobuf:"bytes,1,opt,name=resolution,proto3" json:"resolution,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SetValueResponse) Reset() {
	*x = SetValueResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[21]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SetValueResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SetValueResponse) ProtoMessage() {}

func (x *SetValueResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[21]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SetValueResponse.ProtoReflect.Descriptor instead.
func (*SetValueResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{21}
}

func (x *SetValueResponse) GetResolution() *ResolvedSetting {
	if x != nil {
		return x.Resolution
	}
	return nil
}

type GetValueRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Subject       *SettingSubject        `protobuf:"bytes,1,opt,name=subject,proto3" json:"subject,omitempty"`
	Name          string                 `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetValueRequest) Reset() {
	*x = GetValueRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[22]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetValueRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetValueRequest) ProtoMessage() {}

func (x *GetValueRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[22]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetValueRequest.ProtoReflect.Descriptor instead.
func (*GetValueRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{22}
}

func (x *GetValueRequest) GetSubject() *SettingSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

func (x *GetValueRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

type GetValueResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// result is the stored row, unparsed, and it is absent from no successful
	// response: a subject who has not answered is settings.ErrValueNotFound
	// rather than an empty value. Resolve is the read that applies the default.
	Result        *SettingValue `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetValueResponse) Reset() {
	*x = GetValueResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[23]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetValueResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetValueResponse) ProtoMessage() {}

func (x *GetValueResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[23]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetValueResponse.ProtoReflect.Descriptor instead.
func (*GetValueResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{23}
}

func (x *GetValueResponse) GetResult() *SettingValue {
	if x != nil {
		return x.Result
	}
	return nil
}

type ClearValueRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Subject       *SettingSubject        `protobuf:"bytes,1,opt,name=subject,proto3" json:"subject,omitempty"`
	Name          string                 `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ClearValueRequest) Reset() {
	*x = ClearValueRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[24]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ClearValueRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ClearValueRequest) ProtoMessage() {}

func (x *ClearValueRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[24]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ClearValueRequest.ProtoReflect.Descriptor instead.
func (*ClearValueRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{24}
}

func (x *ClearValueRequest) GetSubject() *SettingSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

func (x *ClearValueRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

type ClearValueResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// resolution is what the setting resolves to now that the subject's answer is
	// gone -- the definition's default, or nothing -- read back inside the
	// transaction that cleared it. It is the value the screen that just showed a
	// "reset" button has to render next.
	Resolution    *ResolvedSetting `protobuf:"bytes,1,opt,name=resolution,proto3" json:"resolution,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ClearValueResponse) Reset() {
	*x = ClearValueResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[25]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ClearValueResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ClearValueResponse) ProtoMessage() {}

func (x *ClearValueResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[25]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ClearValueResponse.ProtoReflect.Descriptor instead.
func (*ClearValueResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{25}
}

func (x *ClearValueResponse) GetResolution() *ResolvedSetting {
	if x != nil {
		return x.Resolution
	}
	return nil
}

type ListValuesForSubjectRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	Subject       *SettingSubject          `protobuf:"bytes,1,opt,name=subject,proto3" json:"subject,omitempty"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,2,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListValuesForSubjectRequest) Reset() {
	*x = ListValuesForSubjectRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[26]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListValuesForSubjectRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListValuesForSubjectRequest) ProtoMessage() {}

func (x *ListValuesForSubjectRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[26]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListValuesForSubjectRequest.ProtoReflect.Descriptor instead.
func (*ListValuesForSubjectRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{26}
}

func (x *ListValuesForSubjectRequest) GetSubject() *SettingSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

func (x *ListValuesForSubjectRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListValuesForSubjectResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*SettingValue         `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListValuesForSubjectResponse) Reset() {
	*x = ListValuesForSubjectResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[27]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListValuesForSubjectResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListValuesForSubjectResponse) ProtoMessage() {}

func (x *ListValuesForSubjectResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[27]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListValuesForSubjectResponse.ProtoReflect.Descriptor instead.
func (*ListValuesForSubjectResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{27}
}

func (x *ListValuesForSubjectResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListValuesForSubjectResponse) GetResults() []*SettingValue {
	if x != nil {
		return x.Results
	}
	return nil
}

type ResolveRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Subject       *SettingSubject        `protobuf:"bytes,1,opt,name=subject,proto3" json:"subject,omitempty"`
	Name          string                 `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ResolveRequest) Reset() {
	*x = ResolveRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[28]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ResolveRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ResolveRequest) ProtoMessage() {}

func (x *ResolveRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[28]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ResolveRequest.ProtoReflect.Descriptor instead.
func (*ResolveRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{28}
}

func (x *ResolveRequest) GetSubject() *SettingSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

func (x *ResolveRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

type ResolveResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Resolution    *ResolvedSetting       `protobuf:"bytes,1,opt,name=resolution,proto3" json:"resolution,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ResolveResponse) Reset() {
	*x = ResolveResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[29]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ResolveResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ResolveResponse) ProtoMessage() {}

func (x *ResolveResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[29]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ResolveResponse.ProtoReflect.Descriptor instead.
func (*ResolveResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{29}
}

func (x *ResolveResponse) GetResolution() *ResolvedSetting {
	if x != nil {
		return x.Resolution
	}
	return nil
}

type ResolveAllRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Subject       *SettingSubject        `protobuf:"bytes,1,opt,name=subject,proto3" json:"subject,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ResolveAllRequest) Reset() {
	*x = ResolveAllRequest{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[30]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ResolveAllRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ResolveAllRequest) ProtoMessage() {}

func (x *ResolveAllRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[30]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ResolveAllRequest.ProtoReflect.Descriptor instead.
func (*ResolveAllRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{30}
}

func (x *ResolveAllRequest) GetSubject() *SettingSubject {
	if x != nil {
		return x.Subject
	}
	return nil
}

type ResolveAllResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// resolutions is every live setting in the deployment's catalog, sorted by
	// name, including the ones the subject has not answered. A page rendering
	// "your preferences" wants those too, at their default or as unset.
	//
	// It is not paged, and the read beneath it is not either: it is one pass over
	// the catalog and one over the subject's answers rather than one resolution
	// per setting.
	Resolutions   []*ResolvedSetting `protobuf:"bytes,1,rep,name=resolutions,proto3" json:"resolutions,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ResolveAllResponse) Reset() {
	*x = ResolveAllResponse{}
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[31]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ResolveAllResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ResolveAllResponse) ProtoMessage() {}

func (x *ResolveAllResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_settings_v1_settings_proto_msgTypes[31]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ResolveAllResponse.ProtoReflect.Descriptor instead.
func (*ResolveAllResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP(), []int{31}
}

func (x *ResolveAllResponse) GetResolutions() []*ResolvedSetting {
	if x != nil {
		return x.Resolutions
	}
	return nil
}

var File_primandproper_platform_settings_v1_settings_proto protoreflect.FileDescriptor

const file_primandproper_platform_settings_v1_settings_proto_rawDesc = "" +
	"\n" +
	"1primandproper/platform/settings/v1/settings.proto\x12\"primandproper.platform.settings.v1\x1a\x1fgoogle/protobuf/timestamp.proto\x1a3primandproper/platform/filtering/v1/filtering.proto\"\x9d\x01\n" +
	"\n" +
	"TypedValue\x12#\n" +
	"\fstring_value\x18\x01 \x01(\tH\x00R\vstringValue\x12\x1f\n" +
	"\n" +
	"bool_value\x18\x02 \x01(\bH\x00R\tboolValue\x12\x1d\n" +
	"\tint_value\x18\x03 \x01(\x03H\x00R\bintValue\x12!\n" +
	"\vfloat_value\x18\x04 \x01(\x01H\x00R\n" +
	"floatValueB\a\n" +
	"\x05value\";\n" +
	"\x0eSettingSubject\x12\x12\n" +
	"\x04type\x18\x01 \x01(\tR\x04type\x12\x0e\n" +
	"\x02id\x18\x02 \x01(\tR\x02idR\x05scope\"\xde\x03\n" +
	"\x11SettingDefinition\x129\n" +
	"\n" +
	"created_at\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\tcreatedAt\x12B\n" +
	"\x0flast_updated_at\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\rlastUpdatedAt\x12;\n" +
	"\varchived_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"archivedAt\x12\x0e\n" +
	"\x02id\x18\x04 \x01(\tR\x02id\x12\x12\n" +
	"\x04name\x18\x05 \x01(\tR\x04name\x12 \n" +
	"\vdescription\x18\x06 \x01(\tR\vdescription\x12C\n" +
	"\x04kind\x18\a \x01(\x0e2/.primandproper.platform.settings.v1.SettingKindR\x04kind\x12(\n" +
	"\rdefault_value\x18\b \x01(\tH\x00R\fdefaultValue\x88\x01\x01\x12 \n" +
	"\venumeration\x18\t \x03(\tR\venumeration\x12\x1d\n" +
	"\n" +
	"admin_only\x18\n" +
	" \x01(\bR\tadminOnlyB\x10\n" +
	"\x0e_default_valueR\x05scope\"\xe6\x02\n" +
	"\fSettingValue\x129\n" +
	"\n" +
	"created_at\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\tcreatedAt\x12B\n" +
	"\x0flast_updated_at\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\rlastUpdatedAt\x12;\n" +
	"\varchived_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"archivedAt\x12L\n" +
	"\asubject\x18\x04 \x01(\v22.primandproper.platform.settings.v1.SettingSubjectR\asubject\x12\x0e\n" +
	"\x02id\x18\x05 \x01(\tR\x02id\x12#\n" +
	"\rdefinition_id\x18\x06 \x01(\tR\fdefinitionID\x12\x10\n" +
	"\x03raw\x18\a \x01(\tR\x03rawR\x05scope\"\xd1\x02\n" +
	"\x0fResolvedSetting\x12U\n" +
	"\n" +
	"definition\x18\x01 \x01(\v25.primandproper.platform.settings.v1.SettingDefinitionR\n" +
	"definition\x12F\n" +
	"\x05value\x18\x02 \x01(\v20.primandproper.platform.settings.v1.SettingValueR\x05value\x12O\n" +
	"\vtyped_value\x18\x03 \x01(\v2..primandproper.platform.settings.v1.TypedValueR\n" +
	"typedValue\x12G\n" +
	"\x06source\x18\x04 \x01(\x0e2/.primandproper.platform.settings.v1.ValueSourceR\x06sourceR\x05scope\"\x97\x02\n" +
	"\x16SettingDefinitionInput\x12\x12\n" +
	"\x04name\x18\x01 \x01(\tR\x04name\x12 \n" +
	"\vdescription\x18\x02 \x01(\tR\vdescription\x12C\n" +
	"\x04kind\x18\x03 \x01(\x0e2/.primandproper.platform.settings.v1.SettingKindR\x04kind\x12(\n" +
	"\rdefault_value\x18\x04 \x01(\tH\x00R\fdefaultValue\x88\x01\x01\x12 \n" +
	"\venumeration\x18\x05 \x03(\tR\venumeration\x12\x1d\n" +
	"\n" +
	"admin_only\x18\x06 \x01(\bR\tadminOnlyB\x10\n" +
	"\x0e_default_valueR\x05scope\"|\n" +
	"\x17CreateDefinitionRequest\x12Z\n" +
	"\n" +
	"definition\x18\x01 \x01(\v2:.primandproper.platform.settings.v1.SettingDefinitionInputR\n" +
	"definitionR\x05scope\"i\n" +
	"\x18CreateDefinitionResponse\x12M\n" +
	"\x06result\x18\x01 \x01(\v25.primandproper.platform.settings.v1.SettingDefinitionR\x06result\"B\n" +
	"\x14GetDefinitionRequest\x12#\n" +
	"\rdefinition_id\x18\x01 \x01(\tR\fdefinitionIDR\x05scope\"f\n" +
	"\x15GetDefinitionResponse\x12M\n" +
	"\x06result\x18\x01 \x01(\v25.primandproper.platform.settings.v1.SettingDefinitionR\x06result\"7\n" +
	"\x1aGetDefinitionByNameRequest\x12\x12\n" +
	"\x04name\x18\x01 \x01(\tR\x04nameR\x05scope\"l\n" +
	"\x1bGetDefinitionByNameResponse\x12M\n" +
	"\x06result\x18\x01 \x01(\v25.primandproper.platform.settings.v1.SettingDefinitionR\x06result\"i\n" +
	"\x16ListDefinitionsRequest\x12H\n" +
	"\x06filter\x18\x01 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xbb\x01\n" +
	"\x17ListDefinitionsResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12O\n" +
	"\aresults\x18\x02 \x03(\v25.primandproper.platform.settings.v1.SettingDefinitionR\aresults\"\xa1\x01\n" +
	"\x17UpdateDefinitionRequest\x12#\n" +
	"\rdefinition_id\x18\x01 \x01(\tR\fdefinitionID\x12Z\n" +
	"\n" +
	"definition\x18\x02 \x01(\v2:.primandproper.platform.settings.v1.SettingDefinitionInputR\n" +
	"definitionR\x05scope\"i\n" +
	"\x18UpdateDefinitionResponse\x12M\n" +
	"\x06result\x18\x01 \x01(\v25.primandproper.platform.settings.v1.SettingDefinitionR\x06result\"F\n" +
	"\x18ArchiveDefinitionRequest\x12#\n" +
	"\rdefinition_id\x18\x01 \x01(\tR\fdefinitionIDR\x05scope\"\x1b\n" +
	"\x19ArchiveDefinitionResponse\"\x85\x01\n" +
	"\x1eListValuesForDefinitionRequest\x12\x12\n" +
	"\x04name\x18\x01 \x01(\tR\x04name\x12H\n" +
	"\x06filter\x18\x02 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xbe\x01\n" +
	"\x1fListValuesForDefinitionResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12J\n" +
	"\aresults\x18\x02 \x03(\v20.primandproper.platform.settings.v1.SettingValueR\aresults\"\xc0\x01\n" +
	"\x0fSetValueRequest\x12L\n" +
	"\asubject\x18\x01 \x01(\v22.primandproper.platform.settings.v1.SettingSubjectR\asubject\x12\x12\n" +
	"\x04name\x18\x02 \x01(\tR\x04name\x12D\n" +
	"\x05value\x18\x03 \x01(\v2..primandproper.platform.settings.v1.TypedValueR\x05valueR\x05scope\"g\n" +
	"\x10SetValueResponse\x12S\n" +
	"\n" +
	"resolution\x18\x01 \x01(\v23.primandproper.platform.settings.v1.ResolvedSettingR\n" +
	"resolution\"z\n" +
	"\x0fGetValueRequest\x12L\n" +
	"\asubject\x18\x01 \x01(\v22.primandproper.platform.settings.v1.SettingSubjectR\asubject\x12\x12\n" +
	"\x04name\x18\x02 \x01(\tR\x04nameR\x05scope\"\\\n" +
	"\x10GetValueResponse\x12H\n" +
	"\x06result\x18\x01 \x01(\v20.primandproper.platform.settings.v1.SettingValueR\x06result\"|\n" +
	"\x11ClearValueRequest\x12L\n" +
	"\asubject\x18\x01 \x01(\v22.primandproper.platform.settings.v1.SettingSubjectR\asubject\x12\x12\n" +
	"\x04name\x18\x02 \x01(\tR\x04nameR\x05scope\"i\n" +
	"\x12ClearValueResponse\x12S\n" +
	"\n" +
	"resolution\x18\x01 \x01(\v23.primandproper.platform.settings.v1.ResolvedSettingR\n" +
	"resolution\"\xbc\x01\n" +
	"\x1bListValuesForSubjectRequest\x12L\n" +
	"\asubject\x18\x01 \x01(\v22.primandproper.platform.settings.v1.SettingSubjectR\asubject\x12H\n" +
	"\x06filter\x18\x02 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xbb\x01\n" +
	"\x1cListValuesForSubjectResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12J\n" +
	"\aresults\x18\x02 \x03(\v20.primandproper.platform.settings.v1.SettingValueR\aresults\"y\n" +
	"\x0eResolveRequest\x12L\n" +
	"\asubject\x18\x01 \x01(\v22.primandproper.platform.settings.v1.SettingSubjectR\asubject\x12\x12\n" +
	"\x04name\x18\x02 \x01(\tR\x04nameR\x05scope\"f\n" +
	"\x0fResolveResponse\x12S\n" +
	"\n" +
	"resolution\x18\x01 \x01(\v23.primandproper.platform.settings.v1.ResolvedSettingR\n" +
	"resolution\"h\n" +
	"\x11ResolveAllRequest\x12L\n" +
	"\asubject\x18\x01 \x01(\v22.primandproper.platform.settings.v1.SettingSubjectR\asubjectR\x05scope\"k\n" +
	"\x12ResolveAllResponse\x12U\n" +
	"\vresolutions\x18\x01 \x03(\v23.primandproper.platform.settings.v1.ResolvedSettingR\vresolutions*\x90\x01\n" +
	"\vSettingKind\x12\x1c\n" +
	"\x18SETTING_KIND_UNSPECIFIED\x10\x00\x12\x17\n" +
	"\x13SETTING_KIND_STRING\x10\x01\x12\x18\n" +
	"\x14SETTING_KIND_BOOLEAN\x10\x02\x12\x18\n" +
	"\x14SETTING_KIND_INTEGER\x10\x03\x12\x16\n" +
	"\x12SETTING_KIND_FLOAT\x10\x04*w\n" +
	"\vValueSource\x12\x1c\n" +
	"\x18VALUE_SOURCE_UNSPECIFIED\x10\x00\x12\x18\n" +
	"\x14VALUE_SOURCE_SUBJECT\x10\x01\x12\x18\n" +
	"\x14VALUE_SOURCE_DEFAULT\x10\x02\x12\x16\n" +
	"\x12VALUE_SOURCE_UNSET\x10\x032\x8e\x0e\n" +
	"\x0fSettingsService\x12\x8d\x01\n" +
	"\x10CreateDefinition\x12;.primandproper.platform.settings.v1.CreateDefinitionRequest\x1a<.primandproper.platform.settings.v1.CreateDefinitionResponse\x12\x84\x01\n" +
	"\rGetDefinition\x128.primandproper.platform.settings.v1.GetDefinitionRequest\x1a9.primandproper.platform.settings.v1.GetDefinitionResponse\x12\x96\x01\n" +
	"\x13GetDefinitionByName\x12>.primandproper.platform.settings.v1.GetDefinitionByNameRequest\x1a?.primandproper.platform.settings.v1.GetDefinitionByNameResponse\x12\x8a\x01\n" +
	"\x0fListDefinitions\x12:.primandproper.platform.settings.v1.ListDefinitionsRequest\x1a;.primandproper.platform.settings.v1.ListDefinitionsResponse\x12\x8d\x01\n" +
	"\x10UpdateDefinition\x12;.primandproper.platform.settings.v1.UpdateDefinitionRequest\x1a<.primandproper.platform.settings.v1.UpdateDefinitionResponse\x12\x90\x01\n" +
	"\x11ArchiveDefinition\x12<.primandproper.platform.settings.v1.ArchiveDefinitionRequest\x1a=.primandproper.platform.settings.v1.ArchiveDefinitionResponse\x12\xa2\x01\n" +
	"\x17ListValuesForDefinition\x12B.primandproper.platform.settings.v1.ListValuesForDefinitionRequest\x1aC.primandproper.platform.settings.v1.ListValuesForDefinitionResponse\x12u\n" +
	"\bSetValue\x123.primandproper.platform.settings.v1.SetValueRequest\x1a4.primandproper.platform.settings.v1.SetValueResponse\x12u\n" +
	"\bGetValue\x123.primandproper.platform.settings.v1.GetValueRequest\x1a4.primandproper.platform.settings.v1.GetValueResponse\x12{\n" +
	"\n" +
	"ClearValue\x125.primandproper.platform.settings.v1.ClearValueRequest\x1a6.primandproper.platform.settings.v1.ClearValueResponse\x12\x99\x01\n" +
	"\x14ListValuesForSubject\x12?.primandproper.platform.settings.v1.ListValuesForSubjectRequest\x1a@.primandproper.platform.settings.v1.ListValuesForSubjectResponse\x12r\n" +
	"\aResolve\x122.primandproper.platform.settings.v1.ResolveRequest\x1a3.primandproper.platform.settings.v1.ResolveResponse\x12{\n" +
	"\n" +
	"ResolveAll\x125.primandproper.platform.settings.v1.ResolveAllRequest\x1a6.primandproper.platform.settings.v1.ResolveAllResponseBIZGgithub.com/primandproper/platform-go/v14/settings/settingspb;settingspbb\x06proto3"

var (
	file_primandproper_platform_settings_v1_settings_proto_rawDescOnce sync.Once
	file_primandproper_platform_settings_v1_settings_proto_rawDescData []byte
)

func file_primandproper_platform_settings_v1_settings_proto_rawDescGZIP() []byte {
	file_primandproper_platform_settings_v1_settings_proto_rawDescOnce.Do(func() {
		file_primandproper_platform_settings_v1_settings_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_primandproper_platform_settings_v1_settings_proto_rawDesc), len(file_primandproper_platform_settings_v1_settings_proto_rawDesc)))
	})
	return file_primandproper_platform_settings_v1_settings_proto_rawDescData
}

var file_primandproper_platform_settings_v1_settings_proto_enumTypes = make([]protoimpl.EnumInfo, 2)
var file_primandproper_platform_settings_v1_settings_proto_msgTypes = make([]protoimpl.MessageInfo, 32)
var file_primandproper_platform_settings_v1_settings_proto_goTypes = []any{
	(SettingKind)(0),                        // 0: primandproper.platform.settings.v1.SettingKind
	(ValueSource)(0),                        // 1: primandproper.platform.settings.v1.ValueSource
	(*TypedValue)(nil),                      // 2: primandproper.platform.settings.v1.TypedValue
	(*SettingSubject)(nil),                  // 3: primandproper.platform.settings.v1.SettingSubject
	(*SettingDefinition)(nil),               // 4: primandproper.platform.settings.v1.SettingDefinition
	(*SettingValue)(nil),                    // 5: primandproper.platform.settings.v1.SettingValue
	(*ResolvedSetting)(nil),                 // 6: primandproper.platform.settings.v1.ResolvedSetting
	(*SettingDefinitionInput)(nil),          // 7: primandproper.platform.settings.v1.SettingDefinitionInput
	(*CreateDefinitionRequest)(nil),         // 8: primandproper.platform.settings.v1.CreateDefinitionRequest
	(*CreateDefinitionResponse)(nil),        // 9: primandproper.platform.settings.v1.CreateDefinitionResponse
	(*GetDefinitionRequest)(nil),            // 10: primandproper.platform.settings.v1.GetDefinitionRequest
	(*GetDefinitionResponse)(nil),           // 11: primandproper.platform.settings.v1.GetDefinitionResponse
	(*GetDefinitionByNameRequest)(nil),      // 12: primandproper.platform.settings.v1.GetDefinitionByNameRequest
	(*GetDefinitionByNameResponse)(nil),     // 13: primandproper.platform.settings.v1.GetDefinitionByNameResponse
	(*ListDefinitionsRequest)(nil),          // 14: primandproper.platform.settings.v1.ListDefinitionsRequest
	(*ListDefinitionsResponse)(nil),         // 15: primandproper.platform.settings.v1.ListDefinitionsResponse
	(*UpdateDefinitionRequest)(nil),         // 16: primandproper.platform.settings.v1.UpdateDefinitionRequest
	(*UpdateDefinitionResponse)(nil),        // 17: primandproper.platform.settings.v1.UpdateDefinitionResponse
	(*ArchiveDefinitionRequest)(nil),        // 18: primandproper.platform.settings.v1.ArchiveDefinitionRequest
	(*ArchiveDefinitionResponse)(nil),       // 19: primandproper.platform.settings.v1.ArchiveDefinitionResponse
	(*ListValuesForDefinitionRequest)(nil),  // 20: primandproper.platform.settings.v1.ListValuesForDefinitionRequest
	(*ListValuesForDefinitionResponse)(nil), // 21: primandproper.platform.settings.v1.ListValuesForDefinitionResponse
	(*SetValueRequest)(nil),                 // 22: primandproper.platform.settings.v1.SetValueRequest
	(*SetValueResponse)(nil),                // 23: primandproper.platform.settings.v1.SetValueResponse
	(*GetValueRequest)(nil),                 // 24: primandproper.platform.settings.v1.GetValueRequest
	(*GetValueResponse)(nil),                // 25: primandproper.platform.settings.v1.GetValueResponse
	(*ClearValueRequest)(nil),               // 26: primandproper.platform.settings.v1.ClearValueRequest
	(*ClearValueResponse)(nil),              // 27: primandproper.platform.settings.v1.ClearValueResponse
	(*ListValuesForSubjectRequest)(nil),     // 28: primandproper.platform.settings.v1.ListValuesForSubjectRequest
	(*ListValuesForSubjectResponse)(nil),    // 29: primandproper.platform.settings.v1.ListValuesForSubjectResponse
	(*ResolveRequest)(nil),                  // 30: primandproper.platform.settings.v1.ResolveRequest
	(*ResolveResponse)(nil),                 // 31: primandproper.platform.settings.v1.ResolveResponse
	(*ResolveAllRequest)(nil),               // 32: primandproper.platform.settings.v1.ResolveAllRequest
	(*ResolveAllResponse)(nil),              // 33: primandproper.platform.settings.v1.ResolveAllResponse
	(*timestamppb.Timestamp)(nil),           // 34: google.protobuf.Timestamp
	(*filteringpb.QueryFilter)(nil),         // 35: primandproper.platform.filtering.v1.QueryFilter
	(*filteringpb.Pagination)(nil),          // 36: primandproper.platform.filtering.v1.Pagination
}
var file_primandproper_platform_settings_v1_settings_proto_depIdxs = []int32{
	34, // 0: primandproper.platform.settings.v1.SettingDefinition.created_at:type_name -> google.protobuf.Timestamp
	34, // 1: primandproper.platform.settings.v1.SettingDefinition.last_updated_at:type_name -> google.protobuf.Timestamp
	34, // 2: primandproper.platform.settings.v1.SettingDefinition.archived_at:type_name -> google.protobuf.Timestamp
	0,  // 3: primandproper.platform.settings.v1.SettingDefinition.kind:type_name -> primandproper.platform.settings.v1.SettingKind
	34, // 4: primandproper.platform.settings.v1.SettingValue.created_at:type_name -> google.protobuf.Timestamp
	34, // 5: primandproper.platform.settings.v1.SettingValue.last_updated_at:type_name -> google.protobuf.Timestamp
	34, // 6: primandproper.platform.settings.v1.SettingValue.archived_at:type_name -> google.protobuf.Timestamp
	3,  // 7: primandproper.platform.settings.v1.SettingValue.subject:type_name -> primandproper.platform.settings.v1.SettingSubject
	4,  // 8: primandproper.platform.settings.v1.ResolvedSetting.definition:type_name -> primandproper.platform.settings.v1.SettingDefinition
	5,  // 9: primandproper.platform.settings.v1.ResolvedSetting.value:type_name -> primandproper.platform.settings.v1.SettingValue
	2,  // 10: primandproper.platform.settings.v1.ResolvedSetting.typed_value:type_name -> primandproper.platform.settings.v1.TypedValue
	1,  // 11: primandproper.platform.settings.v1.ResolvedSetting.source:type_name -> primandproper.platform.settings.v1.ValueSource
	0,  // 12: primandproper.platform.settings.v1.SettingDefinitionInput.kind:type_name -> primandproper.platform.settings.v1.SettingKind
	7,  // 13: primandproper.platform.settings.v1.CreateDefinitionRequest.definition:type_name -> primandproper.platform.settings.v1.SettingDefinitionInput
	4,  // 14: primandproper.platform.settings.v1.CreateDefinitionResponse.result:type_name -> primandproper.platform.settings.v1.SettingDefinition
	4,  // 15: primandproper.platform.settings.v1.GetDefinitionResponse.result:type_name -> primandproper.platform.settings.v1.SettingDefinition
	4,  // 16: primandproper.platform.settings.v1.GetDefinitionByNameResponse.result:type_name -> primandproper.platform.settings.v1.SettingDefinition
	35, // 17: primandproper.platform.settings.v1.ListDefinitionsRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	36, // 18: primandproper.platform.settings.v1.ListDefinitionsResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	4,  // 19: primandproper.platform.settings.v1.ListDefinitionsResponse.results:type_name -> primandproper.platform.settings.v1.SettingDefinition
	7,  // 20: primandproper.platform.settings.v1.UpdateDefinitionRequest.definition:type_name -> primandproper.platform.settings.v1.SettingDefinitionInput
	4,  // 21: primandproper.platform.settings.v1.UpdateDefinitionResponse.result:type_name -> primandproper.platform.settings.v1.SettingDefinition
	35, // 22: primandproper.platform.settings.v1.ListValuesForDefinitionRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	36, // 23: primandproper.platform.settings.v1.ListValuesForDefinitionResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	5,  // 24: primandproper.platform.settings.v1.ListValuesForDefinitionResponse.results:type_name -> primandproper.platform.settings.v1.SettingValue
	3,  // 25: primandproper.platform.settings.v1.SetValueRequest.subject:type_name -> primandproper.platform.settings.v1.SettingSubject
	2,  // 26: primandproper.platform.settings.v1.SetValueRequest.value:type_name -> primandproper.platform.settings.v1.TypedValue
	6,  // 27: primandproper.platform.settings.v1.SetValueResponse.resolution:type_name -> primandproper.platform.settings.v1.ResolvedSetting
	3,  // 28: primandproper.platform.settings.v1.GetValueRequest.subject:type_name -> primandproper.platform.settings.v1.SettingSubject
	5,  // 29: primandproper.platform.settings.v1.GetValueResponse.result:type_name -> primandproper.platform.settings.v1.SettingValue
	3,  // 30: primandproper.platform.settings.v1.ClearValueRequest.subject:type_name -> primandproper.platform.settings.v1.SettingSubject
	6,  // 31: primandproper.platform.settings.v1.ClearValueResponse.resolution:type_name -> primandproper.platform.settings.v1.ResolvedSetting
	3,  // 32: primandproper.platform.settings.v1.ListValuesForSubjectRequest.subject:type_name -> primandproper.platform.settings.v1.SettingSubject
	35, // 33: primandproper.platform.settings.v1.ListValuesForSubjectRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	36, // 34: primandproper.platform.settings.v1.ListValuesForSubjectResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	5,  // 35: primandproper.platform.settings.v1.ListValuesForSubjectResponse.results:type_name -> primandproper.platform.settings.v1.SettingValue
	3,  // 36: primandproper.platform.settings.v1.ResolveRequest.subject:type_name -> primandproper.platform.settings.v1.SettingSubject
	6,  // 37: primandproper.platform.settings.v1.ResolveResponse.resolution:type_name -> primandproper.platform.settings.v1.ResolvedSetting
	3,  // 38: primandproper.platform.settings.v1.ResolveAllRequest.subject:type_name -> primandproper.platform.settings.v1.SettingSubject
	6,  // 39: primandproper.platform.settings.v1.ResolveAllResponse.resolutions:type_name -> primandproper.platform.settings.v1.ResolvedSetting
	8,  // 40: primandproper.platform.settings.v1.SettingsService.CreateDefinition:input_type -> primandproper.platform.settings.v1.CreateDefinitionRequest
	10, // 41: primandproper.platform.settings.v1.SettingsService.GetDefinition:input_type -> primandproper.platform.settings.v1.GetDefinitionRequest
	12, // 42: primandproper.platform.settings.v1.SettingsService.GetDefinitionByName:input_type -> primandproper.platform.settings.v1.GetDefinitionByNameRequest
	14, // 43: primandproper.platform.settings.v1.SettingsService.ListDefinitions:input_type -> primandproper.platform.settings.v1.ListDefinitionsRequest
	16, // 44: primandproper.platform.settings.v1.SettingsService.UpdateDefinition:input_type -> primandproper.platform.settings.v1.UpdateDefinitionRequest
	18, // 45: primandproper.platform.settings.v1.SettingsService.ArchiveDefinition:input_type -> primandproper.platform.settings.v1.ArchiveDefinitionRequest
	20, // 46: primandproper.platform.settings.v1.SettingsService.ListValuesForDefinition:input_type -> primandproper.platform.settings.v1.ListValuesForDefinitionRequest
	22, // 47: primandproper.platform.settings.v1.SettingsService.SetValue:input_type -> primandproper.platform.settings.v1.SetValueRequest
	24, // 48: primandproper.platform.settings.v1.SettingsService.GetValue:input_type -> primandproper.platform.settings.v1.GetValueRequest
	26, // 49: primandproper.platform.settings.v1.SettingsService.ClearValue:input_type -> primandproper.platform.settings.v1.ClearValueRequest
	28, // 50: primandproper.platform.settings.v1.SettingsService.ListValuesForSubject:input_type -> primandproper.platform.settings.v1.ListValuesForSubjectRequest
	30, // 51: primandproper.platform.settings.v1.SettingsService.Resolve:input_type -> primandproper.platform.settings.v1.ResolveRequest
	32, // 52: primandproper.platform.settings.v1.SettingsService.ResolveAll:input_type -> primandproper.platform.settings.v1.ResolveAllRequest
	9,  // 53: primandproper.platform.settings.v1.SettingsService.CreateDefinition:output_type -> primandproper.platform.settings.v1.CreateDefinitionResponse
	11, // 54: primandproper.platform.settings.v1.SettingsService.GetDefinition:output_type -> primandproper.platform.settings.v1.GetDefinitionResponse
	13, // 55: primandproper.platform.settings.v1.SettingsService.GetDefinitionByName:output_type -> primandproper.platform.settings.v1.GetDefinitionByNameResponse
	15, // 56: primandproper.platform.settings.v1.SettingsService.ListDefinitions:output_type -> primandproper.platform.settings.v1.ListDefinitionsResponse
	17, // 57: primandproper.platform.settings.v1.SettingsService.UpdateDefinition:output_type -> primandproper.platform.settings.v1.UpdateDefinitionResponse
	19, // 58: primandproper.platform.settings.v1.SettingsService.ArchiveDefinition:output_type -> primandproper.platform.settings.v1.ArchiveDefinitionResponse
	21, // 59: primandproper.platform.settings.v1.SettingsService.ListValuesForDefinition:output_type -> primandproper.platform.settings.v1.ListValuesForDefinitionResponse
	23, // 60: primandproper.platform.settings.v1.SettingsService.SetValue:output_type -> primandproper.platform.settings.v1.SetValueResponse
	25, // 61: primandproper.platform.settings.v1.SettingsService.GetValue:output_type -> primandproper.platform.settings.v1.GetValueResponse
	27, // 62: primandproper.platform.settings.v1.SettingsService.ClearValue:output_type -> primandproper.platform.settings.v1.ClearValueResponse
	29, // 63: primandproper.platform.settings.v1.SettingsService.ListValuesForSubject:output_type -> primandproper.platform.settings.v1.ListValuesForSubjectResponse
	31, // 64: primandproper.platform.settings.v1.SettingsService.Resolve:output_type -> primandproper.platform.settings.v1.ResolveResponse
	33, // 65: primandproper.platform.settings.v1.SettingsService.ResolveAll:output_type -> primandproper.platform.settings.v1.ResolveAllResponse
	53, // [53:66] is the sub-list for method output_type
	40, // [40:53] is the sub-list for method input_type
	40, // [40:40] is the sub-list for extension type_name
	40, // [40:40] is the sub-list for extension extendee
	0,  // [0:40] is the sub-list for field type_name
}

func init() { file_primandproper_platform_settings_v1_settings_proto_init() }
func file_primandproper_platform_settings_v1_settings_proto_init() {
	if File_primandproper_platform_settings_v1_settings_proto != nil {
		return
	}
	file_primandproper_platform_settings_v1_settings_proto_msgTypes[0].OneofWrappers = []any{
		(*TypedValue_StringValue)(nil),
		(*TypedValue_BoolValue)(nil),
		(*TypedValue_IntValue)(nil),
		(*TypedValue_FloatValue)(nil),
	}
	file_primandproper_platform_settings_v1_settings_proto_msgTypes[2].OneofWrappers = []any{}
	file_primandproper_platform_settings_v1_settings_proto_msgTypes[5].OneofWrappers = []any{}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_primandproper_platform_settings_v1_settings_proto_rawDesc), len(file_primandproper_platform_settings_v1_settings_proto_rawDesc)),
			NumEnums:      2,
			NumMessages:   32,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_primandproper_platform_settings_v1_settings_proto_goTypes,
		DependencyIndexes: file_primandproper_platform_settings_v1_settings_proto_depIdxs,
		EnumInfos:         file_primandproper_platform_settings_v1_settings_proto_enumTypes,
		MessageInfos:      file_primandproper_platform_settings_v1_settings_proto_msgTypes,
	}.Build()
	File_primandproper_platform_settings_v1_settings_proto = out.File
	file_primandproper_platform_settings_v1_settings_proto_goTypes = nil
	file_primandproper_platform_settings_v1_settings_proto_depIdxs = nil
}
