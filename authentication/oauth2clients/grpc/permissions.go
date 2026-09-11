package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// The permissions this service's administered methods require, in
// authorization's vocabulary.
//
// They are declared here rather than in the authorization package because
// authorization is a primitive: it owns what a Permission *is* — a string, a
// set, a role that inherits — and owns no domain's names.
//
// The strings are dotted and namespaced so that a consumer composing several
// domains' fragments cannot have two of them collide on "read". They are values
// rather than an enum because a consumer's policy is data — a YAML file, a table
// of roles — and it has to be able to name one without importing Go.
//
// The namespace is oauth2.clients rather than oauth2clients.clients, which would
// stutter, and it does not collide with authentication/oauth2server: that
// package permissions nothing, because every endpoint it serves is specified by
// an RFC and gated by a client credential rather than by a role.
const (
	// PermissionCreateClients covers minting a registration that belongs to no
	// person — the administered arrangement, where one client speaks for an
	// application on behalf of whoever signs in.
	//
	// It is the sharpest grant in this file, and every registration this service
	// mints is that arrangement. A holder can mint a credential that
	// [oauth2clients.Client.Admits] will let authorize anybody in the registry.
	PermissionCreateClients authorization.Permission = "oauth2.clients.create"

	// PermissionReadClients covers reading or paging the registry's
	// registrations.
	//
	// One permission for the get and the list, because they answer the same
	// question at two cardinalities and a grant that separated them would let a
	// consumer allow enumeration while forbidding the read it enumerates into.
	// A consumer who wants the page behind a stronger grant than the get
	// overrides the map, which is what [Permissions] returning a fresh one is
	// for.
	PermissionReadClients authorization.Permission = "oauth2.clients.read"

	// PermissionArchiveClients covers withdrawing a registration.
	//
	// There is deliberately no oauth2.clients.update beside it. This service
	// serves no revision RPC — no consumer asked for one — and a permission
	// naming a method that does not exist is a grant a consumer's policy hands
	// out for nothing. A deployment that revises registrations does so through
	// oauth2clients.Service.UpdateClient and names the grant in front of its own
	// transport.
	PermissionArchiveClients authorization.Permission = "oauth2.clients.archive"
)

// Permissions is the default map from method name to what it requires: every
// RPC this service declares, and nothing else.
//
// Every method is in it. There is no second set of methods that require nothing
// — this service serves no RPC a caller reaches without a grant — so a method
// missing from this map is a bug rather than a decision, and the suite reads the
// service descriptor rather than a list in order to say so.
//
// The keys are the generated full method name constants rather than strings, so
// an RPC renamed in the .proto is a compile error here instead of a method that
// silently requires nothing.
//
// It returns a fresh map each call, so a consumer composing it into their own
// policy and then overriding an entry is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		oauth2clientspb.OAuth2ClientsService_CreateOAuth2Client_FullMethodName:  {PermissionCreateClients},
		oauth2clientspb.OAuth2ClientsService_GetOAuth2Client_FullMethodName:     {PermissionReadClients},
		oauth2clientspb.OAuth2ClientsService_ListOAuth2Clients_FullMethodName:   {PermissionReadClients},
		oauth2clientspb.OAuth2ClientsService_ArchiveOAuth2Client_FullMethodName: {PermissionArchiveClients},
	}
}

// Require declares every method of this service on a requirements builder.
//
// It is one line over Permissions today and is the exported name anyway, because
// authorization/grpc is fail-closed: a method declared nowhere is denied, and
// what a consumer needs is a call that stays correct when this service's method
// set changes. An earlier revision of this package declared five further methods
// as public here; a consumer who had written the RequireAll themselves would have
// had to notice.
//
// A nil builder is tolerated and returns nil, so composing several domains'
// fragments in a chain does not need a nil check per link.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	return b.RequireAll(Permissions())
}
