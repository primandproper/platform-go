package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// idsOf is what the assertions below compare: which rows came back, in the order
// they came back, without the timestamps a database assigned.
func idsOf(reports []*issuereportspb.IssueReport) []string {
	out := make([]string, 0, len(reports))
	for _, r := range reports {
		out = append(out, r.GetId())
	}

	return out
}

// TestListReports pages the whole queue, and shows what keeps it inside one
// tenant.
func TestListReports(T *testing.T) {
	T.Parallel()

	T.Run("pages the caller's tenant and no other", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		mine := h.seedReport(t, testScope, testReporter)
		theirs := h.seedReport(t, otherScope, testReporter)

		res, err := h.server.ListReports(h.ctx(t, triager), &issuereportspb.ListReportsRequest{})
		must.NoError(t, err)

		ids := idsOf(res.GetResults())
		test.SliceContains(t, ids, mine.ID)
		test.SliceNotContains(t, ids, theirs.ID, test.Sprint(
			"another tenant's report is on this page, so the scope is not bound into the statement"))

		// The pagination is the store's, rendered by filtering/grpc: a console
		// renders "n of m" off it rather than counting the page.
		must.NotNil(t, res.GetPagination())
	})

	T.Run("a filter nothing can read is malformed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.ListReports(h.ctx(t, triager),
			&issuereportspb.ListReportsRequest{Filter: badFilter()})
		must.Error(t, err)
		test.Nil(t, res)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

// TestListReportsByStatus is the triage queue.
func TestListReportsByStatus(T *testing.T) {
	T.Parallel()

	T.Run("pages one queue", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		open := h.seedReport(t, testScope, testReporter)
		resolved := h.seedReport(t, testScope, otherReporter)
		h.move(t, testScope, resolved.ID, issuereports.StatusOpen, issuereports.StatusResolved, "done")

		res, err := h.server.ListReportsByStatus(h.ctx(t, triager),
			&issuereportspb.ListReportsByStatusRequest{Status: issuereportspb.ReportStatus_REPORT_STATUS_OPEN})
		must.NoError(t, err)

		ids := idsOf(res.GetResults())
		test.SliceContains(t, ids, open.ID)
		test.SliceNotContains(t, ids, resolved.ID)
	})

	T.Run("a status the queue does not have is refused rather than answered with an empty page", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seedReport(t, testScope, testReporter)

		res, err := h.server.ListReportsByStatus(h.ctx(t, triager),
			&issuereportspb.ListReportsByStatusRequest{})
		must.Error(t, err)
		test.Nil(t, res)

		// An empty page is what a queue that has been quietly misspelled looks
		// like, and STATUS_UNSPECIFIED is one of those rather than a request for
		// all four.
		test.ErrorIs(t, err, issuereports.ErrUnknownStatus)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

// TestListReportsByReporter is the one read whose request names a person, and
// therefore the one that asks the consumer's rule.
func TestListReportsByReporter(T *testing.T) {
	T.Parallel()

	T.Run("a person pages what they filed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		mine := h.seedReport(t, testScope, testReporter)
		theirs := h.seedReport(t, testScope, otherReporter)

		res, err := h.server.ListReportsByReporter(h.ctx(t, testReporter),
			&issuereportspb.ListReportsByReporterRequest{Reporter: testReporter})
		must.NoError(t, err)

		ids := idsOf(res.GetResults())
		test.SliceContains(t, ids, mine.ID)
		test.SliceNotContains(t, ids, theirs.ID)
	})

	T.Run("a triager pages anybody's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)

		res, err := h.server.ListReportsByReporter(h.ctx(t, triager),
			&issuereportspb.ListReportsByReporterRequest{Reporter: testReporter})
		must.NoError(t, err)
		test.SliceContains(t, idsOf(res.GetResults()), report.ID)
	})

	T.Run("naming somebody else is refused, and the refusal says so", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seedReport(t, testScope, testReporter)

		res, err := h.server.ListReportsByReporter(h.ctx(t, otherReporter),
			&issuereportspb.ListReportsByReporterRequest{Reporter: testReporter})
		must.Error(t, err)
		test.Nil(t, res)

		// PermissionDenied rather than the absence GetReport answers with: this
		// question is asked before anything is read, so the refusal discloses
		// nothing about whether that person has ever filed anything.
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		test.ErrorIs(t, err, issuereportsgrpc.ErrTargetNotPermitted)
	})

	T.Run("naming somebody who has filed nothing is refused the same way", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ListReportsByReporter(h.ctx(t, otherReporter),
			&issuereportspb.ListReportsByReporterRequest{Reporter: "nobody_at_all"})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	T.Run("an authorizer that cannot decide is not a refusal", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithAuthorizer(t, &brokenAuthorizer{err: errAuthorizerUnavailable})

		_, err := h.server.ListReportsByReporter(h.ctx(t, testReporter),
			&issuereportspb.ListReportsByReporterRequest{Reporter: testReporter})
		must.Error(t, err)
		test.EqOp(t, codes.Internal, status.Code(err))
		test.ErrorIs(t, err, errAuthorizerUnavailable)
	})

	T.Run("a malformed filter is answered before the caller is gated", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		// Refused by the filter rather than by the rule, though this caller would
		// have been refused by the rule too: saying a request is malformed
		// discloses nothing about any row.
		_, err := h.server.ListReportsByReporter(h.ctx(t, otherReporter),
			&issuereportspb.ListReportsByReporterRequest{Reporter: testReporter, Filter: badFilter()})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

// TestListReportsBySubjectType and its sibling below are the two reads a
// moderation view makes.
func TestListReportsBySubjectType(T *testing.T) {
	T.Parallel()

	T.Run("pages every report about one kind of thing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		recipe := h.seedReportAbout(t, testScope, testReporter, "recipe", "recipe_1")
		invoice := h.seedReportAbout(t, testScope, testReporter, "invoice", "invoice_1")

		res, err := h.server.ListReportsBySubjectType(h.ctx(t, triager),
			&issuereportspb.ListReportsBySubjectTypeRequest{SubjectType: "recipe"})
		must.NoError(t, err)

		ids := idsOf(res.GetResults())
		test.SliceContains(t, ids, recipe.ID)
		test.SliceNotContains(t, ids, invoice.ID)
	})

	T.Run("a subject type this deployment does not use is an empty page", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seedReport(t, testScope, testReporter)

		// The vocabulary is the application's and nothing here validates it, so
		// a type nobody has used is an answer rather than a refusal.
		res, err := h.server.ListReportsBySubjectType(h.ctx(t, triager),
			&issuereportspb.ListReportsBySubjectTypeRequest{SubjectType: "spaceship"})
		must.NoError(t, err)
		test.SliceEmpty(t, res.GetResults())
	})
}

func TestListReportsForSubject(T *testing.T) {
	T.Parallel()

	T.Run("pages every report about one particular thing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		first := h.seedReportAbout(t, testScope, testReporter, "recipe", "recipe_1")
		second := h.seedReportAbout(t, testScope, otherReporter, "recipe", "recipe_2")

		res, err := h.server.ListReportsForSubject(h.ctx(t, triager),
			&issuereportspb.ListReportsForSubjectRequest{SubjectType: "recipe", SubjectId: "recipe_1"})
		must.NoError(t, err)

		ids := idsOf(res.GetResults())
		test.SliceContains(t, ids, first.ID)
		test.SliceNotContains(t, ids, second.ID)
	})

	T.Run("a filter nothing can read is malformed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ListReportsForSubject(h.ctx(t, triager),
			&issuereportspb.ListReportsForSubjectRequest{
				SubjectType: "recipe",
				SubjectId:   "recipe_1",
				Filter:      badFilter(),
			})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

// TestEveryListIsAnonymousToNobody pins the one thing all five share: without a
// principal there is no tenant, so there is no page.
func TestEveryListIsAnonymousToNobody(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	for name, call := range map[string]func() error{
		"ListReports": func() error {
			_, err := h.server.ListReports(T.Context(), &issuereportspb.ListReportsRequest{})

			return err
		},
		"ListReportsByStatus": func() error {
			_, err := h.server.ListReportsByStatus(T.Context(),
				&issuereportspb.ListReportsByStatusRequest{Status: issuereportspb.ReportStatus_REPORT_STATUS_OPEN})

			return err
		},
		"ListReportsByReporter": func() error {
			_, err := h.server.ListReportsByReporter(T.Context(),
				&issuereportspb.ListReportsByReporterRequest{Reporter: testReporter})

			return err
		},
		"ListReportsBySubjectType": func() error {
			_, err := h.server.ListReportsBySubjectType(T.Context(),
				&issuereportspb.ListReportsBySubjectTypeRequest{SubjectType: "recipe"})

			return err
		},
		"ListReportsForSubject": func() error {
			_, err := h.server.ListReportsForSubject(T.Context(),
				&issuereportspb.ListReportsForSubjectRequest{SubjectType: "recipe", SubjectId: "recipe_1"})

			return err
		},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			err := call()
			must.Error(t, err)
			test.ErrorIs(t, err, issuereportsgrpc.ErrNoPrincipal)
			test.EqOp(t, codes.Unauthenticated, status.Code(err))
		})
	}
}
