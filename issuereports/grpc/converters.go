package grpc

import (
	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// The conversions between issuereports' types and the generated messages.
//
// The To-proto functions are exported because a consumer composing this queue
// into a larger response — a moderation console assembling a subject and the
// reports about it — otherwise writes the same twelve assignments and gets one
// of them wrong. The From-proto ones are not: they read a request, and a request
// is this service's to read.
//
// Three rules run through all of them. The nullable times stay unset rather than
// becoming the zero timestamp, because a client rendering "closed" wants to know
// there was no closing and 1970 is not that answer. No message carries a scope,
// so no converter reads or writes one — the scope is bound off the caller, and a
// Scope on a value handed to a write is the argument's. And no request carries a
// reporter into a write: the one that reads a report out of a request takes the
// name separately, from the principal, which is why it is a parameter here
// rather than a field there.

// ReportToProto renders a report for the wire.
func ReportToProto(r *issuereports.Report) *issuereportspb.IssueReport {
	if r == nil {
		return nil
	}

	out := &issuereportspb.IssueReport{
		CreatedAt:   timestamppb.New(r.CreatedAt),
		Id:          r.ID,
		Reporter:    r.Reporter,
		Kind:        r.Kind,
		Details:     r.Details,
		SubjectType: r.SubjectType,
		SubjectId:   r.SubjectID,
		Status:      StatusToProto(r.Status),
		Resolution:  r.Resolution,
	}

	if r.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*r.LastUpdatedAt)
	}

	if r.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*r.ArchivedAt)
	}

	if r.ClosedAt != nil {
		out.ClosedAt = timestamppb.New(*r.ClosedAt)
	}

	return out
}

// ReportsToProto renders a page of reports.
func ReportsToProto(reports []*issuereports.Report) []*issuereportspb.IssueReport {
	out := make([]*issuereportspb.IssueReport, 0, len(reports))
	for _, r := range reports {
		out = append(out, ReportToProto(r))
	}

	return out
}

// StatusToProto renders a status.
//
// A status this package does not serve renders as unspecified rather than
// panicking, and it is unreachable from a stored row: the store refuses a report
// whose status is not one of the four, so the column holds one of them.
func StatusToProto(s issuereports.Status) issuereportspb.Status {
	switch s {
	case issuereports.StatusOpen:
		return issuereportspb.Status_STATUS_OPEN
	case issuereports.StatusAcknowledged:
		return issuereportspb.Status_STATUS_ACKNOWLEDGED
	case issuereports.StatusResolved:
		return issuereportspb.Status_STATUS_RESOLVED
	case issuereports.StatusDeclined:
		return issuereportspb.Status_STATUS_DECLINED
	default:
		return issuereportspb.Status_STATUS_UNSPECIFIED
	}
}

// StatusFromProto reads a status off a request.
//
// The unspecified case becomes the empty Status rather than a default, so a
// request that named no status is refused by the store — with
// issuereports.ErrUnknownStatus, naming the value — instead of quietly becoming
// the open queue or the move the caller did not ask for. Choosing a default here
// would be this package deciding what a triager meant.
//
// It is exported for the same reason the renderer is: a consumer's own handler
// over this store reads the same enum off the same messages, and the mapping
// between four constants and four constants is exactly the thing a second copy
// gets wrong in one arm.
func StatusFromProto(s issuereportspb.Status) issuereports.Status {
	switch s {
	case issuereportspb.Status_STATUS_OPEN:
		return issuereports.StatusOpen
	case issuereportspb.Status_STATUS_ACKNOWLEDGED:
		return issuereports.StatusAcknowledged
	case issuereportspb.Status_STATUS_RESOLVED:
		return issuereports.StatusResolved
	case issuereportspb.Status_STATUS_DECLINED:
		return issuereports.StatusDeclined
	case issuereportspb.Status_STATUS_UNSPECIFIED:
		return ""
	default:
		return ""
	}
}

// reportFromCreationInput reads a report off a creation request, filed by the
// caller.
//
// A nil message is nil rather than an empty report, so a request that named no
// input is refused as malformed instead of being filed as a report with no kind
// and no details — which the store would refuse anyway, with a message about the
// kind rather than about the request.
//
// The reporter is a parameter and comes off the principal. Nothing about it is
// readable from the message, which reserves the name: a reporter a client could
// name is a report filed in somebody else's words. The status is not read
// either, and the store assigns it — a report is born open.
func reportFromCreationInput(in *issuereportspb.IssueReportCreationInput, reporter string) *issuereports.Report {
	if in == nil {
		return nil
	}

	return &issuereports.Report{
		Reporter:    reporter,
		Kind:        in.GetKind(),
		Details:     in.GetDetails(),
		SubjectType: in.GetSubjectType(),
		SubjectID:   in.GetSubjectId(),
	}
}

// applyUpdateInput writes a revision onto the report as stored.
//
// It revises rather than replaces, on the row the handler has just read inside
// its transaction, because three of the report's fields are not the caller's to
// send and the fourth is not the caller's to invent. The reporter, the status
// and the resolution stay whatever the row holds — a revision that assigned them
// would be a whole-row write that silently reopened a report somebody had just
// resolved, or filed one person's words under another's name.
//
// A nil message leaves the report alone, so the handler's refusal of a request
// that named no input is what answers one rather than a live report having its
// kind and its details blanked.
func applyUpdateInput(report *issuereports.Report, in *issuereportspb.IssueReportUpdateInput) {
	if report == nil || in == nil {
		return
	}

	report.Kind = in.GetKind()
	report.Details = in.GetDetails()
	report.SubjectType = in.GetSubjectType()
	report.SubjectID = in.GetSubjectId()
}
