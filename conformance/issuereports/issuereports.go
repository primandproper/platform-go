package issuereports

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test/must"
)

// The words every report here is filed with. What a report says is immaterial
// to every promise below except the revision's, which changes them.
const (
	kindBug     = "bug"
	detailsBug  = "the thing did not work"
	subjectKind = "recipe"
)

// Suite is the report queue's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    "issuereports",
		Mounted: func(s conformance.Surfaces) bool { return s.IssueReports != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("filing", func(t *testing.T) {
		t.Parallel()
		filing(t, s)
	})
	t.Run("reading", func(t *testing.T) {
		t.Parallel()
		reading(t, s)
	})
	t.Run("the lifecycle", func(t *testing.T) {
		t.Parallel()
		lifecycle(t, s)
	})
	t.Run("the queue", func(t *testing.T) {
		t.Parallel()
		queue(t, s)
	})
}

// twoTenants mints two callers and refuses to proceed if the subject put them
// in one tenant.
//
// A subject whose NewSubject ignored its request and handed back one caller
// twice would make every confinement assertion here compare a queue with
// itself, and all of them would pass.
func twoTenants(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.Subject(t), s.Subject(t)
	needsUser(t, mine)

	must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(),
		must.Sprint("the subject minted two callers in one tenant; the confinement this asserts cannot be observed"))

	return mine, theirs
}

// colleague mints a second person in of's tenant. A subject that cannot put two
// callers in one tenant declines, and the assertion that asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject) *conformance.Subject {
	t.Helper()

	needsUser(t, of)

	other := s.Subject(t, conformance.InTenant(of.Scope))
	needsUser(t, other)

	must.StrNotEqFold(t, of.UserID, other.UserID,
		must.Sprint("the subject minted a colleague as the same user; authorship cannot be observed"))

	return other
}

// needsUser skips unless the subject surfaced the caller's user identifier,
// which is what a report's reporter is.
func needsUser(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.UserID == "" {
		t.Skip("conformance: this subject does not surface the caller's user identifier")
	}
}

// subjectOfItsOwn is a subject type nobody else in the deployment has filed
// about, so a listing by it holds only what the assertion filed.
func subjectOfItsOwn() string { return "conf_" + identifiers.New() }

// file files a report as sub, about the named subject, through the surface.
func file(t *testing.T, sub *conformance.Subject, subjectType, subjectID string) *issuereportspb.IssueReport {
	t.Helper()

	created, err := sub.Surfaces.IssueReports.CreateReport(sub.Context(t.Context()),
		&issuereportspb.CreateReportRequest{Input: &issuereportspb.IssueReportCreationInput{
			Kind:        kindBug,
			Details:     detailsBug,
			SubjectType: subjectType,
			SubjectId:   subjectID,
		}})
	must.NoError(t, err, must.Sprint("filing a report"))
	must.NotNil(t, created.GetResult())
	must.StrNotEqFold(t, "", created.GetResult().GetId(), must.Sprint("a filed report came back with no identifier"))

	return created.GetResult()
}

// fileOne is file about a thing nobody else has reported.
func fileOne(t *testing.T, sub *conformance.Subject) *issuereportspb.IssueReport {
	t.Helper()

	return file(t, sub, subjectKind, identifiers.New())
}

// move transitions a report through the surface, failing if the move is
// refused.
func move(
	t *testing.T,
	sub *conformance.Subject,
	reportID string,
	from, to issuereportspb.ReportStatus,
	resolution string,
) *issuereportspb.IssueReport {
	t.Helper()

	moved, err := sub.Surfaces.IssueReports.TransitionReport(sub.Context(t.Context()),
		&issuereportspb.TransitionReportRequest{
			ReportId:       reportID,
			ExpectedStatus: from,
			TargetStatus:   to,
			Resolution:     resolution,
		})
	must.NoError(t, err, must.Sprintf("moving a report from %s to %s", from, to))
	must.NotNil(t, moved.GetResult())

	return moved.GetResult()
}

// read reads a report as sub, failing if it cannot.
func read(t *testing.T, sub *conformance.Subject, reportID string) *issuereportspb.IssueReport {
	t.Helper()

	found, err := sub.Surfaces.IssueReports.GetReport(sub.Context(t.Context()),
		&issuereportspb.GetReportRequest{ReportId: reportID})
	must.NoError(t, err, must.Sprint("reading a report"))
	must.NotNil(t, found.GetResult())

	return found.GetResult()
}

func reportIDs(reports []*issuereportspb.IssueReport) []string {
	ids := make([]string, 0, len(reports))
	for _, r := range reports {
		ids = append(ids, r.GetId())
	}

	return ids
}
