package grpc

import (
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/rbac"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// The permissions this service's methods require, in authorization's
// vocabulary.
//
// They are declared here rather than in the authorization package because
// authorization is a primitive: it owns what a Permission *is* — a string, a
// set, a role that inherits — and owns no domain's names. "audit.entries.read"
// means something only to a log, so it is spelled beside the log.
//
// The strings are dotted and namespaced so that a consumer composing several
// domains' fragments cannot have two of them collide on "read". They are values
// rather than an enum because a consumer's policy is data — a YAML file, a table
// of roles — and it has to be able to name one without importing Go.
const (
	// PermissionReadEntries covers reading one entry and paging the log.
	PermissionReadEntries authorization.Permission = "audit.entries.read"

	// PermissionVerifyChain covers walking the chain and reporting a break.
	//
	// It is separate from PermissionReadEntries, and the separation is the
	// useful half of this file. A verification answers "has this log been
	// tampered with" and carries no entry's content — no actor, no resource, no
	// changed field — so it is the grant to give a monitor that runs on a
	// schedule and pages somebody, without also giving it the ability to read
	// what everybody did. Collapsing the two would make an alerting job a reader
	// of the audit log, which is the account most likely to be long-lived and
	// least likely to be reviewed.
	PermissionVerifyChain authorization.Permission = "audit.chain.verify"

	// PermissionReadAnyEntries covers AuditAdministrationService: reading an
	// entry in any tenant's chain, and paging every tenant's log.
	//
	// No role holds it by default, and nothing here says who should. Whether
	// somebody may audit the whole deployment is the deployment's policy to
	// decide, and this is the name that policy grants — on a method of its own,
	// so that holding it changes nothing about what AuditService answers the
	// same person reading their own log. Every call it admits is recorded; see
	// WithOperatorRecorder.
	PermissionReadAnyEntries authorization.Permission = "audit.entries.read_any"
)

// Permissions is the default map from this service's methods to what each
// requires: the fragment a consumer composes into its own policy.
//
// It is a default and not a rule. A consumer that wants the page open to every
// member of an account overrides the entry — the map is theirs once they have
// it, and authorization/grpc's builder takes whatever they hand it.
//
// Unlike identity/grpc's, it is the whole of what gates this surface, and
// nothing here is a second question about a row. Every AuditService RPC is
// against the one scope the connection resolved: there is no target on a
// request that a permission cannot speak about, because the only id a request
// carries is an entry's, and an entry outside the caller's scope is answered as
// absent before anything is read out of it. AuditAdministrationService's RPCs
// name any tenant, and that is what [PermissionReadAnyEntries] on the method is
// for: the reach is granted by the method, so there is no row left to ask about.
//
// The keys are the generated full method names, which is the form
// grpc.UnaryServerInfo.FullMethod carries and the form RequirementsBuilder
// matches on. Spelling them as the generated constants rather than as string
// literals is what makes a renamed RPC a compile error here instead of a method
// that silently stops being checked.
//
// The map is exhaustive over both services — there are no public methods and no
// self-service ones, since a log has no notion of a caller's own row — and
// permissions_test.go is what keeps it that way. An RPC added later and decided
// about nowhere fails there rather than being denied in somebody's production by
// the enforcer's fail-closed rule.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		auditpb.AuditService_GetEntry_FullMethodName:    {PermissionReadEntries},
		auditpb.AuditService_ListEntries_FullMethodName: {PermissionReadEntries},
		auditpb.AuditService_VerifyChain_FullMethodName: {PermissionVerifyChain},

		auditpb.AuditAdministrationService_GetAnyEntry_FullMethodName:    {PermissionReadAnyEntries},
		auditpb.AuditAdministrationService_ListAnyEntries_FullMethodName: {PermissionReadAnyEntries},
	}
}

// Require declares every one of this service's methods onto a requirements
// builder, each with what it needs.
//
// It takes and returns the builder rather than building it, so a consumer
// composes several domains and their own methods into one table:
//
//	reqs, err := auditgrpc.Require(identitygrpc.Require(authzgrpc.NewRequirements())).
//		RequireAll(newsletters.Permissions()).
//		Public(healthpb.Health_Check_FullMethodName).
//		Build()
//
// Overriding stays available: this is the default fragment, and a consumer who
// wants VerifyChain open to every operator declares it differently before
// Build — a method declared twice is ErrDuplicateMethod, so the override is
// building the map yourself rather than calling this and amending it.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	b.RequireAll(Permissions())

	return b
}

// Tiers sorts this service's permissions by the kind of principal that should
// hold them. A deployment composes its policy from these with rbac.MergeTiers
// and rbac.PolicyFromTiers; the role names are its own.
//
// Reading a tenant's log is its admin's, because the log is what everybody in
// the tenant did. Verifying the chain is too: it carries no entry's content,
// but it is a question about the whole tenant's log rather than about the
// caller. Reading any tenant's log is an operator's.
func Tiers() rbac.Tiers {
	return rbac.Tiers{
		Operator:    []authorization.Permission{PermissionReadAnyEntries},
		TenantAdmin: []authorization.Permission{PermissionReadEntries, PermissionVerifyChain},
	}
}
