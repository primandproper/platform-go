package grpc

import (
	"context"
	"errors"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/issuereports"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc/codes"
)

// ErrTargetNotPermitted indicates a caller who may make this call and may not
// make it about the person or the report this request named.
//
// It is identity/grpc's sentinel rather than a second one of this package's own,
// and deliberately the same value: a consumer has one rule about whose rows a
// caller has standing in, and an authorizer written for the directory refuses
// the report queue with the answer it already returns. An errors.Is against
// either name matches.
//
// It is never registered as a client-safe sentinel. Its text says the caller was
// refused, and half of what this package does with it is answer as though the
// row were absent — see the two refusal shapes at the bottom of this file.
var ErrTargetNotPermitted = identitygrpc.ErrTargetNotPermitted

// ReportAuthorizer decides whether the caller may act on the report a request
// named, or on the reports one person filed.
//
// # Why it exists
//
// This table has two audiences and one of them is the public. A person files a
// report, reads it back and lists what they have filed; a triager pages the
// queue and works it. The permission fragment in front of each RPC is a grant on
// the method — a holder of issues.reports.read may call GetReport, and nothing
// in a per-method check says whose report — and that is the finding the
// directory paid for first: a grant on the method made a member's management
// right directory-wide until something asked the second question. Here the rows
// are what your users said, in their own words, about things that happened to
// them.
//
// The consumer's authorization interceptor cannot answer it. It holds the
// request and the caller's grants, before the body is parsed, and has no handle
// to read a row with — so the question is asked from inside the handler, where
// the store and the client already are.
//
// It is not tenancy. The scope comes off the [Principal] and every store read
// filters on it, so nothing here crosses a deployment; this is the check inside
// one.
//
// # The two RPCs that ask, and the eight that do not
//
// GetReport asks after the read, holding the row, because whose report it is, is
// a fact about the row rather than about the request. ListReportsByReporter asks
// before it, holding the name the request supplied, because there is nothing to
// read yet and the name is the thing being gated.
//
// The other eight do not ask, and that is a ruling rather than an omission.
// CreateReport files a report in the caller's own name — the reporter comes off
// the principal, so there is no other person's row to name. The four queue
// listings and the three lifecycle writes are the triager's, and their target is
// the queue rather than a person: a grant to page every report in the tenant is
// the answer to "whose", spelled where a consumer's policy can audit it. A
// deployment that wants a narrower triage right names a narrower grant.
//
// # Why there is no default
//
// identity/grpc ships MembershipAuthorizer as its default and this ships none,
// and the difference is that the question here has two halves and this package
// can only answer one. Whether a report is the caller's own is a column it owns;
// whether this caller is a triager is a grant it cannot see, and every default
// available to it is wrong. One that permits everything hands one person's words
// about their own experience to anybody holding a read grant. One that permits
// only the caller's own — which is [ReporterAuthorizer], and is right for a
// deployment whose triage happens somewhere else — silently refuses the triage
// console every deployment eventually builds, which is discovered as a mystery
// rather than as a wiring failure.
//
// So it is positional and required, exactly as the principal extractor is, and
// [ErrNilReportAuthorizer] is what a server built without one is. A deployment
// with no triage console passes [ReporterAuthorizer]; one with a console
// composes it, which is what its documentation shows.
//
// # What implementations owe
//
// A nil error means permitted. [ErrTargetNotPermitted] means refused. Any other
// error is a failure to decide — a database that would not answer — and reaches
// the client as codes.Internal, which is what keeps an unavailable store from
// reading as a refusal. The three are distinguished by errors.Is, so an
// implementation may wrap the sentinel with context of its own and still be
// refusing.
type ReportAuthorizer interface {
	// AuthorizeReport is asked after a keyed read has resolved whose report it
	// is. The whole row is handed over rather than its reporter, because a
	// consumer's rule may turn on what the report is about as well as on who
	// filed it — a moderation team that reads reports about the things they
	// moderate is the ordinary shape of that.
	AuthorizeReport(ctx context.Context, caller Principal, report *issuereports.Report) error

	// AuthorizeReporter is asked before an RPC pages the reports one person
	// filed, with the name the request supplied.
	AuthorizeReporter(ctx context.Context, caller Principal, reporter string) error
}

// ReporterAuthorizer permits a caller their own reports and nothing else.
//
// It is not the default — there is none, and [ReportAuthorizer] says why — but
// it is the narrow half of every rule a deployment writes, so it is exported
// rather than left for each of them to re-derive. A deployment whose triage
// happens outside this surface passes it as it is:
//
//	srv, err := issuereportsgrpc.NewServer(store, client, extractPrincipal,
//		issuereportsgrpc.ReporterAuthorizer{})
//
// and one with a console in front of the queue composes it with whatever says
// that this caller is a triager:
//
//	type consoleAuthorizer struct{ issuereportsgrpc.ReporterAuthorizer }
//
//	func (a consoleAuthorizer) AuthorizeReport(ctx context.Context,
//		caller issuereportsgrpc.Principal, report *issuereports.Report) error {
//		if triages(caller) {
//			return nil
//		}
//
//		return a.ReporterAuthorizer.AuthorizeReport(ctx, caller, report)
//	}
//
// A caller with no identifier is refused rather than matched against a report
// filed by nobody: the store refuses to write one of those, but an authorizer
// that compared two empty strings and permitted would be a rule that turned an
// unauthenticated-looking principal into a key.
type ReporterAuthorizer struct{}

var _ ReportAuthorizer = ReporterAuthorizer{}

// AuthorizeReport permits a report the caller filed.
func (ReporterAuthorizer) AuthorizeReport(_ context.Context, caller Principal, report *issuereports.Report) error {
	if caller == nil || report == nil || caller.UserID() == "" {
		return ErrTargetNotPermitted
	}

	if report.Reporter == caller.UserID() {
		return nil
	}

	return ErrTargetNotPermitted
}

// AuthorizeReporter permits a caller naming themselves.
func (ReporterAuthorizer) AuthorizeReporter(_ context.Context, caller Principal, reporter string) error {
	if caller == nil || caller.UserID() == "" || reporter == "" {
		return ErrTargetNotPermitted
	}

	if reporter == caller.UserID() {
		return nil
	}

	return ErrTargetNotPermitted
}

// The two ways this surface asks the question, and the one thing that differs
// between them: what a refusal looks like to the client.
//
// They are helpers rather than five lines per method because the code is the
// part that can be got wrong quietly, and there are two of them.

// authorizeNamedReporter gates the RPC whose request named a person, and answers
// a refusal with codes.PermissionDenied.
//
// The caller learns nothing from it: a person whose reports they may not read
// and a person who has never filed one are refused by the same rule and answered
// the same way, so the code cannot be used to tell which identifiers are real.
func (s *Server) authorizeNamedReporter(
	ctx context.Context,
	req *request,
	reporter string,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	return s.outcome(req, s.targets.AuthorizeReporter(ctx, req.principal, reporter),
		codes.PermissionDenied, descriptionFmt, descriptionArgs...)
}

// authorizeReadReport gates the RPC that read a report and then asked whose it
// is, and answers a refusal with codes.NotFound.
//
// This is where this surface diverges from identity/grpc, and the divergence is
// the point rather than an inconsistency. There the question is asked ahead of
// the read, so a refusal is all the server can say. Here the read has already
// happened, and answering PermissionDenied would tell a caller walking report
// ids exactly which of them are real — the absent report says NotFound and the
// forbidden one would say something else. So both say NotFound, and a report id
// is worth no more to somebody guessing than it was before they sent it.
//
// The chain returned is still the refusal, which is what the log and the span
// record. Only the status the client reads is the absence, and
// [ErrTargetNotPermitted] is not client-safe, so its wording does not travel
// with it.
func (s *Server) authorizeReadReport(
	ctx context.Context,
	req *request,
	report *issuereports.Report,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	return s.outcome(req, s.targets.AuthorizeReport(ctx, req.principal, report),
		codes.NotFound, descriptionFmt, descriptionArgs...)
}

// outcome turns what the seam said into a status: the refusal code its caller
// chose, and codes.Internal for an authorizer that could not decide.
//
// The second is not a detail. A refusal is a sentence about the caller and an
// outage is not; reporting the second as the first tells a consumer to widen
// their policy while their database is down, and leaves a dashboard counting
// server faults reading zero through it.
func (s *Server) outcome(
	req *request,
	err error,
	refused codes.Code,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	if err == nil {
		return nil
	}

	code := codes.Internal
	if errors.Is(err, ErrTargetNotPermitted) {
		code = refused
	}

	return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), code,
		descriptionFmt, descriptionArgs...)
}
