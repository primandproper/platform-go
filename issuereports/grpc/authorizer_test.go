package grpc_test

import (
	"testing"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"

	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
)

// TestReporterAuthorizer covers the narrow half of every rule a deployment
// writes, which is what this type is exported to be.
//
// It is not the default — there is none — so what these cases pin is that a
// consumer passing it gets a surface where a person reaches their own reports
// and nothing else, and that composing it leaves that half intact.
func TestReporterAuthorizer(T *testing.T) {
	T.Parallel()

	var rule issuereportsgrpc.ReportAuthorizer = issuereportsgrpc.ReporterAuthorizer{}

	caller := &testPrincipal{userID: testReporter, scope: testScope}

	T.Run("a report the caller filed is permitted", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, rule.AuthorizeReport(t.Context(), caller,
			&issuereports.Report{Reporter: testReporter}))
	})

	T.Run("somebody else's report is refused", func(t *testing.T) {
		t.Parallel()

		err := rule.AuthorizeReport(t.Context(), caller, &issuereports.Report{Reporter: otherReporter})
		test.ErrorIs(t, err, issuereportsgrpc.ErrTargetNotPermitted)
	})

	T.Run("the caller's own name is permitted and any other is refused", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, rule.AuthorizeReporter(t.Context(), caller, testReporter))
		test.ErrorIs(t, rule.AuthorizeReporter(t.Context(), caller, otherReporter),
			issuereportsgrpc.ErrTargetNotPermitted)
	})

	T.Run("nothing matches nothing", func(t *testing.T) {
		t.Parallel()

		// A caller with no identifier is refused rather than matched against a
		// report filed by nobody. The store refuses to write one of those, but a
		// rule that compared two empty strings and permitted would turn an
		// unauthenticated-looking principal into a key.
		anonymous := &testPrincipal{scope: testScope}

		test.ErrorIs(t, rule.AuthorizeReport(t.Context(), anonymous, &issuereports.Report{}),
			issuereportsgrpc.ErrTargetNotPermitted)
		test.ErrorIs(t, rule.AuthorizeReporter(t.Context(), anonymous, ""),
			issuereportsgrpc.ErrTargetNotPermitted)

		// And a caller who is nobody at all, which is what a consumer's own
		// composition can hand this rule when their grant check runs first.
		test.ErrorIs(t, rule.AuthorizeReport(t.Context(), nil, &issuereports.Report{Reporter: testReporter}),
			issuereportsgrpc.ErrTargetNotPermitted)
		test.ErrorIs(t, rule.AuthorizeReporter(t.Context(), nil, testReporter),
			issuereportsgrpc.ErrTargetNotPermitted)
	})

	T.Run("a nil report is refused rather than panicking", func(t *testing.T) {
		t.Parallel()

		// Unreachable from this surface, where the row has already been read.
		// A consumer's own handler is a different matter.
		test.ErrorIs(t, rule.AuthorizeReport(t.Context(), caller, nil),
			issuereportsgrpc.ErrTargetNotPermitted)
	})

	T.Run("the rule is about the person and not the tenant", func(t *testing.T) {
		t.Parallel()

		// The scope comes off the principal and every store read filters on it,
		// so this rule never has to ask: a report from another tenant is one this
		// caller's reads cannot reach in the first place.
		elsewhere := &testPrincipal{userID: testReporter, scope: tenancy.Of("tenant_3")}

		test.NoError(t, rule.AuthorizeReport(t.Context(), elsewhere,
			&issuereports.Report{Reporter: testReporter, Scope: testScope}))
	})
}

// TestErrTargetNotPermittedIsTheDirectorysSentinel is what lets a consumer hand
// one rule to two surfaces.
//
// A second sentinel of this package's own would mean an authorizer written for
// identity/grpc refusing this one with an error nothing here recognizes — which
// reaches a client as codes.Internal rather than as the refusal it is.
func TestErrTargetNotPermittedIsTheDirectorysSentinel(T *testing.T) {
	T.Parallel()

	test.EqOp(T, identitygrpc.ErrTargetNotPermitted, issuereportsgrpc.ErrTargetNotPermitted)
}
