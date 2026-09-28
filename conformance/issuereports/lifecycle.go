package issuereports

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The statuses, spelled once. Every move below names two of them.
const (
	statusOpen         = issuereportspb.ReportStatus_REPORT_STATUS_OPEN
	statusAcknowledged = issuereportspb.ReportStatus_REPORT_STATUS_ACKNOWLEDGED
	statusResolved     = issuereportspb.ReportStatus_REPORT_STATUS_RESOLVED
	statusDeclined     = issuereportspb.ReportStatus_REPORT_STATUS_DECLINED
)

func lifecycle(t *testing.T, s *conformance.Session) {
	t.Helper()

	revisions(t, s)
	transitions(t, s)
	archival(t, s)
}

func revisions(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a revision changes what a report says and leaves where it stands alone", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(createReport, transitionReport, updateReport))
		needsUser(t, mine)

		filed := fileOne(t, mine)
		move(t, mine, filed.GetId(), statusOpen, statusAcknowledged, "")

		revised, err := mine.Surfaces.IssueReports.UpdateReport(mine.Context(t.Context()),
			&issuereportspb.UpdateReportRequest{
				ReportId: filed.GetId(),
				Input: &issuereportspb.IssueReportUpdateInput{
					Kind:        "billing",
					Details:     "actually it was the invoice",
					SubjectType: "invoice",
					SubjectId:   "invoice_9",
				},
			})
		must.NoError(t, err)

		result := revised.GetResult()
		test.EqOp(t, "billing", result.GetKind())
		test.EqOp(t, "actually it was the invoice", result.GetDetails())
		test.EqOp(t, "invoice", result.GetSubjectType())
		test.EqOp(t, "invoice_9", result.GetSubjectId())

		// The two the input reserves: whose words these were, and where the
		// report stands. A revision that assigned either would be a whole-row
		// write that reopened a report somebody had decided.
		test.EqOp(t, mine.UserID, result.GetReporter())
		test.EqOp(t, statusAcknowledged, result.GetStatus())

		// The response is the stored row, so the stamp the database assigned is
		// on it rather than left unset.
		must.NotNil(t, result.GetLastUpdatedAt(),
			must.Sprint("a revision answered with no last-updated stamp, so it is not the stored row"))
	})

	t.Run("a revision that names no input is malformed and writes nothing", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(createReport, updateReport, getReport))
		filed := fileOne(t, mine)

		_, err := mine.Surfaces.IssueReports.UpdateReport(mine.Context(t.Context()),
			&issuereportspb.UpdateReportRequest{ReportId: filed.GetId()})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		test.EqOp(t, kindBug, read(t, mine, filed.GetId()).GetKind(),
			test.Sprint("a refused revision changed the report anyway"))
	})

	t.Run("a revision that omits the optional fields clears them rather than keeping them", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(createReport, updateReport, getReport))

		filed := fileOne(t, mine)
		must.StrNotEqFold(t, "", filed.GetSubjectType(),
			must.Sprint("a filed report carried no subject; clearing one cannot be observed"))

		// A revision replaces what the report says: every field is written
		// whether it was set or not, so one naming only the details blanks the
		// subject rather than keeping it.
		response, err := mine.Surfaces.IssueReports.UpdateReport(mine.Context(t.Context()),
			&issuereportspb.UpdateReportRequest{
				ReportId: filed.GetId(),
				Input:    &issuereportspb.IssueReportUpdateInput{Kind: kindBug, Details: "only the details"},
			})
		must.NoError(t, err)

		// The response, and then the stored row: a handler that answered with
		// the input while writing something else would pass the first alone.
		for source, report := range map[string]*issuereportspb.IssueReport{
			"the response":   response.GetResult(),
			"the stored row": read(t, mine, filed.GetId()),
		} {
			test.EqOp(t, kindBug, report.GetKind(), test.Sprintf("%s's kind", source))
			test.EqOp(t, "only the details", report.GetDetails(), test.Sprintf("%s's details", source))
			test.EqOp(t, "", report.GetSubjectType(), test.Sprintf("%s kept a subject type the revision omitted", source))
			test.EqOp(t, "", report.GetSubjectId(), test.Sprintf("%s kept a subject id the revision omitted", source))
		}
	})

	// The store's refusals again, on the way in through a revision: replace
	// semantics make an omitted required field an emptied one, and the report
	// is refused whole rather than written with the rest of the input.
	for name, input := range map[string]*issuereportspb.IssueReportUpdateInput{
		"a revision that would empty the kind is refused and writes nothing":    {Details: revised},
		"a revision that would empty the details is refused and writes nothing": {Kind: revised},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mine := s.Subject(t, conformance.Making(createReport, updateReport, getReport))
			filed := fileOne(t, mine)

			// The positive control: a whole revision reaches another of the
			// caller's reports, so the refusal below is the input's.
			other := fileOne(t, mine)
			_, err := mine.Surfaces.IssueReports.UpdateReport(mine.Context(t.Context()),
				&issuereportspb.UpdateReportRequest{
					ReportId: other.GetId(),
					Input:    &issuereportspb.IssueReportUpdateInput{Kind: revised, Details: revised},
				})
			must.NoError(t, err, must.Sprint("the caller cannot revise its own report; the refusal below proves nothing"))

			_, err = mine.Surfaces.IssueReports.UpdateReport(mine.Context(t.Context()),
				&issuereportspb.UpdateReportRequest{ReportId: filed.GetId(), Input: input})
			must.Error(t, err)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))

			// All four, not only the one under test: a revision applied before
			// it was validated would have blanked the subject on the way.
			stored := read(t, mine, filed.GetId())
			test.EqOp(t, kindBug, stored.GetKind())
			test.EqOp(t, detailsBug, stored.GetDetails())
			test.EqOp(t, filed.GetSubjectType(), stored.GetSubjectType())
			test.EqOp(t, filed.GetSubjectId(), stored.GetSubjectId())
		})
	}

	t.Run("a report in another tenant cannot be revised from here", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s, conformance.Making(createReport, updateReport, getReport))

		own := fileOne(t, mine)
		neighbor := fileOne(t, theirs)
		input := &issuereportspb.IssueReportUpdateInput{Kind: revised, Details: "revised from here"}

		// The positive control: the same revision reaches the caller's own.
		_, err := mine.Surfaces.IssueReports.UpdateReport(mine.Context(t.Context()),
			&issuereportspb.UpdateReportRequest{ReportId: own.GetId(), Input: input})
		must.NoError(t, err, must.Sprint("the caller cannot revise its own report; the refusal below proves nothing"))

		_, err = mine.Surfaces.IssueReports.UpdateReport(mine.Context(t.Context()),
			&issuereportspb.UpdateReportRequest{ReportId: neighbor.GetId(), Input: input})
		must.Error(t, err, must.Sprint("a neighboring tenant's report was revisable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.EqOp(t, kindBug, read(t, theirs, neighbor.GetId()).GetKind(),
			test.Sprint("a neighboring tenant's report was changed by a revision refused as absent"))
	})
}

func transitions(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a move changes the status, keeps the note and stamps the close", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(createReport, transitionReport))
		filed := fileOne(t, mine)

		moved := move(t, mine, filed.GetId(), statusOpen, statusResolved, "fixed in the next release")

		test.EqOp(t, statusResolved, moved.GetStatus())
		test.EqOp(t, "fixed in the next release", moved.GetResolution())

		// A terminal status stamps the close, and the response carries the stamp
		// this call assigned — which is what the caller records beside the move.
		test.NotNil(t, moved.GetClosedAt())
	})

	t.Run("the second of two people deciding from the same read is refused rather than overwriting the first", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(createReport, transitionReport, getReport))
		filed := fileOne(t, mine)

		move(t, mine, filed.GetId(), statusOpen, statusResolved, "the first note")

		// The same expectation again: somebody who read the report while it was
		// still open and decided from that read.
		_, err := mine.Surfaces.IssueReports.TransitionReport(mine.Context(t.Context()),
			&issuereportspb.TransitionReportRequest{
				ReportId:       filed.GetId(),
				ExpectedStatus: statusOpen,
				TargetStatus:   statusDeclined,
				Resolution:     "the second note",
			})
		must.Error(t, err, must.Sprint("a move from a status the report had left overwrote it"))

		// Aborted is gRPC's concurrency answer, and it is what tells a console
		// to re-read rather than to fix something first.
		test.EqOp(t, codes.Aborted, status.Code(err))

		stored := read(t, mine, filed.GetId())
		test.EqOp(t, "the first note", stored.GetResolution())
		test.EqOp(t, statusResolved, stored.GetStatus())
	})

	t.Run("a move the lifecycle does not admit is refused as malformed", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(createReport, transitionReport, getReport))
		filed := fileOne(t, mine)
		move(t, mine, filed.GetId(), statusOpen, statusAcknowledged, "")

		// Acknowledged does not go back to open: somebody has seen it. The pair
		// of statuses is wrong whatever the row holds, so it is malformed rather
		// than a precondition.
		_, err := mine.Surfaces.IssueReports.TransitionReport(mine.Context(t.Context()),
			&issuereportspb.TransitionReportRequest{
				ReportId:       filed.GetId(),
				ExpectedStatus: statusAcknowledged,
				TargetStatus:   statusOpen,
			})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		test.EqOp(t, statusAcknowledged, read(t, mine, filed.GetId()).GetStatus())
	})

	// An unspecified status is refused by name rather than matched against no
	// row, which would have read as somebody else having moved it.
	for name, statuses := range map[string][2]issuereportspb.ReportStatus{
		"a move that names no expected status is refused as malformed": {issuereportspb.ReportStatus_REPORT_STATUS_UNSPECIFIED, statusResolved},
		"a move that names no target status is refused as malformed":   {statusOpen, issuereportspb.ReportStatus_REPORT_STATUS_UNSPECIFIED},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mine := s.Subject(t, conformance.Making(createReport, transitionReport))
			filed := fileOne(t, mine)

			_, err := mine.Surfaces.IssueReports.TransitionReport(mine.Context(t.Context()),
				&issuereportspb.TransitionReportRequest{
					ReportId:       filed.GetId(),
					ExpectedStatus: statuses[0],
					TargetStatus:   statuses[1],
				})
			must.Error(t, err)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))
		})
	}

	t.Run("reopening clears the note and the close", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(createReport, transitionReport))
		filed := fileOne(t, mine)
		move(t, mine, filed.GetId(), statusOpen, statusResolved, "it was fixed")

		// A reopen goes to open rather than to acknowledged, because nobody has
		// dealt with it — and a reason that no longer holds is worse than none.
		reopened := move(t, mine, filed.GetId(), statusResolved, statusOpen, "this note is not kept")

		test.EqOp(t, statusOpen, reopened.GetStatus())
		test.EqOp(t, "", reopened.GetResolution())
		test.Nil(t, reopened.GetClosedAt())
	})

	t.Run("a report in another tenant cannot be moved from here", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s, conformance.Making(createReport, transitionReport, getReport))

		own := fileOne(t, mine)
		neighbor := fileOne(t, theirs)

		// The positive control: the same move reaches the caller's own.
		move(t, mine, own.GetId(), statusOpen, statusAcknowledged, "")

		_, err := mine.Surfaces.IssueReports.TransitionReport(mine.Context(t.Context()),
			&issuereportspb.TransitionReportRequest{
				ReportId:       neighbor.GetId(),
				ExpectedStatus: statusOpen,
				TargetStatus:   statusAcknowledged,
			})
		must.Error(t, err, must.Sprint("a neighboring tenant's report was movable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.EqOp(t, statusOpen, read(t, theirs, neighbor.GetId()).GetStatus(),
			test.Sprint("a neighboring tenant's report moved on a request refused as absent"))
	})
}

func archival(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an archived report leaves the queue, and archiving it again is an absence", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(createReport, getReport, archiveReport))
		filed := fileOne(t, mine)

		// The positive control: it is readable before it is archived, so the
		// absence afterwards is the archive's.
		read(t, mine, filed.GetId())

		_, err := mine.Surfaces.IssueReports.ArchiveReport(mine.Context(t.Context()),
			&issuereportspb.ArchiveReportRequest{ReportId: filed.GetId()})
		must.NoError(t, err)

		_, err = mine.Surfaces.IssueReports.GetReport(mine.Context(t.Context()),
			&issuereportspb.GetReportRequest{ReportId: filed.GetId()})
		must.Error(t, err, must.Sprint("an archived report was still readable from the queue"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = mine.Surfaces.IssueReports.ArchiveReport(mine.Context(t.Context()),
			&issuereportspb.ArchiveReportRequest{ReportId: filed.GetId()})
		must.Error(t, err, must.Sprint("an archived report was archived a second time"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("a report in another tenant cannot be archived from here", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s, conformance.Making(createReport, archiveReport, getReport))

		own := fileOne(t, mine)
		neighbor := fileOne(t, theirs)

		_, err := mine.Surfaces.IssueReports.ArchiveReport(mine.Context(t.Context()),
			&issuereportspb.ArchiveReportRequest{ReportId: own.GetId()})
		must.NoError(t, err, must.Sprint("the caller cannot archive its own report; the refusal below proves nothing"))

		_, err = mine.Surfaces.IssueReports.ArchiveReport(mine.Context(t.Context()),
			&issuereportspb.ArchiveReportRequest{ReportId: neighbor.GetId()})
		must.Error(t, err, must.Sprint("a neighboring tenant's report was archivable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		// And it is still in the queue of the tenant that owns it.
		test.Nil(t, read(t, theirs, neighbor.GetId()).GetArchivedAt())
	})
}
