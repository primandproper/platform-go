package grpc

import (
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// The permissions this service's methods require, in authorization's
// vocabulary.
//
// They are declared here rather than in the authorization package because
// authorization is a primitive: it owns what a Permission *is* — a string, a
// set, a role that inherits — and owns no domain's names.
//
// The strings are dotted and namespaced so that a consumer composing several
// domains' fragments cannot have two of them collide on "read". They are values
// rather than an enum because a consumer's policy is data — a YAML file, a
// table of roles — and it has to be able to name one without importing Go.
//
// There are seven of them over thirteen RPCs, and the shape of the collapse is
// the two audiences this package has. Four grants cover the catalog, which is
// an operator's, and three cover the answers, which are a person's own. Within
// each, a get and its list share one grant: they answer the same question at
// two cardinalities, and a grant that separated them would let a consumer allow
// enumeration while forbidding the read it enumerates into. A consumer who
// wants a page behind a stronger grant than its get overrides the map, which is
// what [Permissions] returning a fresh one is for.
//
// None of them says whose settings. That is the second half of authorization
// and it is [SubjectAuthorizer]'s, asked inside the handler where the request
// body has been parsed — a grant on SetValue is not a grant to write anybody's.
const (
	// PermissionCreateDefinitions covers adding a setting to the catalog.
	//
	// It is an administrative grant in the sense settings.Definition means:
	// defining a setting is a deployment's decision, in the same sense that a
	// database column is, and nothing on a request path does it.
	PermissionCreateDefinitions authorization.Permission = "settings.definitions.create"

	// PermissionReadDefinitions covers reading a definition by id or by name and
	// paging the catalog.
	//
	// It is the read a settings screen makes before it renders anything: what
	// exists, what kind each is, what it falls back to and which values it
	// admits. A deployment whose catalog is not a secret grants it widely; the
	// answers stored against it are a different grant.
	PermissionReadDefinitions authorization.Permission = "settings.definitions.read"

	// PermissionUpdateDefinitions covers rewriting a definition.
	//
	// It is the sharpest grant in this file, and it is sharper than "edit a row"
	// looks. Narrowing an enumeration or changing a kind decides how every value
	// already stored is read, which is why the store refuses an edit some live
	// value no longer satisfies — settings.ErrStrandedValues — rather than
	// leaving rows that exist, resolve and fail to parse. A holder is deciding
	// for every subject who has already answered.
	PermissionUpdateDefinitions authorization.Permission = "settings.definitions.update"

	// PermissionArchiveDefinitions covers retiring a setting. It is lighter than
	// an update: the values stored against it are left alone, the name stays
	// claimed, and nothing already written is reinterpreted.
	PermissionArchiveDefinitions authorization.Permission = "settings.definitions.archive"

	// PermissionReadValues covers reading a subject's own answers: the stored
	// row, the page of everything they have answered, and the two resolutions.
	//
	// It is on the method and says nothing about whose — see
	// [SubjectAuthorizer], which is what stops it from being a grant to read
	// everybody's.
	PermissionReadValues authorization.Permission = "settings.values.read"

	// PermissionWriteValues covers a subject answering a setting and taking the
	// answer back.
	//
	// Set and clear share one grant because they are one capability: a
	// deployment that let somebody choose a value and not return to the default
	// would be one where the only way out of a choice is another choice.
	PermissionWriteValues authorization.Permission = "settings.values.write"

	// PermissionReadAllValues covers paging everyone who has answered one
	// setting.
	//
	// It is a different grant from PermissionReadValues because it answers a
	// different question, to a different person: not "what did I choose" but
	// "who has overridden this", across every subject in the scope. It is the
	// read an administrator makes before narrowing an enumeration, since it is
	// the same walk the update runs to decide whether the edit strands
	// anything.
	//
	// It is also the one value-side RPC no [SubjectAuthorizer] gates, because it
	// names no subject to gate — which is exactly why it is its own grant.
	PermissionReadAllValues authorization.Permission = "settings.values.read.all"
)

// Permissions is the default map from method name to what it requires: every
// RPC this service declares, and nothing else.
//
// Every method is in it. There is no second set of methods that require nothing
// — this service serves no RPC a caller reaches without a grant — so a method
// missing from this map is a bug rather than a decision, and the suite reads
// the service descriptor rather than a list in order to say so.
//
// The keys are the generated full method name constants rather than strings, so
// an RPC renamed in the .proto is a compile error here instead of a method that
// silently requires nothing.
//
// It returns a fresh map each call, so a consumer composing it into their own
// policy and then overriding an entry is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		settingspb.SettingsService_CreateDefinition_FullMethodName:        {PermissionCreateDefinitions},
		settingspb.SettingsService_GetDefinition_FullMethodName:           {PermissionReadDefinitions},
		settingspb.SettingsService_GetDefinitionByName_FullMethodName:     {PermissionReadDefinitions},
		settingspb.SettingsService_ListDefinitions_FullMethodName:         {PermissionReadDefinitions},
		settingspb.SettingsService_UpdateDefinition_FullMethodName:        {PermissionUpdateDefinitions},
		settingspb.SettingsService_ArchiveDefinition_FullMethodName:       {PermissionArchiveDefinitions},
		settingspb.SettingsService_ListValuesForDefinition_FullMethodName: {PermissionReadAllValues},

		settingspb.SettingsService_SetValue_FullMethodName:             {PermissionWriteValues},
		settingspb.SettingsService_GetValue_FullMethodName:             {PermissionReadValues},
		settingspb.SettingsService_ClearValue_FullMethodName:           {PermissionWriteValues},
		settingspb.SettingsService_ListValuesForSubject_FullMethodName: {PermissionReadValues},
		settingspb.SettingsService_Resolve_FullMethodName:              {PermissionReadValues},
		settingspb.SettingsService_ResolveAll_FullMethodName:           {PermissionReadValues},
	}
}

// Require declares every method of this service on a requirements builder.
//
// It is one line over Permissions today and is the exported name anyway,
// because authorization/grpc is fail-closed: a method declared nowhere is
// denied, and what a consumer needs is a call that stays correct when this
// service's method set changes.
//
// A nil builder is tolerated and returns nil, so composing several domains'
// fragments in a chain does not need a nil check per link.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	return b.RequireAll(Permissions())
}
