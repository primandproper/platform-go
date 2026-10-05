package grpc

import (
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	"github.com/primandproper/platform-go/v14/rbac"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// The permissions this service's methods require, in authorization's vocabulary.
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
// The namespace is issues.reports rather than issuereports.reports, which would
// stutter, and it is the reading authentication/oauth2clients/grpc already took
// of the same choice. Nothing else in this module claims "issues".
//
// The shape of this file is the table's two audiences. Filing, reading a report
// and reading one person's list are what a reporter does; paging the queue,
// revising, moving and archiving are what a triager does. The two never collapse
// into one grant, because the queue is every one of your users' words about
// their own experience and a person's own list is theirs. The operator reading
// across tenants is a third, with one grant of its own and nobody holding it by
// default.
const (
	// PermissionFileReports covers filing a report.
	//
	// It is the grant a deployment gives every signed-in caller, and it is still
	// a grant: a report is a row somebody else has to read, so a deployment that
	// wants filing gated behind a verified address or a paid plan names this
	// permission and gives it to fewer people.
	PermissionFileReports authorization.Permission = "issues.reports.file"

	// PermissionReadReports covers reading one report and paging the reports one
	// person filed.
	//
	// One permission for the two, because they answer the same question at two
	// cardinalities and a grant that separated them would let a consumer allow
	// enumeration while forbidding the read it enumerates into.
	//
	// It says the caller may perform this kind of read. *Whose* reports they may
	// perform it against is [ReportAuthorizer]'s, asked inside the handler where
	// the store is, and both have to say yes — which is why this is the grant a
	// reporter and a triager both hold, and why holding it is not a directory of
	// everyone's complaints.
	PermissionReadReports authorization.Permission = "issues.reports.read"

	// PermissionTriageReports covers paging the queue: whole, by status, by what
	// a report is about, or about one particular thing.
	//
	// It is the sharpest read grant in this file and the one that is not
	// row-gated, because its target is the queue rather than a person: a holder
	// pages every report in the tenant. That is what a triage console is, and
	// spelling it as a grant is what lets a consumer's policy audit who has one.
	PermissionTriageReports authorization.Permission = "issues.reports.triage"

	// PermissionReadAnyReports covers paging every tenant's reports: the
	// operator's queue, whole or by status.
	//
	// It is the one grant here whose reach is not the caller's tenant, and this
	// module grants it to nobody. The scoped reads cannot be widened into it —
	// the two RPCs it covers are separate methods, and the store reads behind
	// them are the stated exception to "no read path omits the scope" — so a
	// deployment that gives it to its operators has decided to, in its own
	// policy, where an audit of who holds it finds the decision.
	//
	// Separate from PermissionTriageReports because a triager works one tenant's
	// queue and this reads everybody's. A deployment whose tenants run their
	// own triage grants the first to them and this to nobody outside its staff.
	PermissionReadAnyReports authorization.Permission = "issues.reports.read_any"

	// PermissionUpdateReports covers revising what a report says — the kind, the
	// details, and what it is about.
	//
	// Separate from PermissionTransitionReports because they are different acts:
	// deciding a report is not the same as rewriting it, and a deployment that
	// lets its triagers do the first without the second is the ordinary
	// arrangement rather than an unusual one.
	PermissionUpdateReports authorization.Permission = "issues.reports.update"

	// PermissionTransitionReports covers moving a report through the lifecycle:
	// acknowledging, resolving, declining and reopening.
	//
	// One permission for the four moves rather than one each. Which moves are
	// admitted is the lifecycle's answer and not a policy's — see
	// issuereports.Status.CanTransitionTo — so a grant per move would be a
	// consumer's policy restating a table this module already enforces, and
	// getting it wrong would mean a report somebody may resolve and nobody may
	// reopen.
	PermissionTransitionReports authorization.Permission = "issues.reports.transition"

	// PermissionArchiveReports covers removing a report from the queue.
	//
	// It is not closing: closing is a status a triager moves to and this hides
	// the row, which is what a test submission or a duplicate somebody filed
	// twice by refreshing wants. The row stays for whoever asks later what was
	// reported.
	PermissionArchiveReports authorization.Permission = "issues.reports.archive"
)

// Permissions is the default map from method name to what it requires: every RPC
// this service declares, and nothing else.
//
// Every method is in it. There is no second set of methods that require nothing
// — this service serves no RPC a caller reaches without a grant, filing included
// — so a method missing from this map is a bug rather than a decision, and the
// suite reads the service descriptor rather than a list in order to say so.
//
// The keys are the generated full method name constants rather than strings, so
// an RPC renamed in the .proto is a compile error here instead of a method that
// silently requires nothing.
//
// It returns a fresh map each call, so a consumer composing it into their own
// policy and then overriding an entry is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		issuereportspb.IssueReportsService_CreateReport_FullMethodName:          {PermissionFileReports},
		issuereportspb.IssueReportsService_GetReport_FullMethodName:             {PermissionReadReports},
		issuereportspb.IssueReportsService_ListReportsByReporter_FullMethodName: {PermissionReadReports},

		issuereportspb.IssueReportsService_ListReports_FullMethodName:              {PermissionTriageReports},
		issuereportspb.IssueReportsService_ListReportsByStatus_FullMethodName:      {PermissionTriageReports},
		issuereportspb.IssueReportsService_ListReportsBySubjectType_FullMethodName: {PermissionTriageReports},
		issuereportspb.IssueReportsService_ListReportsForSubject_FullMethodName:    {PermissionTriageReports},

		issuereportspb.IssueReportsService_ListReportsAcrossScopes_FullMethodName:         {PermissionReadAnyReports},
		issuereportspb.IssueReportsService_ListReportsByStatusAcrossScopes_FullMethodName: {PermissionReadAnyReports},

		issuereportspb.IssueReportsService_UpdateReport_FullMethodName:     {PermissionUpdateReports},
		issuereportspb.IssueReportsService_TransitionReport_FullMethodName: {PermissionTransitionReports},
		issuereportspb.IssueReportsService_ArchiveReport_FullMethodName:    {PermissionArchiveReports},
	}
}

// Require declares every method of this service on a requirements builder.
//
// It is one line over [Permissions] today and is the exported name anyway,
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

// Tiers sorts this service's permissions by the kind of principal that should
// hold them. A deployment composes its policy from these with rbac.MergeTiers
// and rbac.PolicyFromTiers; the role names are its own.
//
// The table's two audiences are two tiers. Filing a report and reading one's
// own are a member's, and [ReportAuthorizer] keeps the read to the caller's
// reports. Paging, revising, moving and archiving the queue are the tenant's
// triager's. Reading every tenant's queue is an operator's.
func Tiers() rbac.Tiers {
	return rbac.Tiers{
		Operator: []authorization.Permission{PermissionReadAnyReports},
		TenantAdmin: []authorization.Permission{
			PermissionTriageReports,
			PermissionUpdateReports,
			PermissionTransitionReports,
			PermissionArchiveReports,
		},
		Member: []authorization.Permission{PermissionFileReports, PermissionReadReports},
	}
}
