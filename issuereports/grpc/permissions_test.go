package grpc_test

import (
	"slices"
	"strings"
	"testing"

	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// serviceMethods is every RPC the generated service descriptor declares, as the
// full method names the interceptor matches on.
//
// It is read off the descriptor rather than written down, which is the whole
// point: a list here would be a third place to forget an RPC, and the tests
// below exist because the first two are easy to forget.
func serviceMethods() []string {
	prefix := "/" + issuereportspb.IssueReportsService_ServiceDesc.ServiceName + "/"

	out := make([]string, 0, len(issuereportspb.IssueReportsService_ServiceDesc.Methods))
	for _, m := range issuereportspb.IssueReportsService_ServiceDesc.Methods {
		out = append(out, prefix+m.MethodName)
	}

	return out
}

// TestEveryMethodIsPermissioned is the decision this service made rather than a
// property it happens to have: there is no second set of methods here that
// require nothing, filing included.
//
// It fails in the direction that matters most. An RPC added later and left out
// of the map is a method a consumer's fail-closed enforcer denies in production,
// for a reason nobody would connect to this package.
func TestEveryMethodIsPermissioned(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("no methods on the service descriptor, so this asserted nothing"))

	permissions := issuereportsgrpc.Permissions()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, permissioned := permissions[method]
			test.True(t, permissioned, test.Sprintf(
				"%s is not in Permissions, so nothing says who may call it", method))
		})
	}
}

// TestNoDecisionOutlivesItsMethod is the other direction: a decision naming an
// RPC that no longer exists is a permission a consumer is still granting for
// nothing, and a rename would leave the real method undeclared and therefore
// denied.
func TestNoDecisionOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()

	for method := range issuereportsgrpc.Permissions() {
		test.True(T, slices.Contains(methods, method), test.Sprintf(
			"Permissions names %s, which the service descriptor does not declare", method))
	}
}

// TestNoPermissionIsEmpty catches the decision that looks made and is not: an
// entry mapping a method to an empty slice declares it and requires nothing.
func TestNoPermissionIsEmpty(T *testing.T) {
	T.Parallel()

	for method, permissions := range issuereportsgrpc.Permissions() {
		test.SliceNotEmpty(T, permissions, test.Sprintf(
			"%s is declared with no permissions, which grants it to everybody", method))
	}
}

// TestTheTwoAudiencesDoNotShareAGrant is the shape of this file asserted rather
// than described.
//
// Reading your own reports and paging everybody's are different grants, and a
// policy that could not separate them would hand out the whole queue — every one
// of your users' words about their own experience — to get somebody their own
// list back. So the reporter-facing reads and the queue listings must never
// require the same permission.
func TestTheTwoAudiencesDoNotShareAGrant(T *testing.T) {
	T.Parallel()

	permissions := issuereportsgrpc.Permissions()

	reporterFacing := []string{
		issuereportspb.IssueReportsService_GetReport_FullMethodName,
		issuereportspb.IssueReportsService_ListReportsByReporter_FullMethodName,
	}

	queueFacing := []string{
		issuereportspb.IssueReportsService_ListReports_FullMethodName,
		issuereportspb.IssueReportsService_ListReportsByStatus_FullMethodName,
		issuereportspb.IssueReportsService_ListReportsBySubjectType_FullMethodName,
		issuereportspb.IssueReportsService_ListReportsForSubject_FullMethodName,
	}

	for _, reporterMethod := range reporterFacing {
		for _, queueMethod := range queueFacing {
			for _, granted := range permissions[reporterMethod] {
				test.SliceNotContains(T, permissions[queueMethod], granted, test.Sprintf(
					"%s and %s require %q in common, so one grant reaches both audiences",
					reporterMethod, queueMethod, granted))
			}
		}
	}
}

// TestTheLifecycleWritesAreSeparatelyGranted keeps three different acts from
// arriving as one grant: revising what somebody said, deciding their report, and
// taking it off the queue.
func TestTheLifecycleWritesAreSeparatelyGranted(T *testing.T) {
	T.Parallel()

	permissions := issuereportsgrpc.Permissions()

	writes := []string{
		issuereportspb.IssueReportsService_UpdateReport_FullMethodName,
		issuereportspb.IssueReportsService_TransitionReport_FullMethodName,
		issuereportspb.IssueReportsService_ArchiveReport_FullMethodName,
	}

	seen := map[string]string{}

	for _, method := range writes {
		for _, granted := range permissions[method] {
			previous, collides := seen[string(granted)]
			test.False(T, collides, test.Sprintf(
				"%s and %s both require %q, so one grant performs two different acts",
				previous, method, granted))

			seen[string(granted)] = method
		}
	}
}

// TestPermissionsAreNamespaced keeps a consumer composing several domains'
// fragments from having two of them collide on "read".
func TestPermissionsAreNamespaced(T *testing.T) {
	T.Parallel()

	for method, permissions := range issuereportsgrpc.Permissions() {
		for _, granted := range permissions {
			test.True(T, strings.HasPrefix(string(granted), "issues.reports."), test.Sprintf(
				"%s requires %q, which is outside this package's namespace", method, granted))
		}
	}
}

// TestPermissionsReturnsAFreshMap keeps a consumer's override from editing every
// other consumer's copy in the same process.
func TestPermissionsReturnsAFreshMap(T *testing.T) {
	T.Parallel()

	first := issuereportsgrpc.Permissions()
	for method := range first {
		delete(first, method)
	}

	test.MapNotEmpty(T, issuereportsgrpc.Permissions(),
		test.Sprint("Permissions handed back a map a caller could empty for everybody"))
}

// TestRequireDeclaresEveryMethod is the check a consumer writing the RequireAll
// themselves would not have. authorization/grpc is fail-closed, so a method
// Require left out is denied, and a denial for want of a declaration looks
// exactly like a policy somebody meant.
func TestRequireDeclaresEveryMethod(T *testing.T) {
	T.Parallel()

	reqs, err := issuereportsgrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(T, err)

	declared := reqs.Methods()

	for _, method := range serviceMethods() {
		test.SliceContains(T, declared, method, test.Sprintf(
			"%s is not declared after Require, so the enforcer will deny it as undeclared", method))
	}
}

// TestRequireToleratesANilBuilder keeps a chain composing several domains'
// fragments from needing a nil check per link.
func TestRequireToleratesANilBuilder(T *testing.T) {
	T.Parallel()

	test.Nil(T, issuereportsgrpc.Require(nil))
}
