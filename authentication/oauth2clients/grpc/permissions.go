package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/authorization"
	authzgrpc "github.com/primandproper/platform-go/v14/authorization/grpc"
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
	// It is the sharpest grant in this file. A holder can mint a credential that
	// [oauth2clients.Client.Admits] will let authorize anybody in the registry,
	// which is why creating one is permissioned and creating your own is not.
	PermissionCreateClients authorization.Permission = "oauth2.clients.create"

	// PermissionReadClients covers reading or paging registrations that are not
	// the caller's own.
	//
	// One permission for the get and the list, because they answer the same
	// question at two cardinalities and a grant that separated them would let a
	// consumer allow enumeration while forbidding the read it enumerates into.
	// A consumer who wants the page behind a stronger grant than the get
	// overrides the map, which is what [Permissions] returning a fresh one is
	// for.
	PermissionReadClients authorization.Permission = "oauth2.clients.read"

	// PermissionUpdateClients covers revising a registration that is not the
	// caller's own.
	//
	// Separate from PermissionArchiveClients, and deliberately: revising a
	// client's redirect URIs is a routine correction, and withdrawing one breaks
	// every integration using it. Collapsing the two would make the first imply
	// the second.
	PermissionUpdateClients authorization.Permission = "oauth2.clients.update"

	// PermissionArchiveClients covers withdrawing a registration that is not the
	// caller's own.
	PermissionArchiveClients authorization.Permission = "oauth2.clients.archive"
)

// Permissions is the default map from method name to what it requires: the five
// administered RPCs, and nothing else.
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
		oauth2clientspb.OAuth2ClientsService_UpdateOAuth2Client_FullMethodName:  {PermissionUpdateClients},
		oauth2clientspb.OAuth2ClientsService_ArchiveOAuth2Client_FullMethodName: {PermissionArchiveClients},
	}
}

// SelfServiceMethods are the five RPCs that require no permission.
//
// Not because they are unauthenticated — every one of them refuses a caller with
// no principal — but because owning the row *is* the authorization. Each of them
// reads both the registry and the owner off the principal, so there is nothing
// in any of these requests that could name somebody else's registration, and a
// grant in front of them would be a grant that says "you may manage your own",
// which is not a decision a consumer's policy should have to make.
func SelfServiceMethods() []string {
	return []string{
		oauth2clientspb.OAuth2ClientsService_CreateOwnOAuth2Client_FullMethodName,
		oauth2clientspb.OAuth2ClientsService_GetOwnOAuth2Client_FullMethodName,
		oauth2clientspb.OAuth2ClientsService_ListOwnOAuth2Clients_FullMethodName,
		oauth2clientspb.OAuth2ClientsService_UpdateOwnOAuth2Client_FullMethodName,
		oauth2clientspb.OAuth2ClientsService_ArchiveOwnOAuth2Client_FullMethodName,
	}
}

// Require declares every method of this service on a requirements builder: the
// five administered ones with their permissions, and the five self-service ones
// as public.
//
// It exists because the two-step version — RequireAll(Permissions()) and then a
// loop over SelfServiceMethods() — is one a caller can do half of, and
// authorization/grpc is fail-closed: a method declared nowhere is denied, so
// forgetting the second step turns every self-service call into a refusal that
// looks like a policy decision somebody made.
//
// A nil builder is tolerated and returns nil, so composing several domains'
// fragments in a chain does not need a nil check per link.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	b = b.RequireAll(Permissions())

	for _, method := range SelfServiceMethods() {
		b = b.Public(method)
	}

	return b
}
