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

// TestCreateReport covers the filing, and what a request may and may not say
// about who filed it.
func TestCreateReport(T *testing.T) {
	T.Parallel()

	T.Run("files the report in the caller's name", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateReport(h.ctx(t, testReporter),
			&issuereportspb.CreateReportRequest{Input: creationInput()})
		must.NoError(t, err)
		must.NotNil(t, res.GetResult())

		// The reporter is the principal's, and the request had nowhere to put
		// one: IssueReportCreationInput reserves the name.
		test.EqOp(t, testReporter, res.GetResult().GetReporter())

		// A report is born open, and the identifier and the creation time are
		// the store's — so the response is the row that was written rather than
		// the request that asked for it.
		test.EqOp(t, issuereportspb.Status_STATUS_OPEN, res.GetResult().GetStatus())
		test.NotEqOp(t, "", res.GetResult().GetId())
		test.True(t, res.GetResult().GetCreatedAt().AsTime().After(zeroTime()),
			test.Sprint("the response carries no creation time, so the read-back did not happen"))

		// The two nullable stamps stay unset rather than becoming 1970.
		test.Nil(t, res.GetResult().GetClosedAt())
		test.Nil(t, res.GetResult().GetLastUpdatedAt())
	})

	T.Run("the report is readable by the person who filed it and by nobody else", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		created, err := h.server.CreateReport(h.ctx(t, testReporter),
			&issuereportspb.CreateReportRequest{Input: creationInput()})
		must.NoError(t, err)

		id := created.GetResult().GetId()

		read, err := h.server.GetReport(h.ctx(t, testReporter),
			&issuereportspb.GetReportRequest{ReportId: id})
		must.NoError(t, err)
		test.EqOp(t, id, read.GetResult().GetId())

		// The other person is refused, and the refusal reads as an absence — see
		// TestGetReport.
		_, err = h.server.GetReport(h.ctx(t, otherReporter),
			&issuereportspb.GetReportRequest{ReportId: id})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("a request that named no input is malformed rather than a report with nothing in it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateReport(h.ctx(t, testReporter), &issuereportspb.CreateReportRequest{})
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, issuereportsgrpc.ErrNilReportInput)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("the store's own refusals reach the client as the mapper's codes", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		for _, tc := range []struct {
			input *issuereportspb.IssueReportCreationInput
			want  error
			name  string
		}{
			{
				name:  "no kind",
				input: &issuereportspb.IssueReportCreationInput{Details: "something"},
				want:  issuereports.ErrEmptyKind,
			},
			{
				name:  "no details",
				input: &issuereportspb.IssueReportCreationInput{Kind: "bug"},
				want:  issuereports.ErrEmptyDetails,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				_, err := h.server.CreateReport(h.ctx(t, testReporter),
					&issuereportspb.CreateReportRequest{Input: tc.input})
				must.Error(t, err)
				test.ErrorIs(t, err, tc.want)

				// codes.Internal is what the handler passed as its default; this
				// is what issuereports.GRPCMapper made of it over the preserved
				// chain.
				test.EqOp(t, codes.InvalidArgument, status.Code(err))
			})
		}
	})

	T.Run("a caller with no principal is unauthenticated", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateReport(t.Context(),
			&issuereportspb.CreateReportRequest{Input: creationInput()})
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, issuereportsgrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

// TestGetReport covers the read and the row-level question behind it.
func TestGetReport(T *testing.T) {
	T.Parallel()

	T.Run("a triager reads anybody's report", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)

		res, err := h.server.GetReport(h.ctx(t, triager),
			&issuereportspb.GetReportRequest{ReportId: report.ID})
		must.NoError(t, err)
		test.EqOp(t, report.ID, res.GetResult().GetId())
		test.EqOp(t, testReporter, res.GetResult().GetReporter())
	})

	T.Run("a caller the rule refuses is told the report is not there", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)

		res, err := h.server.GetReport(h.ctx(t, otherReporter),
			&issuereportspb.GetReportRequest{ReportId: report.ID})
		must.Error(t, err)
		test.Nil(t, res)

		// NotFound rather than PermissionDenied, and that is the point: the read
		// has already happened by the time the question is asked, so a refusal
		// that said so would tell a caller walking report ids which of them are
		// real.
		test.EqOp(t, codes.NotFound, status.Code(err))
		test.ErrorIs(t, err, issuereportsgrpc.ErrTargetNotPermitted)
	})

	T.Run("another tenant's report is not there either", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, otherScope, testReporter)

		// The same person, in the tenant the extractor puts them in. The scope
		// is bound into the statement rather than checked in front of it, so
		// this is the store's absence and not the authorizer's.
		_, err := h.server.GetReport(h.ctx(t, testReporter),
			&issuereportspb.GetReportRequest{ReportId: report.ID})
		must.Error(t, err)
		test.ErrorIs(t, err, issuereports.ErrReportNotFound)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("an identifier that names nothing is an absence", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.GetReport(h.ctx(t, triager),
			&issuereportspb.GetReportRequest{ReportId: "nonexistent"})
		must.Error(t, err)
		test.ErrorIs(t, err, issuereports.ErrReportNotFound)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("an authorizer that cannot decide is not a refusal", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithAuthorizer(t, &brokenAuthorizer{err: errAuthorizerUnavailable})
		report := h.seedReport(t, testScope, testReporter)

		_, err := h.server.GetReport(h.ctx(t, testReporter),
			&issuereportspb.GetReportRequest{ReportId: report.ID})
		must.Error(t, err)

		// codes.Internal, because a refusal is a sentence about the caller and
		// an outage is not.
		test.EqOp(t, codes.Internal, status.Code(err))
		test.ErrorIs(t, err, errAuthorizerUnavailable)
	})
}

// TestUpdateReport covers the revision, and the three fields it must not assign.
func TestUpdateReport(T *testing.T) {
	T.Parallel()

	T.Run("revises what the report says and leaves the rest of the row alone", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		report := h.seedReport(t, testScope, testReporter)
		moved := h.move(t, testScope, report.ID, issuereports.StatusOpen, issuereports.StatusAcknowledged, "")
		must.EqOp(t, issuereports.StatusAcknowledged, moved.Status)

		res, err := h.server.UpdateReport(h.ctx(t, triager), &issuereportspb.UpdateReportRequest{
			ReportId: report.ID,
			Input: &issuereportspb.IssueReportUpdateInput{
				Kind:        "billing",
				Details:     "actually it was the invoice",
				SubjectType: "invoice",
				SubjectId:   "invoice_9",
			},
		})
		must.NoError(t, err)

		result := res.GetResult()
		test.EqOp(t, "billing", result.GetKind())
		test.EqOp(t, "actually it was the invoice", result.GetDetails())
		test.EqOp(t, "invoice", result.GetSubjectType())
		test.EqOp(t, "invoice_9", result.GetSubjectId())

		// The three the input reserves: whose words these were, where the report
		// stands, and why. A revision that assigned any of them would be a
		// whole-row write that reopened a report somebody had decided.
		test.EqOp(t, testReporter, result.GetReporter())
		test.EqOp(t, issuereportspb.Status_STATUS_ACKNOWLEDGED, result.GetStatus())

		// The read-back is the stored row, so the stamp the database assigned is
		// on the response rather than the epoch.
		must.NotNil(t, result.GetLastUpdatedAt())
		test.True(t, result.GetLastUpdatedAt().AsTime().After(zeroTime()))
	})

	T.Run("a request that named no input is malformed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)

		res, err := h.server.UpdateReport(h.ctx(t, triager),
			&issuereportspb.UpdateReportRequest{ReportId: report.ID})
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, issuereportsgrpc.ErrNilReportInput)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		// Nothing was written, which is what the check before the transaction
		// buys: the report still says what it said.
		read, err := h.server.GetReport(h.ctx(t, triager),
			&issuereportspb.GetReportRequest{ReportId: report.ID})
		must.NoError(t, err)
		test.EqOp(t, "bug", read.GetResult().GetKind())
	})

	T.Run("a report in another tenant is an absence", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, otherScope, testReporter)

		_, err := h.server.UpdateReport(h.ctx(t, triager), &issuereportspb.UpdateReportRequest{
			ReportId: report.ID,
			Input:    &issuereportspb.IssueReportUpdateInput{Kind: "bug", Details: "still"},
		})
		must.Error(t, err)
		test.ErrorIs(t, err, issuereports.ErrReportNotFound)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}

// TestTransitionReport is the compare-and-set, which is the reason this surface
// is worth having.
func TestTransitionReport(T *testing.T) {
	T.Parallel()

	T.Run("moves the report and stores the note", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)

		res, err := h.server.TransitionReport(h.ctx(t, triager), &issuereportspb.TransitionReportRequest{
			ReportId:       report.ID,
			ExpectedStatus: issuereportspb.Status_STATUS_OPEN,
			TargetStatus:   issuereportspb.Status_STATUS_RESOLVED,
			Resolution:     "fixed in the next release",
		})
		must.NoError(t, err)

		result := res.GetResult()
		test.EqOp(t, issuereportspb.Status_STATUS_RESOLVED, result.GetStatus())
		test.EqOp(t, "fixed in the next release", result.GetResolution())

		// A terminal status stamps closed_at, and the response carries the stamp
		// this call assigned — which is what the caller records beside the move.
		must.NotNil(t, result.GetClosedAt())
	})

	T.Run("the second triager is refused rather than overwriting the first", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)

		first := &issuereportspb.TransitionReportRequest{
			ReportId:       report.ID,
			ExpectedStatus: issuereportspb.Status_STATUS_OPEN,
			TargetStatus:   issuereportspb.Status_STATUS_RESOLVED,
			Resolution:     "the first note",
		}

		_, err := h.server.TransitionReport(h.ctx(t, triager), first)
		must.NoError(t, err)

		// The same request again: a second person who read the report while it
		// was still open and decided from that read.
		second := &issuereportspb.TransitionReportRequest{
			ReportId:       report.ID,
			ExpectedStatus: issuereportspb.Status_STATUS_OPEN,
			TargetStatus:   issuereportspb.Status_STATUS_DECLINED,
			Resolution:     "the second note",
		}

		res, err := h.server.TransitionReport(h.ctx(t, triager), second)
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, issuereports.ErrStatusConflict)

		// codes.Aborted is gRPC's concurrency answer, and it is what tells a
		// console to re-read rather than to fix something first.
		test.EqOp(t, codes.Aborted, status.Code(err))

		// And the first note is still the note.
		read, err := h.server.GetReport(h.ctx(t, triager),
			&issuereportspb.GetReportRequest{ReportId: report.ID})
		must.NoError(t, err)
		test.EqOp(t, "the first note", read.GetResult().GetResolution())
		test.EqOp(t, issuereportspb.Status_STATUS_RESOLVED, read.GetResult().GetStatus())
	})

	T.Run("a move the lifecycle does not admit is refused before anything is written", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)
		h.move(t, testScope, report.ID, issuereports.StatusOpen, issuereports.StatusAcknowledged, "")

		// Acknowledged does not go back to open: somebody has seen it.
		_, err := h.server.TransitionReport(h.ctx(t, triager), &issuereportspb.TransitionReportRequest{
			ReportId:       report.ID,
			ExpectedStatus: issuereportspb.Status_STATUS_ACKNOWLEDGED,
			TargetStatus:   issuereportspb.Status_STATUS_OPEN,
		})
		must.Error(t, err)
		test.ErrorIs(t, err, issuereports.ErrInvalidStatusTransition)

		// InvalidArgument rather than FailedPrecondition: the pair of statuses is
		// wrong whatever the row holds.
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("a status the queue does not have is refused rather than matched against no row", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)

		for _, tc := range []struct {
			request *issuereportspb.TransitionReportRequest
			name    string
		}{
			{
				name: "no expected status",
				request: &issuereportspb.TransitionReportRequest{
					ReportId:     report.ID,
					TargetStatus: issuereportspb.Status_STATUS_RESOLVED,
				},
			},
			{
				name: "no target status",
				request: &issuereportspb.TransitionReportRequest{
					ReportId:       report.ID,
					ExpectedStatus: issuereportspb.Status_STATUS_OPEN,
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				_, err := h.server.TransitionReport(h.ctx(t, triager), tc.request)
				must.Error(t, err)

				// STATUS_UNSPECIFIED converts to the empty status, which the
				// store refuses by name rather than treating as a default.
				test.ErrorIs(t, err, issuereports.ErrUnknownStatus)
				test.EqOp(t, codes.InvalidArgument, status.Code(err))
			})
		}
	})

	T.Run("reopening clears the note and the stamp", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)
		h.move(t, testScope, report.ID, issuereports.StatusOpen, issuereports.StatusResolved, "it was fixed")

		res, err := h.server.TransitionReport(h.ctx(t, triager), &issuereportspb.TransitionReportRequest{
			ReportId:       report.ID,
			ExpectedStatus: issuereportspb.Status_STATUS_RESOLVED,
			TargetStatus:   issuereportspb.Status_STATUS_OPEN,
			Resolution:     "this note is not kept",
		})
		must.NoError(t, err)

		// A reopen goes to open rather than to acknowledged, because nobody has
		// dealt with it — and a reason that no longer holds is worse than none.
		test.EqOp(t, issuereportspb.Status_STATUS_OPEN, res.GetResult().GetStatus())
		test.EqOp(t, "", res.GetResult().GetResolution())
		test.Nil(t, res.GetResult().GetClosedAt())
	})
}

// TestArchiveReport covers removing a report from the queue.
func TestArchiveReport(T *testing.T) {
	T.Parallel()

	T.Run("the report leaves the queue", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)

		res, err := h.server.ArchiveReport(h.ctx(t, triager),
			&issuereportspb.ArchiveReportRequest{ReportId: report.ID})
		must.NoError(t, err)
		must.NotNil(t, res)

		// An archived report is not in the queue, and every read here addresses
		// the queue.
		_, err = h.server.GetReport(h.ctx(t, triager),
			&issuereportspb.GetReportRequest{ReportId: report.ID})
		must.Error(t, err)
		test.ErrorIs(t, err, issuereports.ErrReportNotFound)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("archiving one that is already archived is an absence", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, testScope, testReporter)

		_, err := h.server.ArchiveReport(h.ctx(t, triager),
			&issuereportspb.ArchiveReportRequest{ReportId: report.ID})
		must.NoError(t, err)

		_, err = h.server.ArchiveReport(h.ctx(t, triager),
			&issuereportspb.ArchiveReportRequest{ReportId: report.ID})
		must.Error(t, err)
		test.ErrorIs(t, err, issuereports.ErrReportNotFound)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("a report in another tenant is not archivable from here", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		report := h.seedReport(t, otherScope, testReporter)

		_, err := h.server.ArchiveReport(h.ctx(t, triager),
			&issuereportspb.ArchiveReportRequest{ReportId: report.ID})
		must.Error(t, err)
		test.ErrorIs(t, err, issuereports.ErrReportNotFound)

		// And it is still there for the tenant that owns it.
		read, err := h.server.GetReport(h.otherScopeCtx(t, triager),
			&issuereportspb.GetReportRequest{ReportId: report.ID})
		must.NoError(t, err)
		test.Nil(t, read.GetResult().GetArchivedAt())
	})
}
