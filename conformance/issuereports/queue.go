package issuereports

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// listing is one of the five paged reads, reduced to "which reports did it
// answer with", for the properties every one of them shares.
type listing func(ctx context.Context, sub *conformance.Subject, report *issuereportspb.IssueReport, filter *filteringpb.QueryFilter) ([]string, error)

// listings are all five, each asked the question that should find report —
// the caller's own, open, about the subject it was filed about.
func listings() map[string]listing {
	return map[string]listing{
		"ListReports": func(ctx context.Context, sub *conformance.Subject, _ *issuereportspb.IssueReport, filter *filteringpb.QueryFilter) ([]string, error) {
			page, err := sub.Surfaces.IssueReports.ListReports(ctx, &issuereportspb.ListReportsRequest{Filter: filter})

			return reportIDs(page.GetResults()), err
		},
		"ListReportsByStatus": func(ctx context.Context, sub *conformance.Subject, _ *issuereportspb.IssueReport, filter *filteringpb.QueryFilter) ([]string, error) {
			page, err := sub.Surfaces.IssueReports.ListReportsByStatus(ctx,
				&issuereportspb.ListReportsByStatusRequest{Status: statusOpen, Filter: filter})

			return reportIDs(page.GetResults()), err
		},
		"ListReportsByReporter": func(ctx context.Context, sub *conformance.Subject, _ *issuereportspb.IssueReport, filter *filteringpb.QueryFilter) ([]string, error) {
			page, err := sub.Surfaces.IssueReports.ListReportsByReporter(ctx,
				&issuereportspb.ListReportsByReporterRequest{Reporter: sub.UserID, Filter: filter})

			return reportIDs(page.GetResults()), err
		},
		"ListReportsBySubjectType": func(ctx context.Context, sub *conformance.Subject, report *issuereportspb.IssueReport, filter *filteringpb.QueryFilter) ([]string, error) {
			page, err := sub.Surfaces.IssueReports.ListReportsBySubjectType(ctx,
				&issuereportspb.ListReportsBySubjectTypeRequest{SubjectType: report.GetSubjectType(), Filter: filter})

			return reportIDs(page.GetResults()), err
		},
		"ListReportsForSubject": func(ctx context.Context, sub *conformance.Subject, report *issuereportspb.IssueReport, filter *filteringpb.QueryFilter) ([]string, error) {
			page, err := sub.Surfaces.IssueReports.ListReportsForSubject(ctx,
				&issuereportspb.ListReportsForSubjectRequest{
					SubjectType: report.GetSubjectType(),
					SubjectId:   report.GetSubjectId(),
					Filter:      filter,
				})

			return reportIDs(page.GetResults()), err
		},
	}
}

// includeArchived is the filter a client sets to ask for the reports taken out
// of the queue.
func includeArchived() *filteringpb.QueryFilter {
	include := true

	return &filteringpb.QueryFilter{IncludeArchived: &include}
}

// badFilter is a page request no converter can read. The sort direction is the
// field with a closed set of spellings, so an unrecognized one is refused by the
// filter's own conversion rather than by anything this surface decides.
func badFilter() *filteringpb.QueryFilter {
	sideways := "sideways"

	return &filteringpb.QueryFilter{SortBy: &sideways}
}

func queue(t *testing.T, s *conformance.Session) {
	t.Helper()

	everyListing(t, s)
	byStatus(t, s)
	byReporter(t, s)
	bySubject(t, s)
}

func everyListing(t *testing.T, s *conformance.Session) {
	t.Helper()

	for name, list := range listings() {
		t.Run(name+" pages the caller's tenant and no other", func(t *testing.T) {
			t.Parallel()

			mine, theirs := twoTenants(t, s)
			needsUser(t, theirs)

			// Both filed about one subject, so the listings keyed on a subject
			// could reach the neighbor's through the subject alone and only the
			// scope keeps it out.
			subjectType, subjectID := subjectOfItsOwn(), identifiers.New()
			own := file(t, mine, subjectType, subjectID)
			neighbor := file(t, theirs, subjectType, subjectID)

			ids, err := list(mine.Context(t.Context()), mine, own, nil)
			must.NoError(t, err)

			// Presence and absence of two known reports, never a count: this
			// listing may run against a database the suite does not own.
			test.SliceContains(t, ids, own.GetId(),
				test.Sprint("the caller's own report was missing from its queue"))
			test.SliceNotContains(t, ids, neighbor.GetId(),
				test.Sprint("a neighboring tenant's report reached this listing"))
		})

		t.Run(name+" leaves an archived report out when the archive was not asked for", func(t *testing.T) {
			t.Parallel()

			mine := s.Subject(t)
			needsUser(t, mine)

			subjectType, subjectID := subjectOfItsOwn(), identifiers.New()
			live := file(t, mine, subjectType, subjectID)
			removed := file(t, mine, subjectType, subjectID)

			_, err := mine.Surfaces.IssueReports.ArchiveReport(mine.Context(t.Context()),
				&issuereportspb.ArchiveReportRequest{ReportId: removed.GetId()})
			must.NoError(t, err)

			ids, err := list(mine.Context(t.Context()), mine, live, nil)
			must.NoError(t, err)

			test.SliceContains(t, ids, live.GetId())
			test.SliceNotContains(t, ids, removed.GetId(),
				test.Sprint("an archived report was listed to a read that did not ask for the archive"))
		})

		// Whether the archive comes back is the deployment's grants' answer,
		// and a suite cannot know which one to expect. What holds under every
		// answer is that asking is not refused: a caller without the grant is
		// narrowed to the live queue rather than told off, which is the half a
		// client notices.
		t.Run(name+" answers a request for the archive rather than refusing it", func(t *testing.T) {
			t.Parallel()

			mine := s.Subject(t)
			needsUser(t, mine)

			live := fileOne(t, mine)

			ids, err := list(mine.Context(t.Context()), mine, live, includeArchived())
			must.NoError(t, err, must.Sprint("asking for the archive was refused"))
			test.SliceContains(t, ids, live.GetId())
		})

		// The granted half. An administrator holds whatever grant a deployment
		// reads the archive off, so asking is answered with it — and the read
		// without the filter, by the same caller, is the control that the
		// report really did leave the queue.
		t.Run(name+" gives an administrator the archive it asked for", func(t *testing.T) {
			t.Parallel()

			admin := s.Subject(t, conformance.AsAdmin())
			needsUser(t, admin)

			subjectType, subjectID := subjectOfItsOwn(), identifiers.New()
			live := file(t, admin, subjectType, subjectID)
			removed := file(t, admin, subjectType, subjectID)

			ctx := admin.Context(t.Context())

			_, err := admin.Surfaces.IssueReports.ArchiveReport(ctx,
				&issuereportspb.ArchiveReportRequest{ReportId: removed.GetId()})
			must.NoError(t, err)

			without, err := list(ctx, admin, live, nil)
			must.NoError(t, err)
			test.SliceNotContains(t, without, removed.GetId())

			with, err := list(ctx, admin, live, includeArchived())
			must.NoError(t, err)
			test.SliceContains(t, with, live.GetId())
			test.SliceContains(t, with, removed.GetId(),
				test.Sprint("an administrator asked for the archive and was answered without it"))
		})
	}
}

func byStatus(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a queue by status holds the reports in that status and no others", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		open := fileOne(t, mine)
		resolved := fileOne(t, mine)
		move(t, mine, resolved.GetId(), statusOpen, statusResolved, "done")

		page, err := mine.Surfaces.IssueReports.ListReportsByStatus(mine.Context(t.Context()),
			&issuereportspb.ListReportsByStatusRequest{Status: statusOpen})
		must.NoError(t, err)

		ids := reportIDs(page.GetResults())
		test.SliceContains(t, ids, open.GetId())
		test.SliceNotContains(t, ids, resolved.GetId())
	})

	// An empty page is what a queue that has been quietly misspelled looks
	// like, and an unspecified status is one of those rather than a request
	// for all four.
	t.Run("a queue by status that names no status is refused as malformed", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		fileOne(t, mine)

		_, err := mine.Surfaces.IssueReports.ListReportsByStatus(mine.Context(t.Context()),
			&issuereportspb.ListReportsByStatusRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func byReporter(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a person pages what they filed and not what a colleague filed", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		own := fileOne(t, mine)
		theirs := fileOne(t, other)

		page, err := mine.Surfaces.IssueReports.ListReportsByReporter(mine.Context(t.Context()),
			&issuereportspb.ListReportsByReporterRequest{Reporter: mine.UserID})
		must.NoError(t, err)

		ids := reportIDs(page.GetResults())
		test.SliceContains(t, ids, own.GetId())
		test.SliceNotContains(t, ids, theirs.GetId())
	})

	// What a "your reports" view sends: it has nothing to put in the field that
	// the connection is not already carrying.
	t.Run("a request that names no reporter pages the caller's own", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		own := fileOne(t, mine)
		theirs := fileOne(t, other)

		page, err := mine.Surfaces.IssueReports.ListReportsByReporter(mine.Context(t.Context()),
			&issuereportspb.ListReportsByReporterRequest{})
		must.NoError(t, err)

		ids := reportIDs(page.GetResults())
		test.SliceContains(t, ids, own.GetId())
		test.SliceNotContains(t, ids, theirs.GetId())
	})

	// PermissionDenied rather than the absence GetReport answers with: this
	// question is asked before anything is read, so the refusal discloses
	// nothing about whether that person has ever filed anything — which is also
	// why somebody who has filed nothing is refused identically.
	t.Run("naming somebody else is refused before anything is read", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)
		fileOne(t, other)

		// The positive control: naming oneself is answered.
		_, err := mine.Surfaces.IssueReports.ListReportsByReporter(mine.Context(t.Context()),
			&issuereportspb.ListReportsByReporterRequest{Reporter: mine.UserID})
		must.NoError(t, err, must.Sprint("the caller cannot page its own reports; the refusals below prove nothing"))

		_, err = mine.Surfaces.IssueReports.ListReportsByReporter(mine.Context(t.Context()),
			&issuereportspb.ListReportsByReporterRequest{Reporter: other.UserID})
		must.Error(t, err, must.Sprint("a colleague's reports were pageable by name"))
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		_, err = mine.Surfaces.IssueReports.ListReportsByReporter(mine.Context(t.Context()),
			&issuereportspb.ListReportsByReporterRequest{Reporter: identifiers.New()})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err),
			test.Sprint("somebody who has filed nothing was answered differently from somebody who has"))
	})

	// Refused by the filter rather than by the rule, though this caller would
	// have been refused by the rule too: saying a request is malformed discloses
	// nothing about any report, which is only possible if the conversion ran
	// first.
	t.Run("a malformed page is answered as malformed before the reporter is gated", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		_, err := mine.Surfaces.IssueReports.ListReportsByReporter(mine.Context(t.Context()),
			&issuereportspb.ListReportsByReporterRequest{Reporter: other.UserID, Filter: badFilter()})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func bySubject(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a listing by subject type holds reports about that kind of thing and no other", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		wanted, unwanted := subjectOfItsOwn(), subjectOfItsOwn()
		about := file(t, mine, wanted, identifiers.New())
		elsewhere := file(t, mine, unwanted, identifiers.New())

		page, err := mine.Surfaces.IssueReports.ListReportsBySubjectType(mine.Context(t.Context()),
			&issuereportspb.ListReportsBySubjectTypeRequest{SubjectType: wanted})
		must.NoError(t, err)

		ids := reportIDs(page.GetResults())
		test.SliceContains(t, ids, about.GetId())
		test.SliceNotContains(t, ids, elsewhere.GetId())
	})

	// The vocabulary is the application's and nothing here validates it, so a
	// type nobody has used is an answer rather than a refusal.
	t.Run("a listing by a subject type nobody has used is answered rather than refused", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		filed := fileOne(t, mine)

		page, err := mine.Surfaces.IssueReports.ListReportsBySubjectType(mine.Context(t.Context()),
			&issuereportspb.ListReportsBySubjectTypeRequest{SubjectType: subjectOfItsOwn()})
		must.NoError(t, err)
		test.SliceNotContains(t, reportIDs(page.GetResults()), filed.GetId())
	})

	t.Run("a listing for one subject holds reports about it and not about its siblings", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		subjectType := subjectOfItsOwn()
		first := file(t, mine, subjectType, "first")
		second := file(t, mine, subjectType, "second")

		page, err := mine.Surfaces.IssueReports.ListReportsForSubject(mine.Context(t.Context()),
			&issuereportspb.ListReportsForSubjectRequest{SubjectType: subjectType, SubjectId: "first"})
		must.NoError(t, err)

		ids := reportIDs(page.GetResults())
		test.SliceContains(t, ids, first.GetId())
		test.SliceNotContains(t, ids, second.GetId())
	})
}
