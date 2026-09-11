package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestReportToProto covers the renderer a consumer composing this queue into a
// larger response reaches for.
func TestReportToProto(T *testing.T) {
	T.Parallel()

	T.Run("carries every field the message has", func(t *testing.T) {
		t.Parallel()

		var (
			created  = time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
			updated  = created.Add(time.Hour)
			closed   = created.Add(2 * time.Hour)
			archived = created.Add(3 * time.Hour)
		)

		report := &issuereports.Report{
			CreatedAt:     created,
			LastUpdatedAt: &updated,
			ClosedAt:      &closed,
			ArchivedAt:    &archived,
			ID:            "report_1",
			Reporter:      testReporter,
			Kind:          "bug",
			Details:       "the thing did not work",
			SubjectType:   "recipe",
			SubjectID:     "recipe_1",
			Status:        issuereports.StatusResolved,
			Resolution:    "fixed",
			Scope:         testScope,
		}

		rendered := issuereportsgrpc.ReportToProto(report)
		must.NotNil(t, rendered)

		test.EqOp(t, created, rendered.GetCreatedAt().AsTime())
		test.EqOp(t, updated, rendered.GetLastUpdatedAt().AsTime())
		test.EqOp(t, closed, rendered.GetClosedAt().AsTime())
		test.EqOp(t, archived, rendered.GetArchivedAt().AsTime())
		test.EqOp(t, "report_1", rendered.GetId())
		test.EqOp(t, testReporter, rendered.GetReporter())
		test.EqOp(t, "bug", rendered.GetKind())
		test.EqOp(t, "the thing did not work", rendered.GetDetails())
		test.EqOp(t, "recipe", rendered.GetSubjectType())
		test.EqOp(t, "recipe_1", rendered.GetSubjectId())
		test.EqOp(t, issuereportspb.ReportStatus_REPORT_STATUS_RESOLVED, rendered.GetStatus())
		test.EqOp(t, "fixed", rendered.GetResolution())
	})

	T.Run("the scope on the value does not travel", func(t *testing.T) {
		t.Parallel()

		// There is nowhere for it to go — the message reserves the name — and
		// this is the assertion that the converter does not find somewhere else
		// to put it.
		rendered := issuereportsgrpc.ReportToProto(&issuereports.Report{
			ID:    "report_1",
			Scope: tenancy.Of("tenant_9"),
		})
		must.NotNil(t, rendered)

		test.StrNotContains(t, rendered.String(), "tenant_9")
	})

	T.Run("the three nullable stamps stay unset", func(t *testing.T) {
		t.Parallel()

		// A client rendering "closed" wants to know there was no closing, and
		// 1970 is not that answer.
		rendered := issuereportsgrpc.ReportToProto(&issuereports.Report{ID: "report_1"})
		must.NotNil(t, rendered)

		test.Nil(t, rendered.GetLastUpdatedAt())
		test.Nil(t, rendered.GetClosedAt())
		test.Nil(t, rendered.GetArchivedAt())
	})

	T.Run("a nil report is nil rather than an empty message", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, issuereportsgrpc.ReportToProto(nil))
	})

	T.Run("a page renders in order", func(t *testing.T) {
		t.Parallel()

		rendered := issuereportsgrpc.ReportsToProto([]*issuereports.Report{
			{ID: "first"}, {ID: "second"},
		})
		must.SliceLen(t, 2, rendered)
		test.EqOp(t, "first", rendered[0].GetId())
		test.EqOp(t, "second", rendered[1].GetId())

		// An empty page is an empty slice rather than nil, so a response carries
		// "no results" rather than an absent field.
		test.SliceEmpty(t, issuereportsgrpc.ReportsToProto(nil))
	})
}

// TestStatusFromProto is the half of the enum conversion that decides what an
// unspecified status means, which is the decision worth pinning.
func TestStatusFromProto(T *testing.T) {
	T.Parallel()

	T.Run("unspecified is the empty status rather than a default", func(t *testing.T) {
		t.Parallel()

		// The store refuses the empty status by name — ErrUnknownStatus — which
		// is what keeps a request that named none from quietly becoming the open
		// queue, or a move the caller did not ask for.
		test.EqOp(t, issuereports.Status(""),
			issuereportsgrpc.StatusFromProto(issuereportspb.ReportStatus_REPORT_STATUS_UNSPECIFIED))
	})

	T.Run("a member no lifecycle has is the empty status too", func(t *testing.T) {
		t.Parallel()

		// A number off the wire that the enum does not declare. It is refused
		// rather than rendered as anything, for the same reason.
		test.EqOp(t, issuereports.Status(""), issuereportsgrpc.StatusFromProto(issuereportspb.ReportStatus(99)))
	})

	T.Run("a status this module does not serve renders as unspecified", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, issuereportspb.ReportStatus_REPORT_STATUS_UNSPECIFIED,
			issuereportsgrpc.StatusToProto(issuereports.Status("triaged-ish")))
	})
}
