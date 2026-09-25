package issuereports

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func filing(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a report is filed in the caller's name and answered as the row that was written", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		needsUser(t, mine)

		filed := fileOne(t, mine)

		// The reporter is the principal's, and the request had nowhere to put
		// one: the creation input reserves the name.
		test.EqOp(t, mine.UserID, filed.GetReporter())

		// A report is born open, and its creation time is the database's — so
		// the response is the row that was written rather than the request that
		// asked for it.
		test.EqOp(t, issuereportspb.ReportStatus_REPORT_STATUS_OPEN, filed.GetStatus())
		must.NotNil(t, filed.GetCreatedAt(),
			must.Sprint("the response carries no creation time, so the read-back did not happen"))
		test.False(t, filed.GetCreatedAt().AsTime().IsZero())

		// The two nullable stamps stay unset rather than becoming 1970.
		test.Nil(t, filed.GetClosedAt())
		test.Nil(t, filed.GetLastUpdatedAt())
	})

	t.Run("a filing that names no input is malformed rather than a report with nothing in it", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		_, err := mine.Surfaces.IssueReports.CreateReport(mine.Context(t.Context()),
			&issuereportspb.CreateReportRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	// The store's refusals, as a client reads them: the handler hands each one
	// over as Internal, and it is the registered mapper that makes it the
	// client's mistake — which is why this is worth asserting through a
	// composition root.
	for name, input := range map[string]*issuereportspb.IssueReportCreationInput{
		"a filing with no kind is refused as malformed":    {Details: detailsBug},
		"a filing with no details is refused as malformed": {Kind: kindBug},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mine := s.Subject(t)

			_, err := mine.Surfaces.IssueReports.CreateReport(mine.Context(t.Context()),
				&issuereportspb.CreateReportRequest{Input: input})
			must.Error(t, err)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func reading(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a report is readable by the person who filed it and absent to a colleague", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		filed := fileOne(t, mine)

		// The positive control, which is what keeps the absence below from being
		// a surface that answers nobody.
		test.EqOp(t, filed.GetId(), read(t, mine, filed.GetId()).GetId())

		// NotFound rather than PermissionDenied, and that is the point: the read
		// has already happened by the time the rule is asked, so a refusal that
		// said so would tell a caller walking identifiers which of them are real.
		_, err := other.Surfaces.IssueReports.GetReport(other.Context(t.Context()),
			&issuereportspb.GetReportRequest{ReportId: filed.GetId()})
		must.Error(t, err, must.Sprint("a colleague read a report they did not file"))
		test.EqOp(t, codes.NotFound, status.Code(err),
			test.Sprint("a report the rule refused was answered as something other than absent"))

		// And the colleague reaches their own, which rules out a rule that
		// happens to favor whichever caller was made first.
		theirs := fileOne(t, other)
		test.EqOp(t, theirs.GetId(), read(t, other, theirs.GetId()).GetId())
	})

	t.Run("a report in another tenant is absent", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)

		own := fileOne(t, mine)
		neighbor := fileOne(t, theirs)

		test.EqOp(t, own.GetId(), read(t, mine, own.GetId()).GetId())

		_, err := mine.Surfaces.IssueReports.GetReport(mine.Context(t.Context()),
			&issuereportspb.GetReportRequest{ReportId: neighbor.GetId()})
		must.Error(t, err, must.Sprint("a neighboring tenant's report was readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("an identifier nobody minted is an absence", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		_, err := mine.Surfaces.IssueReports.GetReport(mine.Context(t.Context()),
			&issuereportspb.GetReportRequest{ReportId: identifiers.New()})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}
