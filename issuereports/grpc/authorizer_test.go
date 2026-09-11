package grpc_test

import (
	"context"
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

// consoleAuthorizer is the composition [issuereportsgrpc.ReportAuthorizer]'s
// documentation asks a deployment with a triage console to write: the narrow
// half embedded, the question the console widens answered differently, and the
// rest inherited.
//
// It is what makes this seam's ruling the opposite of the principal's. The
// method set here may grow, because an implementation in this shape already
// answers whatever is added.
type consoleAuthorizer struct {
	issuereportsgrpc.ReporterAuthorizer

	triager string
}

var _ issuereportsgrpc.ReportAuthorizer = consoleAuthorizer{}

// AuthorizeReport widens one of the two and delegates the rest of that one.
func (a consoleAuthorizer) AuthorizeReport(
	ctx context.Context,
	caller issuereportsgrpc.Principal,
	report *issuereports.Report,
) error {
	if caller != nil && caller.UserID() == a.triager {
		return nil
	}

	return a.ReporterAuthorizer.AuthorizeReport(ctx, caller, report)
}

// TestTheNarrowHalfIsEmbeddableAndEmbeddingStaysAdditive pins the authorizer
// half of the two seams' opposite rulings.
//
// [issuereportsgrpc.Principal]'s method set is final because a consumer has
// nothing to inherit from. This one's need not be, and the reason is exactly
// what runs here: an implementation that embeds
// [issuereportsgrpc.ReporterAuthorizer] answers the questions it did not write,
// so a third question this surface grows costs its implementers nothing. That
// this package ships no default does not change it — what is embedded is the
// narrow half every deployment's rule contains, which is what it is exported to
// be. The two rules must not be collapsed into one, and this test is the second
// of them.
func TestTheNarrowHalfIsEmbeddableAndEmbeddingStaysAdditive(T *testing.T) {
	T.Parallel()

	rule := consoleAuthorizer{triager: testReporter}
	narrow := issuereportsgrpc.ReporterAuthorizer{}

	caller := &testPrincipal{userID: testReporter, scope: testScope}

	T.Run("the overridden question answers the deployment's rule", func(t *testing.T) {
		t.Parallel()

		somebodyElses := &issuereports.Report{Reporter: otherReporter}

		test.NoError(t, rule.AuthorizeReport(t.Context(), caller, somebodyElses))
		test.ErrorIs(t, narrow.AuthorizeReport(t.Context(), caller, somebodyElses),
			issuereportsgrpc.ErrTargetNotPermitted)
	})

	T.Run("the question it did not write is inherited", func(t *testing.T) {
		t.Parallel()

		// AuthorizeReporter is not declared on consoleAuthorizer, so it is the
		// narrow half's answer — which is what a method added to the interface
		// later would also be.
		test.NoError(t, rule.AuthorizeReporter(t.Context(), caller, testReporter))
		test.ErrorIs(t, rule.AuthorizeReporter(t.Context(), caller, otherReporter),
			issuereportsgrpc.ErrTargetNotPermitted)
	})
}
